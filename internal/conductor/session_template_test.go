package conductor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// === Session-template registry ==============================================
//
// templateFor get-or-creates a doctrine-loaded template session per (role,
// repo): one bounded stub-claude spawn on a miss, a storage read on a hit, and
// ("", false) — a cold start — on the kill switch, a doctrine-less role, or any
// failure. The stub records every spawn in a counter file so the tests can pin
// "exactly one creation" (caching, singleflight) and "zero spawns" (kill switch).

// TestMain defaults the session-reuse kill switch OFF for the whole package:
// the pre-existing run/quest/campaign tests sequence their stub-claude scripts
// by invocation count, and an implicit synchronous template creation at a spawn
// site would corrupt that accounting. Template/fork tests opt back in
// deliberately (templateConductor sets CANDYLAND_SESSION_REUSE=1). An explicit
// value already present in the environment is respected.
func TestMain(m *testing.M) {
	if _, set := os.LookupEnv("CANDYLAND_SESSION_REUSE"); !set {
		os.Setenv("CANDYLAND_SESSION_REUSE", "0")
	}
	os.Exit(m.Run())
}

// templateStubClaude speaks the two contracts the registry uses: a --version
// probe (stamped into the entry) and a template-creation spawn — it logs the
// spawn + argv to $CANDYLAND_TEMPLATE_FIXTURE, echoes back the --session-id it
// was given on an init line, and ends with a READY result.
const templateStubClaude = `#!/usr/bin/env bash
if [[ "$1" == "--version" ]]; then echo "9.9.9 (stub)"; exit 0; fi
echo spawn >> "$CANDYLAND_TEMPLATE_FIXTURE"
echo "$@" >> "$CANDYLAND_TEMPLATE_FIXTURE.args"
sid=""
prev=""
for a in "$@"; do
  if [[ "$prev" == "--session-id" ]]; then sid="$a"; fi
  prev="$a"
done
dir="$CANDYLAND_CLAUDE_PROJECTS_DIR/$(pwd | sed 's/[^a-zA-Z0-9]/-/g')"
mkdir -p "$dir"
echo '{"doctrine":true}' > "$dir/$sid.jsonl"
echo "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$sid\"}"
echo '{"type":"result","subtype":"success","result":"READY","usage":{"output_tokens":10}}'
`

// templateConductor wires a storage-backed conductor plus the stub claude, a
// stub detritus (so detritusVersion stamps deterministically), and a fresh
// spawn-counter fixture. Returns the conductor and a repo-ish dir to key on.
func templateConductor(t *testing.T, claudeScript string) (*Conductor, string, string) {
	t.Helper()
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := New(srv)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(os.Interrupt) })
	writeFakeClaude(t, claudeScript)
	// The package default (TestMain) and writeFakeClaude both force the reuse
	// kill switch OFF; the registry tests ARE the template machinery, so turn
	// it back on after the harness call.
	t.Setenv("CANDYLAND_SESSION_REUSE", "1")
	writeFakeDetritus(t)
	// Success stubs write the template transcript here — storedTemplate treats
	// a missing transcript as a miss (claude GCs old session files).
	t.Setenv("CANDYLAND_CLAUDE_PROJECTS_DIR", t.TempDir())
	fixture := filepath.Join(t.TempDir(), "template")
	t.Setenv("CANDYLAND_TEMPLATE_FIXTURE", fixture)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	return c, repo, fixture
}

// writeFakeDetritus pins DETRITUS_BIN to a stub whose --version is stable, so
// entries stamp "detritus 7.7.7" instead of whatever is (or isn't) on PATH.
func writeFakeDetritus(t *testing.T) {
	t.Helper()
	fake := filepath.Join(t.TempDir(), "detritus")
	if err := os.WriteFile(fake, []byte("#!/usr/bin/env bash\necho \"detritus 7.7.7\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DETRITUS_BIN", fake)
}

// spawnCount reads how many template-creation spawns the stub recorded.
func spawnCount(t *testing.T, fixture string) int {
	t.Helper()
	b, err := os.ReadFile(fixture)
	if err != nil {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(string(b)), "\n"))
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The doctrine map is pinned per role: a template that pre-loads different
// documents than the role's bootstrap APPLIES would fork the wrong doctrine
// (e.g. the gate-1 intent manager applies core/planning per
// partitionReviewBootstrap, so its template must pre-load it).
func TestRoleDoctrineMap(t *testing.T) {
	want := map[string][]string{
		RoleCoder:          {"flows/principles/coding-style", "flows/principles/line-of-sight"},
		RoleFix:            {"flows/principles/coding-style", "flows/principles/line-of-sight"},
		RoleQuestLead:      {"flows/principles/truthseeker", "core/loop", "core/todo-audit", "core/completion"},
		RoleReviewer:       {"flows/principles/truthseeker", "core/review-rigor"},
		RoleTechLead:       {"flows/principles/truthseeker", "core/completion", "roles/tech-lead"},
		RoleIntentLead:     {"flows/principles/truthseeker", "core/planning", "core/dream"},
		RoleTechManager:    {"flows/principles/truthseeker", "roles/tech-lead", "core/completion"},
		RoleIntentManager:  {"flows/principles/truthseeker", "core/planning", "core/intent-review"},
		RoleIntentReviewer: {"flows/principles/truthseeker", "core/intent-review"},
	}
	if len(roleDoctrine) != len(want) {
		t.Errorf("roleDoctrine has %d entries, want %d", len(roleDoctrine), len(want))
	}
	for role, docs := range want {
		if !slices.Equal(roleDoctrine[role], docs) {
			t.Errorf("roleDoctrine[%s]\n got %v\nwant %v", role, roleDoctrine[role], docs)
		}
	}
}

// Creation persists a fully stamped entry and returns the minted UUID; the
// spawn's argv carries the doctrine bootstrap, the role's model/effort, the
// minted --session-id, and the detritus --mcp-config for kb_get.
func TestTemplateForCreatesAndPersists(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateStubClaude)

	id, ok := c.templateFor(RoleCoder, repo)
	if !ok {
		t.Fatal("creation must succeed")
	}
	if !uuidRe.MatchString(id) {
		t.Fatalf("session id %q is not a v4 UUID", id)
	}
	if got := spawnCount(t, fixture); got != 1 {
		t.Fatalf("want exactly 1 creation spawn, got %d", got)
	}

	argv, err := os.ReadFile(fixture + ".args")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--session-id " + id,
		"--model claude-opus-4-8", // RoleCoder default
		"--effort low",            // RoleCoder default thinking
		"--mcp-config",
		`kb_get name="flows/principles/coding-style"`,
		`kb_get name="flows/principles/line-of-sight"`,
		"reply with exactly: READY",
	} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("creation argv missing %q:\n%s", want, argv)
		}
	}

	obj, err := c.server.Storage.Get(templateKey(RoleCoder, repo))
	if err != nil {
		t.Fatalf("entry not persisted: %v", err)
	}
	var e sessionTemplate
	if err := json.Unmarshal(obj.Data, &e); err != nil {
		t.Fatal(err)
	}
	if e.SessionID != id {
		t.Errorf("persisted sessionID = %q, want %q", e.SessionID, id)
	}
	if e.ClaudeVersion != "9.9.9 (stub)" {
		t.Errorf("claudeVersion = %q, want the stub's probe output", e.ClaudeVersion)
	}
	if e.DetritusVersion != "detritus 7.7.7" {
		t.Errorf("detritusVersion = %q, want the stub detritus probe", e.DetritusVersion)
	}
	if e.Model != "claude-opus-4-8" || e.Thinking != "low" {
		t.Errorf("model/thinking = %q/%q, want the coder defaults", e.Model, e.Thinking)
	}
	if e.CreatedAt == "" {
		t.Error("createdAt must be stamped")
	}
}

// A second call is a cache hit: the same id comes back with NO new spawn.
func TestTemplateForSecondCallCached(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateStubClaude)

	first, ok := c.templateFor(RoleReviewer, repo)
	if !ok {
		t.Fatal("creation must succeed")
	}
	second, ok := c.templateFor(RoleReviewer, repo)
	if !ok || second != first {
		t.Fatalf("cached call = (%q, %v), want (%q, true)", second, ok, first)
	}
	if got := spawnCount(t, fixture); got != 1 {
		t.Fatalf("the second call must not spawn: want 1 spawn, got %d", got)
	}
}

// Each stamped coordinate independently invalidates the entry: a mismatch on
// claudeVersion, detritusVersion, model, or thinking forces a re-creation.
func TestTemplateForInvalidation(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateStubClaude)
	key := templateKey(RoleCoder, repo)

	prev, ok := c.templateFor(RoleCoder, repo)
	if !ok {
		t.Fatal("creation must succeed")
	}
	spawns := 1

	tamper := func(mutate func(*sessionTemplate)) {
		t.Helper()
		obj, err := c.server.Storage.Get(key)
		if err != nil {
			t.Fatal(err)
		}
		var e sessionTemplate
		if err := json.Unmarshal(obj.Data, &e); err != nil {
			t.Fatal(err)
		}
		mutate(&e)
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.server.Storage.Set(key, json.RawMessage(b)); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name   string
		mutate func(*sessionTemplate)
	}{
		{"claudeVersion", func(e *sessionTemplate) { e.ClaudeVersion = "0.0.1 (older)" }},
		{"detritusVersion", func(e *sessionTemplate) { e.DetritusVersion = "detritus 0.0.1" }},
		{"model", func(e *sessionTemplate) { e.Model = "claude-sonnet-5" }},
		{"thinking", func(e *sessionTemplate) { e.Thinking = "max" }},
	}
	for _, tc := range cases {
		tamper(tc.mutate)
		id, ok := c.templateFor(RoleCoder, repo)
		if !ok {
			t.Fatalf("%s mismatch: re-creation must succeed", tc.name)
		}
		if id == prev {
			t.Errorf("%s mismatch must mint a NEW session, still %q", tc.name, id)
		}
		spawns++
		if got := spawnCount(t, fixture); got != spawns {
			t.Fatalf("%s mismatch: want %d spawns, got %d", tc.name, spawns, got)
		}
		prev = id
	}
}

// The kill switch and the structural misses all return ("", false) with ZERO
// spawns: CANDYLAND_SESSION_REUSE=0, a role with no doctrine entry, no server.
func TestTemplateForColdStartPaths(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateStubClaude)

	t.Setenv("CANDYLAND_SESSION_REUSE", "0")
	if id, ok := c.templateFor(RoleCoder, repo); ok || id != "" {
		t.Errorf("kill switch off: got (%q, %v), want (\"\", false)", id, ok)
	}
	t.Setenv("CANDYLAND_SESSION_REUSE", "1")

	if id, ok := c.templateFor("escalation", repo); ok || id != "" {
		t.Errorf("doctrine-less role: got (%q, %v), want (\"\", false)", id, ok)
	}
	serverless := New(nil)
	if id, ok := serverless.templateFor(RoleCoder, repo); ok || id != "" {
		t.Errorf("serverless conductor: got (%q, %v), want (\"\", false)", id, ok)
	}
	if got := spawnCount(t, fixture); got != 0 {
		t.Fatalf("cold-start paths must never spawn, got %d spawns", got)
	}
}

func TestSessionReuseEnabled(t *testing.T) {
	t.Setenv("CANDYLAND_SESSION_REUSE", "")
	if !sessionReuseEnabled() {
		t.Error("default must be ON")
	}
	t.Setenv("CANDYLAND_SESSION_REUSE", "0")
	if sessionReuseEnabled() {
		t.Error("CANDYLAND_SESSION_REUSE=0 must disable")
	}
}

// A stub that always fails: creation returns ok=false, persists nothing, and a
// later call honestly retries (no negative caching of a transient failure).
const templateFailingClaude = `#!/usr/bin/env bash
if [[ "$1" == "--version" ]]; then echo "9.9.9 (stub)"; exit 0; fi
echo spawn >> "$CANDYLAND_TEMPLATE_FIXTURE"
echo "template creation exploded" >&2
exit 1
`

func TestTemplateForCreationFailure(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateFailingClaude)

	if id, ok := c.templateFor(RoleCoder, repo); ok || id != "" {
		t.Errorf("failed creation: got (%q, %v), want (\"\", false)", id, ok)
	}
	if _, err := c.server.Storage.Get(templateKey(RoleCoder, repo)); err == nil {
		t.Error("a failed creation must not persist an entry")
	}
	if _, ok := c.templateFor(RoleCoder, repo); ok {
		t.Error("still failing")
	}
	if got := spawnCount(t, fixture); got != 2 {
		t.Fatalf("each miss retries the creation: want 2 spawns, got %d", got)
	}
}

// A stub whose creation spawn ends WITHOUT confirming the doctrine load — the
// result is chatter, not READY (the shape of a spawn whose kb_get tool was
// missing or errored mid-load). Persisting it would fork doctrine-less
// sessions whose slim bootstraps "APPLY the doctrine already loaded" over
// nothing — strictly worse than cold.
const templateNotReadyClaude = `#!/usr/bin/env bash
if [[ "$1" == "--version" ]]; then echo "9.9.9 (stub)"; exit 0; fi
echo spawn >> "$CANDYLAND_TEMPLATE_FIXTURE"
echo '{"type":"system","subtype":"init","session_id":"sess-x"}'
echo '{"type":"result","subtype":"success","result":"I do not have a kb_get tool available.","usage":{"output_tokens":10}}'
`

func TestTemplateForRequiresReadyConfirmation(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateNotReadyClaude)

	if id, ok := c.templateFor(RoleCoder, repo); ok || id != "" {
		t.Errorf("non-READY creation: got (%q, %v), want (\"\", false)", id, ok)
	}
	if _, err := c.server.Storage.Get(templateKey(RoleCoder, repo)); err == nil {
		t.Error("a creation that never confirmed READY must not persist an entry")
	}
	if got := spawnCount(t, fixture); got != 1 {
		t.Fatalf("want exactly 1 creation spawn, got %d", got)
	}
}

// Without a resolvable detritus binary the creation spawn would have no kb_get
// surface at all — templateFor must refuse to create (cold spawns still get
// kb_get via busMCPConfig's inherited servers, so cold is strictly better).
func TestTemplateForRequiresDetritus(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateStubClaude)
	t.Setenv("DETRITUS_BIN", "")
	t.Setenv("PATH", t.TempDir()) // LookPath("detritus") must fail too

	if id, ok := c.templateFor(RoleCoder, repo); ok || id != "" {
		t.Errorf("no detritus: got (%q, %v), want (\"\", false)", id, ok)
	}
	if _, err := c.server.Storage.Get(templateKey(RoleCoder, repo)); err == nil {
		t.Error("no entry may persist when the doctrine source is unavailable")
	}
	if got := spawnCount(t, fixture); got != 0 {
		t.Fatalf("creation must abort before spawning, got %d spawns", got)
	}
}

// templateSlowStub delays inside the creation spawn so concurrent callers pile
// up while the first is still creating — the singleflight window under test.
const templateSlowStub = `#!/usr/bin/env bash
if [[ "$1" == "--version" ]]; then echo "9.9.9 (stub)"; exit 0; fi
echo spawn >> "$CANDYLAND_TEMPLATE_FIXTURE"
sleep 0.3
sid=""
prev=""
for a in "$@"; do
  if [[ "$prev" == "--session-id" ]]; then sid="$a"; fi
  prev="$a"
done
dir="$CANDYLAND_CLAUDE_PROJECTS_DIR/$(pwd | sed 's/[^a-zA-Z0-9]/-/g')"
mkdir -p "$dir"
echo '{"doctrine":true}' > "$dir/$sid.jsonl"
echo "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$sid\"}"
echo '{"type":"result","subtype":"success","result":"READY"}'
`

// N concurrent requests for the same (role, repo) coalesce into EXACTLY ONE
// creation spawn, and every caller gets the same session id.
func TestTemplateForSingleflight(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateSlowStub)

	const n = 8
	ids := make([]string, n)
	oks := make([]bool, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			ids[i], oks[i] = c.templateFor(RoleCoder, repo)
		})
	}
	wg.Wait()

	for i := range n {
		if !oks[i] || ids[i] != ids[0] {
			t.Fatalf("caller %d got (%q, %v), want (%q, true)", i, ids[i], oks[i], ids[0])
		}
	}
	if got := spawnCount(t, fixture); got != 1 {
		t.Fatalf("singleflight: want exactly 1 creation spawn for %d callers, got %d", n, got)
	}
}

// The verified real-world example of claude's project-directory escaping: every
// non-alphanumeric character (including '/' and '.') maps to '-'.
const (
	verifiedCwd        = "/tmp/claude-0/-root-go-src-github-com-benitogf-detritus/0e76f127-f3a4-4b6e-961d-b2eb1d80acca/scratchpad"
	verifiedProjectDir = "-tmp-claude-0--root-go-src-github-com-benitogf-detritus-0e76f127-f3a4-4b6e-961d-b2eb1d80acca-scratchpad"
)

func TestProjectDirName(t *testing.T) {
	if got := projectDirName(verifiedCwd); got != verifiedProjectDir {
		t.Errorf("projectDirName(%q)\n got %q\nwant %q", verifiedCwd, got, verifiedProjectDir)
	}
	// '.' maps to '-' too (github.com → github-com).
	if got := projectDirName("/root/go/src/github.com/example/repo"); got != "-root-go-src-github-com-example-repo" {
		t.Errorf("dot escaping: got %q", got)
	}
	// Non-ASCII escapes per RUNE, one dash each — verified against real claude:
	// a cwd ending in "tëst-ünï" produced a project dir ending in "t-st--n-"
	// (byte-wise escaping would have produced "t--st---n--").
	if got := projectDirName("/x/tëst-ünï"); got != "-x-t-st--n-" {
		t.Errorf("rune escaping: got %q, want %q", got, "-x-t-st--n-")
	}
}

// canonPath makes symlinked/relative/trailing-slash spellings of the same repo
// derive the SAME template key and project dir claude will actually use.
func TestCanonPath(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	canonReal := canonPath(real) // TempDir itself may sit behind a symlink (e.g. /tmp on macOS)
	if got := canonPath(link); got != canonReal {
		t.Errorf("symlink: canonPath(%q) = %q, want %q", link, got, canonReal)
	}
	if got := canonPath(real + string(filepath.Separator)); got != canonReal {
		t.Errorf("trailing separator: got %q, want %q", got, canonReal)
	}
	if templateKey(RoleCoder, link) != templateKey(RoleCoder, real) {
		// templateKey hashes the raw path — callers canonicalize first; this
		// pins that templateFor's canonPath makes the two spellings collide.
		if templateKey(RoleCoder, canonPath(link)) != templateKey(RoleCoder, canonReal) {
			t.Error("canonicalized spellings must share one template key")
		}
	}
}

// copySessionForCwd round-trips a session transcript between two project dirs
// under an overridden projects root, creating the destination directory.
func TestCopySessionForCwd(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CANDYLAND_CLAUDE_PROJECTS_DIR", root)
	const sid = "0e76f127-f3a4-4b6e-961d-b2eb1d80acca"
	srcDir := filepath.Join(root, verifiedProjectDir)
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, sid+".jsonl"), []byte("{\"transcript\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	toCwd := "/work/trees/task-a"
	if err := copySessionForCwd(sid, verifiedCwd, toCwd); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "-work-trees-task-a", sid+".jsonl"))
	if err != nil {
		t.Fatalf("destination transcript missing: %v", err)
	}
	if string(got) != "{\"transcript\":true}\n" {
		t.Errorf("copied content = %q", got)
	}

	if err := copySessionForCwd(sid, "/nowhere/at/all", toCwd); err == nil {
		t.Error("a missing source transcript must error")
	}
}

// templateForWorkdir: same-dir needs no copy; a worktree gets the transcript
// copied into its project dir; a copy failure degrades to ("", false).
func TestTemplateForWorkdir(t *testing.T) {
	c, repo, _ := templateConductor(t, templateStubClaude)
	root := os.Getenv("CANDYLAND_CLAUDE_PROJECTS_DIR")

	id, ok := c.templateFor(RoleCoder, repo)
	if !ok {
		t.Fatal("creation must succeed")
	}

	// Same dir: the template is already resolvable there — no copy, no failure.
	if got, ok := c.templateForWorkdir(RoleCoder, repo, repo); !ok || got != id {
		t.Fatalf("same-dir = (%q, %v), want (%q, true)", got, ok, id)
	}

	// The worktree path copies the stub-written transcript into the worktree's
	// project dir.
	workdir := filepath.Join(repo, "wt", "a")
	if got, ok := c.templateForWorkdir(RoleCoder, repo, workdir); !ok || got != id {
		t.Fatalf("worktree = (%q, %v), want (%q, true)", got, ok, id)
	}
	if _, err := os.Stat(filepath.Join(root, projectDirName(workdir), id+".jsonl")); err != nil {
		t.Errorf("transcript not delivered to the worktree's project dir: %v", err)
	}

	// A destination that can't be created (a FILE squatting on the project-dir
	// path) fails the copy → cold start, never an error surfaced to the run.
	blocked := filepath.Join(repo, "wt", "b")
	if err := os.WriteFile(filepath.Join(root, projectDirName(blocked)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.templateForWorkdir(RoleCoder, repo, blocked); ok || got != "" {
		t.Fatalf("copy failure = (%q, %v), want (\"\", false)", got, ok)
	}
}

// invalidateTemplate drops the entry (an unresolved fork means the transcript
// is unusable in a way the stamps can't see); the next templateFor recreates.
// cleanupTemplateCopy removes ONLY the copied transcript from a worktree's
// project dir — the canonical template file stays.
func TestInvalidateAndCleanupCopy(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateStubClaude)

	id, ok := c.templateFor(RoleCoder, repo)
	if !ok {
		t.Fatal("creation must succeed")
	}
	workdir := filepath.Join(repo, "wt", "a")
	if _, ok := c.templateForWorkdir(RoleCoder, repo, workdir); !ok {
		t.Fatal("worktree copy must succeed")
	}
	root := os.Getenv("CANDYLAND_CLAUDE_PROJECTS_DIR")
	copied := filepath.Join(root, projectDirName(canonPath(workdir)), id+".jsonl")
	canonical := filepath.Join(root, projectDirName(canonPath(repo)), id+".jsonl")

	cleanupTemplateCopy(id, workdir)
	if _, err := os.Stat(copied); !os.IsNotExist(err) {
		t.Errorf("copied transcript must be removed, stat err=%v", err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Errorf("canonical template transcript must survive cleanup: %v", err)
	}

	c.invalidateTemplate(RoleCoder, repo)
	if _, err := c.server.Storage.Get(templateKey(RoleCoder, canonPath(repo))); err == nil {
		t.Error("invalidateTemplate must drop the registry entry")
	}
	second, ok := c.templateFor(RoleCoder, repo)
	if !ok || second == id {
		t.Errorf("after invalidation the template must be recreated: (%q, %v), first %q", second, ok, id)
	}
	if got := spawnCount(t, fixture); got != 2 {
		t.Errorf("want 2 creation spawns (initial + post-invalidation), got %d", got)
	}
}

// A stamped-valid entry whose transcript claude has since garbage-collected is
// a MISS, not a doomed fork forever: recreation rewrites the transcript and
// heals the registry.
func TestTemplateForHealsAfterTranscriptGC(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateStubClaude)

	first, ok := c.templateFor(RoleCoder, repo)
	if !ok {
		t.Fatal("creation must succeed")
	}
	root := os.Getenv("CANDYLAND_CLAUDE_PROJECTS_DIR")
	if err := os.Remove(filepath.Join(root, projectDirName(repo), first+".jsonl")); err != nil {
		t.Fatal(err)
	}

	second, ok := c.templateFor(RoleCoder, repo)
	if !ok {
		t.Fatal("recreation after transcript GC must succeed")
	}
	if second == first {
		t.Error("a GC'd template must be recreated, not returned stale")
	}
	if got := spawnCount(t, fixture); got != 2 {
		t.Errorf("want 2 creation spawns (initial + heal), got %d", got)
	}
	if _, err := os.Stat(filepath.Join(root, projectDirName(repo), second+".jsonl")); err != nil {
		t.Errorf("healed transcript missing: %v", err)
	}
}
