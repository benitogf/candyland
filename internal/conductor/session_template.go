package conductor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A session template is a pre-warmed claude session that has already loaded a
// role's doctrine (via the detritus kb_get MCP tool) and nothing else. Agent
// spawns fork it (`--resume <id> --fork-session`, see spawnOpts.forkFrom) so
// they start with the doctrine in context instead of re-reading it cold every
// spawn. Templates are an OPTIMIZATION: every path in this file degrades to
// ("", false) — a cold start — and must never fail a run.

// roleDoctrine maps a settings role to the detritus knowledge documents its
// template session pre-loads. A role with no entry (e.g. the escalation
// decider) gets no template and always starts cold.
var roleDoctrine = map[string][]string{
	RoleCoder:       {"flows/principles/coding-style", "flows/principles/line-of-sight"},
	RoleFix:         {"flows/principles/coding-style", "flows/principles/line-of-sight"},
	RoleQuestLead:   {"flows/principles/truthseeker", "core/loop", "core/todo-audit", "core/completion"},
	RoleReviewer:    {"flows/principles/truthseeker", "core/review-rigor", "roles/reviewer"},
	RoleTechLead:    {"flows/principles/truthseeker", "core/completion", "roles/tech-lead"},
	RoleIntentLead:  {"flows/principles/truthseeker", "core/planning", "core/dream"},
	RoleTechManager: {"flows/principles/truthseeker", "roles/tech-lead", "core/completion"},
	// The intent manager also judges gate 1 (partitionReviewBootstrap applies
	// core/planning), so its template pre-loads that doctrine too.
	RoleIntentManager:  {"flows/principles/truthseeker", "core/planning", "core/intent-review"},
	RoleIntentReviewer: {"flows/principles/truthseeker", "core/intent-review"},
}

// sessionReuseEnabled is the kill switch: CANDYLAND_SESSION_REUSE=0 disables
// template creation and reuse entirely (every spawn starts cold). Default on.
func sessionReuseEnabled() bool {
	return os.Getenv("CANDYLAND_SESSION_REUSE") != "0"
}

// templateTimeout is the hard wall clock for one template-creation spawn. A
// template loads a few documents and replies READY, so it is bounded well below
// a work attempt; a creation that overruns is killed and the caller starts cold.
func templateTimeout() time.Duration { return envDur("CANDYLAND_TEMPLATE_TIMEOUT_MS", 10*60*1000) }

// sessionTemplate is the persisted registry entry under templates/<role>/<repoBase>.
// An entry is only valid while every stamped coordinate still matches the
// current environment — a CLI upgrade, a doctrine (detritus) upgrade, or a
// settings change for the role each invalidate it, forcing a fresh template.
type sessionTemplate struct {
	SessionID       string `json:"sessionID"`
	ClaudeVersion   string `json:"claudeVersion"`
	DetritusVersion string `json:"detritusVersion"`
	Model           string `json:"model"`
	Thinking        string `json:"thinking"`
	Docs            string `json:"docs"`
	CreatedAt       string `json:"createdAt"`
}

// templateKey is the storage key for a role's template in a repo: the repo
// BASENAME for readability plus a short hash of the full path — two repos that
// share a basename must not share an entry, because the session transcript
// exists only under the creating repo's project dir and the other repo's forks
// would pay a doomed fork + cold fallback on every spawn, forever.
func templateKey(role, repo string) string {
	h := fnv.New32a()
	h.Write([]byte(repo))
	return fmt.Sprintf("templates/%s/%s-%08x", role, repoBase(repo), h.Sum32())
}

// --- binary version stamping (cached per process) ---------------------------

// binVersions caches `<bin> --version` per binary path for the process
// lifetime — the version can only change with a reinstall, which in practice
// comes with a candyland restart. Keyed by path so a test's stub gets its own
// probe.
var binVersions = struct {
	mu sync.Mutex
	m  map[string]string
}{m: map[string]string{}}

// binVersion probes and caches a binary's --version (first output line).
// "unknown" when the probe fails — stamped and compared consistently, so an
// unprobeable binary still yields stable (never thrashing) entries. The probe
// is hard-bounded: it runs on every templated spawn's registry lookup while
// holding the cache lock, so a hung binary must degrade to "unknown" rather
// than wedging every spawn site (the feature's never-fail-a-run contract).
func binVersion(bin string) string {
	binVersions.mu.Lock()
	defer binVersions.mu.Unlock()
	if v, ok := binVersions.m[bin]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = claudeEnv()
	configureProc(cmd)
	out, err := cmd.Output()
	v := "unknown"
	if err == nil {
		if fl := firstLine(string(out)); fl != "" {
			v = fl
		}
	}
	binVersions.m[bin] = v
	return v
}

func claudeVersion() string { return binVersion(claudeBin()) }

// detritusVersion stamps the doctrine source's version: a detritus upgrade can
// change the documents a template loaded, so it must invalidate the template.
func detritusVersion() string {
	bin := resolveDetritusBin()
	if bin == "" {
		return "unknown"
	}
	return binVersion(bin)
}

// --- registry ----------------------------------------------------------------

// templateFlight is one in-flight get-or-create; waiters block on done and read
// the result. The coder fan-out requests the same (role, repo) template from N
// goroutines at once — only one creation may spawn claude.
type templateFlight struct {
	done chan struct{}
	id   string
	ok   bool
}

// flightKey scopes the singleflight to a conductor instance, so tests running
// several conductors in one process can't cross-wait on each other's creations.
type flightKey struct {
	c   *Conductor
	key string
}

var templateFlights = struct {
	mu sync.Mutex
	m  map[flightKey]*templateFlight
}{m: map[flightKey]*templateFlight{}}

// templateFor returns the session id of a valid doctrine template for (role,
// repo), creating and persisting one on a miss. ok=false — a cold start — when
// the kill switch is off, the role has no doctrine entry, there is no storage,
// or creation fails for any reason (never a run failure). Concurrent calls for
// the same key coalesce into a single creation.
func (c *Conductor) templateFor(role, repo string) (sessionID string, ok bool) {
	if !sessionReuseEnabled() {
		return "", false
	}
	docs, hasDoctrine := roleDoctrine[role]
	if !hasDoctrine || c.server == nil {
		return "", false
	}
	// claude (node) resolves its cwd, so a relative, trailing-slash, or
	// symlinked repo path would make claude write the transcript under a
	// different escaped project dir than the one we stat/copy — every spawn
	// would silently miss and recreate. Canonicalize once, up front.
	repo = canonPath(repo)
	key := templateKey(role, repo)
	fk := flightKey{c: c, key: key}

	templateFlights.mu.Lock()
	if f, running := templateFlights.m[fk]; running {
		templateFlights.mu.Unlock()
		<-f.done
		return f.id, f.ok
	}
	f := &templateFlight{done: make(chan struct{})}
	templateFlights.m[fk] = f
	templateFlights.mu.Unlock()
	defer func() {
		templateFlights.mu.Lock()
		delete(templateFlights.m, fk)
		templateFlights.mu.Unlock()
		close(f.done)
	}()

	if id, valid := c.storedTemplate(role, repo, key); valid {
		f.id, f.ok = id, true
		return id, true
	}
	f.id, f.ok = c.createTemplate(role, repo, key, docs)
	return f.id, f.ok
}

// storedTemplate reads the persisted entry and validates every stamped
// coordinate against the current environment. Any mismatch (or an absent /
// unreadable entry) is a miss — the caller recreates.
func (c *Conductor) storedTemplate(role, repo, key string) (string, bool) {
	obj, err := c.server.Storage.Get(key)
	if err != nil {
		return "", false
	}
	var e sessionTemplate
	if json.Unmarshal(obj.Data, &e) != nil {
		return "", false
	}
	model, thinking := c.agentConfig(role)
	if e.SessionID == "" ||
		e.ClaudeVersion != claudeVersion() ||
		e.DetritusVersion != detritusVersion() ||
		e.Model != model || e.Thinking != thinking ||
		e.Docs != strings.Join(roleDoctrine[role], ",") {
		return "", false
	}
	// The stamps can all match while the transcript is gone — claude garbage-
	// collects old session files on its own schedule. Without this check every
	// spawn would pay a doomed fork + cold rerun forever (the entry never
	// invalidates in a stable environment); treating a missing transcript as a
	// miss lets recreation rewrite the file and heal.
	if root, err := claudeProjectsRoot(); err != nil ||
		!fileExists(filepath.Join(root, projectDirName(repo), e.SessionID+".jsonl")) {
		return "", false
	}
	return e.SessionID, true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// createTemplate mints a session id, runs one bounded claude spawn in the repo
// dir that loads the role's doctrine via kb_get and replies READY, and persists
// the entry. Any failure logs once and returns ok=false — the caller (and every
// waiter on the flight) starts cold.
func (c *Conductor) createTemplate(role, repo, key string, docs []string) (string, bool) {
	model, thinking := c.agentConfig(role)
	sessionID, err := newSessionID()
	if err != nil {
		log.Printf("candyland: session template %s: could not mint a session id: %v", key, err)
		return "", false
	}
	// The template spawn is NOT streamOnce: streamOnce writes agent events onto a
	// host run record, and a template belongs to no run. A lean bounded exec with
	// the same argv contract (`-p` first — the stub reads $2) is enough.
	args := []string{"-p", doctrineBootstrap(docs), "--output-format", "stream-json", "--verbose",
		"--model", model, "--dangerously-skip-permissions", "--session-id", sessionID}
	if thinking != "" {
		args = append(args, "--effort", thinking)
	}
	// No kb_get surface → no template. A doctrine-load spawn without the detritus
	// MCP config cannot load anything; the cold path is strictly better (its
	// busMCPConfig also layers the origin session's inherited servers).
	cfg := templateMCPConfig()
	if cfg == "" {
		log.Printf("candyland: session template %s: detritus MCP unavailable (spawns start cold)", key)
		return "", false
	}
	defer os.Remove(cfg)
	args = append(args, "--mcp-config", cfg)
	if err := runTemplateSpawn(repo, args); err != nil {
		log.Printf("candyland: session template %s: creation failed (spawns start cold): %v", key, err)
		return "", false
	}
	entry := sessionTemplate{
		SessionID:       sessionID,
		ClaudeVersion:   claudeVersion(),
		DetritusVersion: detritusVersion(),
		Model:           model,
		Thinking:        thinking,
		Docs:            strings.Join(docs, ","),
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(entry)
	if err != nil {
		log.Printf("candyland: session template %s: marshal: %v", key, err)
		return "", false
	}
	if _, err := c.server.Storage.Set(key, json.RawMessage(b)); err != nil {
		log.Printf("candyland: session template %s: persist failed (spawns start cold): %v", key, err)
		return "", false
	}
	return sessionID, true
}

// doctrineBootstrap is the template spawn's whole prompt: load each mapped
// document via the detritus kb_get MCP tool, then reply READY. NO task content
// — the task arrives when a real spawn forks the session.
func doctrineBootstrap(docs []string) string {
	var b strings.Builder
	b.WriteString("You are pre-loading doctrine for a reusable session template. " +
		"Load each of the following knowledge documents by calling the kb_get tool, one call per document, in order:")
	for _, d := range docs {
		b.WriteString(` kb_get name="` + d + `".`)
	}
	b.WriteString(" Do no other work: do not read or modify files, do not run commands, do not summarize the documents." +
		" When every document is loaded, reply with exactly: READY")
	return b.String()
}

// templateMCPConfig writes a throwaway --mcp-config exposing ONLY the detritus
// stdio server — the same kb_get surface busMCPConfig gives production agents,
// minus the per-run comms endpoint a run-less template has no use for. Returns
// "" (flag omitted, degraded) when the detritus binary doesn't resolve or the
// file can't be written; the caller removes the file after the spawn.
func templateMCPConfig() string {
	bin := resolveDetritusBin()
	if bin == "" {
		return ""
	}
	data, err := json.Marshal(mcpConfigFile{MCPServers: map[string]mcpServerSpec{
		"detritus": {Command: bin, Args: []string{}},
	}})
	if err != nil {
		return ""
	}
	f, err := os.CreateTemp("", "candyland-template-mcp-*.json")
	if err != nil {
		return ""
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return ""
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return ""
	}
	return f.Name()
}

// runTemplateSpawn runs one claude process to completion in dir under a hard
// wall clock, scanning its stream-json for a terminal result line. Clean exit +
// a result seen is success; anything else is an error the caller logs.
func runTemplateSpawn(dir string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), templateTimeout())
	defer cancel()
	cmd := exec.Command(claudeBin(), args...)
	cmd.Dir = dir
	cmd.Env = claudeEnv()
	configureProc(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		return err
	}
	afterStart(cmd)
	// Kill the whole tree on timeout; procDone releases the watcher on a clean end.
	procDone := make(chan struct{})
	defer close(procDone)
	go func() {
		select {
		case <-ctx.Done():
			killTree(cmd)
		case <-procDone:
		}
	}()
	sawResult := false
	resultText := ""
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		var line streamLine
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Type == "result" {
			sawResult = true
			resultText = line.Result
		}
	}
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return fmt.Errorf("timed out after %s", templateTimeout())
	}
	if werr != nil {
		return fmt.Errorf("claude exited: %v (%s)", werr, firstLine(stderr.String()))
	}
	if !sawResult {
		return errors.New("the stream ended with no result line")
	}
	// The doctrine bootstrap ends with "reply with exactly: READY" — anything
	// else means the doctrine did NOT load (kb_get unavailable, a doc renamed,
	// the model improvising). Persisting such a session would be worse than
	// cold: every fork would "APPLY the doctrine already loaded" over nothing.
	if strings.TrimSpace(resultText) != "READY" {
		return fmt.Errorf("doctrine load not confirmed: result was %q, want READY", truncate(firstLine(resultText), 120))
	}
	return nil
}

// newSessionID mints a random v4 UUID — claude's --session-id requires UUID form.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// --- worktree delivery ---------------------------------------------------------

// claudeProjectsRoot is where claude stores session transcripts, one directory
// per project cwd: ~/.claude/projects/<escaped-cwd>/<session-id>.jsonl.
// Overridable via CANDYLAND_CLAUDE_PROJECTS_DIR (tests).
func claudeProjectsRoot() (string, error) {
	if d := os.Getenv("CANDYLAND_CLAUDE_PROJECTS_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// projectDirName is claude's project-directory escaping: every non-alphanumeric
// RUNE of the absolute cwd maps to one '-' (so '/', '.', and a multi-byte
// character each become a single dash — verified against real claude with a
// non-ASCII cwd: ".../tëst-ünï" → ".../t-st--n-", one dash per rune, not per
// UTF-8 byte).
func projectDirName(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if alnum {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// copySessionForCwd makes a template session forkable from another working
// directory: claude resolves --resume per project directory, so a session
// created in the repo must be copied into the worktree's project directory
// before a spawn there can fork it.
func copySessionForCwd(sessionID, fromCwd, toCwd string) error {
	root, err := claudeProjectsRoot()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(root, projectDirName(fromCwd), sessionID+".jsonl"))
	if err != nil {
		return err
	}
	dstDir := filepath.Join(root, projectDirName(toCwd))
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dstDir, sessionID+".jsonl"), data, 0o600)
}

// templateForWorkdir is the spawn-site convenience: get-or-create the (role,
// repo) template and, when the spawn runs somewhere other than the repo (a
// worktree), copy the session there so the fork resolves. A copy failure logs
// and returns ok=false — the spawn starts cold, never fails.
func (c *Conductor) templateForWorkdir(role, repo, workdir string) (string, bool) {
	repo, workdir = canonPath(repo), canonPath(workdir)
	id, ok := c.templateFor(role, repo)
	if !ok {
		return "", false
	}
	if workdir == repo {
		return id, true
	}
	if err := copySessionForCwd(id, repo, workdir); err != nil {
		log.Printf("candyland: session template %s: copy to %s failed (spawn starts cold): %v", templateKey(role, repo), workdir, err)
		return "", false
	}
	return id, true
}

// canonPath resolves a path the way claude's own cwd resolution will see it
// (absolute, symlinks resolved, no trailing separator) so the project-dir
// derivation matches where transcripts actually land. Best-effort: on error
// the cleaned absolute form is used.
func canonPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return p
}

// invalidateTemplate drops a role's registry entry after a fork against it
// failed to resolve — the transcript is gone or unusable in a way the stamp
// check can't see, so without this every later spawn would pay the same
// doomed fork + cold rerun until an unrelated stamp change. The next spawn
// recreates the template.
func (c *Conductor) invalidateTemplate(role, repo string) {
	if c.server == nil {
		return
	}
	key := templateKey(role, canonPath(repo))
	if err := c.server.Storage.Del(key); err != nil {
		log.Printf("candyland: session template %s: invalidate after unresolved fork: %v", key, err)
		return
	}
	log.Printf("candyland: session template %s: dropped after an unresolved fork (next spawn recreates)", key)
}

// cleanupTemplateCopy removes the transcript copy a worktree spawn used —
// worktrees are throwaway, and nothing else ever deletes these files, so
// leaving them accumulates one orphan jsonl per worktree per run, forever.
// Only the copied template file is removed; transcripts the forked session
// itself wrote stay untouched. Best-effort.
func cleanupTemplateCopy(sessionID, workdir string) {
	if sessionID == "" {
		return
	}
	root, err := claudeProjectsRoot()
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(root, projectDirName(canonPath(workdir)), sessionID+".jsonl"))
}
