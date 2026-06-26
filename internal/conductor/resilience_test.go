package conductor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// writeFakeClaude drops an executable fake `claude` and points the executor at
// it, so the whole real flow (partition → coders → integrate → push → PR) runs
// deterministically with no Anthropic API. A coder stub WRITES a file in its cwd
// (the worktree) so there's a genuine edit to commit and merge.
func writeFakeClaude(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_CLAUDE", fake)
}

// writeFakeGh drops a stub `gh` that prints a PR URL, so the push → PR path is
// exercised without touching GitHub.
func writeFakeGh(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	script := "#!/usr/bin/env bash\necho 'https://github.com/example/repo/pull/7'\n"
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_GH", gh)
}

// newGitRepo creates a throwaway git repo (with an initial commit and a local
// bare `origin` to push to), so the executor's real git/worktree/push work runs
// against a real repository.
func newGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	bare := filepath.Join(root, "origin.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(repo, "init", "-q", "-b", "main")
	run(repo, "config", "user.email", "test@candyland.local")
	run(repo, "config", "user.name", "candyland test")
	run(repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-q", "-m", "init")
	run(repo, "init", "--bare", "-q", bare)
	run(repo, "remote", "add", "origin", bare)
	return repo
}

// deliveryConductor wires the stub claude + stub gh + a real throwaway repo and
// returns a conductor whose runs target that repo.
func deliveryConductor(t *testing.T, claudeScript string) (*Conductor, string) {
	t.Helper()
	repo := newGitRepo(t)
	writeFakeClaude(t, claudeScript)
	writeFakeGh(t)
	// A real ooo server + bus so the conductor writes each agent's brief and the
	// stub claude can fetch it over HTTP (the brief carries the plan/task that no
	// longer rides on argv). StartBus registers the bus filters before Start.
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := New(srv)
	c.StartBus()
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatalf("start bus: %v", err)
	}
	t.Cleanup(func() { srv.Close(os.Interrupt) })
	c.folders = func(run.Run) ([]string, error) { return []string{repo}, nil }
	// Drain before the test's t.TempDir() is removed: cancel any still-tracked
	// runs and wait for each executor's deferred worktree cleanup (git worktree
	// remove / branch -D / prune on the repo, then rm the worktree dir) to
	// finish. Registered AFTER newGitRepo's t.TempDir, so it runs BEFORE that
	// RemoveAll (LIFO) — otherwise a late git subprocess races the harness
	// removing repo/.git ("directory not empty"). Test-only teardown.
	t.Cleanup(func() {
		c.mu.Lock()
		ids := make([]string, 0, len(c.runs))
		for id := range c.runs {
			ids = append(ids, id)
		}
		c.mu.Unlock()
		for _, id := range ids {
			c.Cancel(id)
		}
		wtParent := filepath.Join(os.TempDir(), "candyland-wt")
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			pending := false
			for _, id := range ids {
				if _, err := os.Stat(filepath.Join(wtParent, id)); err == nil {
					pending = true
					break
				}
			}
			if !pending {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	return c, repo
}

func waitFor(t *testing.T, c *Conductor, id string, until func(run.Run) bool, d time.Duration) run.Run {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		r, _ := c.Get(id)
		if until(r) {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	r, _ := c.Get(id)
	return r
}

// A single atomic task is a VALID partition — not a failure. The only tech-lead
// partition failure is emitting nothing parseable (so a small/atomic task, or one
// fullstack task spanning both domains, completes instead of being rejected).
func TestCompliantAtomicSingleTaskIsValid(t *testing.T) {
	ok, why := compliant(attemptOutcome{partition: []partitionTask{{ID: "a", Title: "the whole thing", Role: "fullstack"}}}, true)
	if !ok {
		t.Errorf("a one-task partition must be compliant (atomic is valid), got: %s", why)
	}
	if ok, why := compliant(attemptOutcome{partition: nil}, true); ok || why == "" {
		t.Errorf("an empty partition must be the (only) tech-lead failure, got ok=%v why=%q", ok, why)
	}
}

// The spawn prompts are constant bootstraps that encode the role contract (atomic
// + fullstack) and fetch context via brief_get — they must never carry a plan body.
func TestBootstrapsCarryRoleContractNotContext(t *testing.T) {
	if !strings.Contains(techLeadBootstrap, "atomic") || !strings.Contains(techLeadBootstrap, "fullstack") {
		t.Error("tech-lead bootstrap must bless atomic + fullstack partitions")
	}
	if !strings.Contains(coderBootstrap, "fullstack") || !strings.Contains(coderBootstrap, "brief_get") {
		t.Error("coder bootstrap must be role-aware (fullstack) and fetch its brief")
	}
	for name, p := range map[string]string{"techLead": techLeadBootstrap, "coder": coderBootstrap, "conflict": conflictBootstrap} {
		if !strings.Contains(p, "brief_get") {
			t.Errorf("%s bootstrap must instruct the agent to call brief_get", name)
		}
		if len(p) > 2000 {
			t.Errorf("%s bootstrap is %d chars — must be a small constant, not carry context", name, len(p))
		}
	}
}

// A coder that defers/asks a question on the first try (base prompt) and only
// does real work once the prompt has been hardened with the autonomy reminder.
// Exercises the non-compliance → retry-with-firmer-prompt → success path.
const flakyThenCompliant = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"role\":\"Backend\",\"emoji\":\"X\",\"files\":[\"a.txt\"],\"test\":\"a_test\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"partition emitted","usage":{"output_tokens":10}}'
elif [[ "$prompt" == *"AUTONOMY REQUIRED"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "work by $$" > "candyland_$$.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":20}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Could you clarify which columns the export should include?"}]}}'
  echo '{"type":"result","subtype":"success","result":"I will defer the rest to a later step.","usage":{"output_tokens":5}}'
fi
`

func TestRetryRecoversNonCompliantAgent(t *testing.T) {
	c, _ := deliveryConductor(t, flakyThenCompliant)
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "3")

	id := c.Create(run.Spec{Mode: "developer", Prompt: "add a CSV export"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 30*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error != "" {
		t.Fatalf("run errored despite eventual compliance: %q", r.Error)
	}
	var coder *run.Agent
	for i := range r.Agents {
		if r.Agents[i].ID == "a" {
			coder = &r.Agents[i]
		}
	}
	if coder == nil {
		t.Fatal("coder agent 'a' missing")
	}
	if coder.State != "green" {
		t.Errorf("coder not green after retry: state=%q activity=%q", coder.State, coder.Activity)
	}
	if r.TasksGreen != 1 {
		t.Errorf("tasksGreen=%d want 1", r.TasksGreen)
	}
	// The retry must be visible in the agent's stream (a system event records it).
	retried := false
	for _, e := range coder.Events {
		if strings.Contains(e.Text, "retry") {
			retried = true
		}
	}
	if !retried {
		t.Error("expected a retry event in the coder's stream (recovery path not exercised)")
	}
	// A clean run delivers a real PR.
	if r.PrURL == "" {
		t.Error("a completed run must set PrURL")
	}
}

// A tech lead that hangs with no output — exercises the stall watchdog. After
// the attempts are exhausted the run must fail honestly: an actionable error,
// the agent blocked, and NO claim of a finished PR.
const hangingTechLead = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"tech lead"* ]]; then
  sleep 30
else
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":1}}'
fi
`

func TestStallFailsHonestly(t *testing.T) {
	c, _ := deliveryConductor(t, hangingTechLead)
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "200")
	t.Setenv("CANDYLAND_AGENT_TIMEOUT_MS", "4000")
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "2")

	id := c.Create(run.Spec{Mode: "developer", Prompt: "do the thing"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 12*time.Second)
	if r.Status != "done" {
		t.Fatalf("stalled run never terminated: status=%q", r.Status)
	}
	if r.Error == "" {
		t.Fatal("stalled run reported no error — it should fail honestly")
	}
	if !strings.Contains(strings.ToLower(r.Error), "stall") {
		t.Errorf("error should mention the stall, got %q", r.Error)
	}
	// An errored run must NOT claim the final PR phase, 100% progress, or a PR.
	if r.Phase == len(run.Phases)-1 || r.Progress >= 1 || r.PrURL != "" {
		t.Errorf("errored run falsely claimed completion: phase=%d progress=%v prUrl=%q", r.Phase, r.Progress, r.PrURL)
	}
	var tl *run.Agent
	for i := range r.Agents {
		if r.Agents[i].ID == "tl" {
			tl = &r.Agents[i]
		}
	}
	if tl == nil || tl.State != "blocked" {
		t.Errorf("tech lead should be blocked after stalling, got %+v", tl)
	}
}

// A coder that announces itself then hangs, so the run is genuinely in-flight
// when we stop it — exercising the control path against the claude executor.
const slowCoder = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"a_test\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"starting work"}]}}'
  sleep 30
fi
`

// Running as root, claude refuses --dangerously-skip-permissions unless it
// believes it's sandboxed — so claudeEnv must set IS_SANDBOX=1 there, or every
// run dies at the tech lead. (The common WSL/server case runs as root.)
func TestClaudeEnvSignalsSandboxAsRoot(t *testing.T) {
	has := false
	for _, e := range claudeEnv() {
		if e == "IS_SANDBOX=1" {
			has = true
		}
	}
	if root := os.Geteuid() == 0; root != has {
		t.Errorf("IS_SANDBOX present=%v but euid==0 is %v — root must signal sandbox, non-root must not", has, root)
	}
}

// A claude process that exits non-zero must surface WHY (its stderr) in the run
// error — not a blank "exited with an error". This is the difference between the
// reported failure being diagnosable or opaque.
const exitWithStderr = `#!/usr/bin/env bash
echo "boom: --dangerously-skip-permissions cannot be used with root" >&2
exit 1
`

func TestProcessExitSurfacesStderr(t *testing.T) {
	c, _ := deliveryConductor(t, exitWithStderr)
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "1")

	id := c.Create(run.Spec{Mode: "developer", Prompt: "do the thing"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 15*time.Second)
	if r.Error == "" {
		t.Fatal("a non-zero claude exit must record an error")
	}
	if !strings.Contains(r.Error, "boom") {
		t.Errorf("the run error should surface claude's stderr, got %q", r.Error)
	}
}

// A tech lead that exits non-zero the FIRST time (no marker) and succeeds the
// SECOND (marker present) — so a restart of the failed run recovers and delivers.
const failFirstThenSucceed = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"tech lead"* ]]; then
  if [[ -f "$CANDYLAND_TEST_MARKER" ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"a_test\"}]"}]}}'
    echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
  else
    touch "$CANDYLAND_TEST_MARKER"
    echo "tech lead boom (first attempt)" >&2
    exit 1
  fi
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "work by $$" > "candyland_$$.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

func TestRestartRecoversFailedRun(t *testing.T) {
	c, _ := deliveryConductor(t, failFirstThenSucceed)
	t.Setenv("CANDYLAND_TEST_MARKER", filepath.Join(t.TempDir(), "marker"))
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "1") // fail fast on the first run

	id := c.Create(run.Spec{Mode: "developer", Prompt: "do the thing"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 15*time.Second)
	if r.Error == "" {
		t.Fatal("the first run should fail (tech lead exits non-zero)")
	}

	// Restart the FAILED run — its executor has already exited, so this must
	// relaunch a fresh one (not just signal a dead control channel).
	if !c.Restart(id) {
		t.Fatal("Restart should succeed for a finished/failed run")
	}
	r = waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" && r.Error == "" }, 20*time.Second)
	if r.Error != "" {
		t.Fatalf("restart did not recover the run: %q", r.Error)
	}
	if r.PrURL == "" {
		t.Error("the recovered run should open a PR")
	}
}

func TestStopHaltsWithoutFalseGreen(t *testing.T) {
	c, _ := deliveryConductor(t, slowCoder)
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000") // don't let the stall watchdog fire during the test
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "2")

	id := c.Create(run.Spec{Mode: "developer", Prompt: "do the thing"})
	c.Begin(id, nil)

	// Wait until the coder is spawned and in flight, then stop the run.
	waitFor(t, c, id, func(r run.Run) bool {
		for _, a := range r.Agents {
			if a.ID == "a" && a.State == "working" {
				return true
			}
		}
		return false
	}, 20*time.Second)
	if !c.Command(id, "stop") {
		t.Fatal("stop command was dropped")
	}

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "paused" }, 15*time.Second)
	if r.Status != "paused" {
		t.Fatalf("run did not pause on stop: status=%q", r.Status)
	}
	if r.Error != "" {
		t.Errorf("stop is not a failure — r.Error should be empty, got %q", r.Error)
	}
	for _, a := range r.Agents {
		if a.ID == "a" && a.State == "green" {
			t.Error("a coder killed mid-flight by stop was falsely marked green")
		}
	}
	if r.TasksGreen != 0 {
		t.Errorf("tasksGreen=%d want 0 after stopping an in-flight run", r.TasksGreen)
	}
	if r.PrURL != "" {
		t.Errorf("a stopped run must not have opened a PR, got %q", r.PrURL)
	}
}
