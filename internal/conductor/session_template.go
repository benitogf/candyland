package conductor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
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
	RoleReviewer:    {"flows/principles/truthseeker", "core/review-rigor"},
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
	CreatedAt       string `json:"createdAt"`
}

// templateKey is the storage key for a role's template in a repo. Keyed by the
// repo BASENAME (like the per-repo worktree layout) — the template session is
// created in that repo's directory, which is where a fork resolves it.
func templateKey(role, repo string) string {
	return "templates/" + role + "/" + repoBase(repo)
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
// unprobeable binary still yields stable (never thrashing) entries.
func binVersion(bin string) string {
	binVersions.mu.Lock()
	defer binVersions.mu.Unlock()
	if v, ok := binVersions.m[bin]; ok {
		return v
	}
	cmd := exec.Command(bin, "--version")
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

	if id, valid := c.storedTemplate(role, key); valid {
		f.id, f.ok = id, true
		return id, true
	}
	f.id, f.ok = c.createTemplate(role, repo, key, docs)
	return f.id, f.ok
}

// storedTemplate reads the persisted entry and validates every stamped
// coordinate against the current environment. Any mismatch (or an absent /
// unreadable entry) is a miss — the caller recreates.
func (c *Conductor) storedTemplate(role, key string) (string, bool) {
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
		e.Model != model || e.Thinking != thinking {
		return "", false
	}
	return e.SessionID, true
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
	if cfg := templateMCPConfig(); cfg != "" {
		defer os.Remove(cfg)
		args = append(args, "--mcp-config", cfg)
	}
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
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		var line streamLine
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Type == "result" {
			sawResult = true
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
// character of the absolute cwd maps to '-' (so both '/' and '.' become '-').
func projectDirName(cwd string) string {
	b := []byte(cwd)
	for i, ch := range b {
		alnum := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if !alnum {
			b[i] = '-'
		}
	}
	return string(b)
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
