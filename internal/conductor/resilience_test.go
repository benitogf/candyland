package conductor

import (
	"context"
	"fmt"
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
	// Session-template reuse is OFF for stub tests by default: the flow stubs
	// don't speak the template-creation contract (--version probe, READY spawn),
	// so a spawn site calling templateFor would feed the stub's coder branch and
	// pollute the test cwd. Tests that exercise forking opt back in with
	// t.Setenv("CANDYLAND_SESSION_REUSE", "1") and a template-aware stub.
	t.Setenv("CANDYLAND_SESSION_REUSE", "0")
}

// writeFakeGh drops a stub `gh` that prints a PR URL, so the push → PR path is
// exercised without touching GitHub.
func writeFakeGh(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	script := "#!/usr/bin/env bash\n" +
		"if [[ \"$*\" == *defaultBranchRef* ]]; then echo 'main'; exit 0; fi\n" +
		"echo 'https://github.com/example/repo/pull/7'\n"
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_GH", gh)
}

// newGitRepo creates a throwaway git repo (with an initial commit and a local
// bare `origin` to push to), so the executor's real git/worktree/push work runs
// against a real repository.
func newGitRepo(t *testing.T) string { return newGitRepoNamed(t, "repo") }

// newGitRepoNamed creates a throwaway git repo with a controlled basename (the
// name a multi-repo task's `repo` field matches) and its own bare origin.
func newGitRepoNamed(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, name)
	bare := filepath.Join(root, name+"-origin.git")
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
	// Session-template forking is opt-in per test: a stub oracle dispatches on the
	// spawn prompt, and an unsolicited template-creation spawn would hit arbitrary
	// branches (some sleep, some count invocations). The fork tests re-enable this
	// (t.Setenv after the harness call wins) and seed a transcript so forks resolve.
	t.Setenv("CANDYLAND_SESSION_REUSE", "0")
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

// multiRepoConductor wires N real throwaway repos (with the given basenames, the
// names a task's `repo` field matches), a stub claude, and a real bus, returning
// a conductor whose runs span all of them. The caller installs its own stub gh
// (CANDYLAND_GH) before creating the run, so it can make one repo's PR fail.
func multiRepoConductor(t *testing.T, claudeScript string, repoNames ...string) (*Conductor, []string) {
	t.Helper()
	repos := make([]string, len(repoNames))
	for i, n := range repoNames {
		repos[i] = newGitRepoNamed(t, n)
	}
	writeFakeClaude(t, claudeScript)
	t.Setenv("CANDYLAND_SESSION_REUSE", "0") // same opt-in rationale as deliveryConductor
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := New(srv)
	c.StartBus()
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatalf("start bus: %v", err)
	}
	t.Cleanup(func() { srv.Close(os.Interrupt) })
	c.folders = func(run.Run) ([]string, error) { return repos, nil }
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
	return c, repos
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

// A usage-limit death must be classified as a limit (so it pauses+resumes rather
// than burning a retry) across the ways claude phrases it, and must NOT fire on an
// ordinary crash. The reset time comes from the message; an unparseable one still
// classifies as a limit and defaults to a backoff window.
func TestClassifyUsageLimitVariants(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	epoch := time.Date(2026, 7, 7, 15, 0, 0, 0, time.UTC).Unix()
	cases := []struct {
		name  string
		out   attemptOutcome
		want  bool
		reset time.Time
	}{
		{"stderr epoch", attemptOutcome{runErr: errStub, stderr: fmt.Sprintf("Claude AI usage limit reached|%d", epoch)}, true, time.Unix(epoch, 0).In(time.UTC)},
		{"text clock pm", attemptOutcome{runErr: errStub, lastText: "Claude usage limit reached. Your limit will reset at 3pm."}, true, time.Date(2026, 7, 7, 15, 0, 0, 0, time.UTC)},
		{"rate limited", attemptOutcome{runErr: errStub, stderr: "rate limited, try again later"}, true, now.Add(defaultLimitBackoff)},
		{"429", attemptOutcome{runErr: errStub, stderr: "server returned 429 Too Many Requests"}, true, now.Add(defaultLimitBackoff)},
		{"stalled limit", attemptOutcome{stalled: true, lastText: "usage limit reached"}, true, now.Add(defaultLimitBackoff)},
		{"ordinary crash", attemptOutcome{stderr: "panic: nil pointer", runErr: errStub}, false, time.Time{}},
		{"clean success", attemptOutcome{sawTool: true}, false, time.Time{}},
		{"success mentions limit in transcript", attemptOutcome{sawTool: true, lastText: "I reviewed the usage limit reached handling and 429 path", allText: "usage limit reached rate limit 429"}, false, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset, ok := classifyUsageLimit(tc.out, now)
			if ok != tc.want {
				t.Fatalf("classifyUsageLimit ok=%v want %v", ok, tc.want)
			}
			if ok && !reset.Equal(tc.reset) {
				t.Errorf("reset=%v want %v", reset, tc.reset)
			}
		})
	}
}

var errStub = fmt.Errorf("stub")

// parseResetTime reads the reset moment out of the many shapes a limit message
// takes, relative to now, and rejects an unparseable one.
func TestParseResetTimeTable(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"limit reached|1751900400", time.Unix(1751900400, 0).In(time.UTC), true},
		{"your limit will reset at 3pm", time.Date(2026, 7, 7, 15, 0, 0, 0, time.UTC), true},
		{"resets at 15:30 today", time.Date(2026, 7, 7, 15, 30, 0, 0, time.UTC), true},
		{"resets 9am", time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC), true}, // 9am already passed → tomorrow
		{"limit will reset at 12am", time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC), true},
		{"no time here", time.Time{}, false},
		{"resets at 99:99", time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseResetTime(tc.in, now)
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v", ok, tc.ok)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

// A limit-interrupted resume spawn carries `--resume <session-id>` WITHOUT
// --fork-session (a fork, not a resume) and never a --session-id (only valid on a
// fresh session) — so the interrupted session is continued in place.
func TestResumeArgsCarrySessionID(t *testing.T) {
	args := claudeArgs("go on", nil, "", spawnOpts{resumeFrom: "sess-123"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--resume sess-123") {
		t.Errorf("resume spawn must carry --resume <session-id>, got: %v", args)
	}
	if strings.Contains(joined, "--fork-session") {
		t.Errorf("a resume is not a fork — must not carry --fork-session, got: %v", args)
	}
	if strings.Contains(joined, "--session-id") {
		t.Errorf("a resume must not mint a new --session-id, got: %v", args)
	}
	// A fresh spawn names its session so a later limit pause can resume it.
	fresh := claudeArgs("start", nil, "", spawnOpts{sessionID: "sess-new"})
	if !strings.Contains(strings.Join(fresh, " "), "--session-id sess-new") {
		t.Errorf("a fresh spawn must carry --session-id, got: %v", fresh)
	}
}

// The conductor-wide limit gate blocks every spawn's awaitLimit until the armed
// window passes — a limit hit by one agent pauses them all (one account, one
// limit). An unarmed gate never blocks.
func TestLimitGateBlocksAllSpawns(t *testing.T) {
	c := New(nil)
	// Unarmed: awaitLimit returns immediately.
	done := make(chan struct{})
	go func() { c.awaitLimit(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("awaitLimit blocked with no limit armed")
	}

	// Arm a short window; N concurrent waiters must all block until it passes.
	c.reArmLimit(time.Now().Add(150 * time.Millisecond))
	const n = 5
	released := make(chan time.Time, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		go func() { c.awaitLimit(context.Background()); released <- time.Now() }()
	}
	for i := 0; i < n; i++ {
		select {
		case at := <-released:
			if waited := at.Sub(start); waited < 100*time.Millisecond {
				t.Errorf("waiter released after %v — the gate did not block it", waited)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a waiter never released after the limit window passed")
		}
	}

	// A cancelled context unblocks a waiter even while the gate is still closed.
	c.reArmLimit(time.Now().Add(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	unblocked := make(chan struct{})
	go func() { c.awaitLimit(ctx); close(unblocked) }()
	cancel()
	select {
	case <-unblocked:
	case <-time.After(time.Second):
		t.Fatal("awaitLimit ignored context cancellation")
	}
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
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
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

	id := c.Create(run.Spec{Prompt: "add a CSV export"})
	c.Begin(id)

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

// A coder that works around a non-terminal problem self-reports it as an
// `INCIDENT <json>` line (per incidentDoctrine, appended to every bootstrap) and
// keeps working green — the incident must NOT block the run. It runs the real
// delivery flow to completion and lands on the run's Incidents audit trail.
const incidentReporter = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"role\":\"Backend\",\"emoji\":\"X\",\"files\":[\"a.txt\"],\"test\":\"a_test\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"partition emitted","usage":{"output_tokens":10}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"INCIDENT {\"summary\":\"worked around a stale lockfile\",\"detail\":\"deleted go.sum and refetched\",\"severity\":\"warn\"}"}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"TEST {\"pass\":1,\"fail\":0}"}]}}'
  echo "work by $$" > "candyland_$$.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":20}}'
fi
`

func TestSelfReportedIncidentLandsOnRunWithoutBlocking(t *testing.T) {
	c, _ := deliveryConductor(t, incidentReporter)
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "2")

	id := c.Create(run.Spec{Prompt: "add a CSV export"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 30*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	// A self-reported incident is NON-terminal — it must never fail the run.
	if r.Error != "" {
		t.Fatalf("a self-reported incident must not block the run: %q", r.Error)
	}
	if r.TasksGreen != 1 {
		t.Errorf("tasksGreen=%d want 1 — the coder still finished green", r.TasksGreen)
	}
	if r.PrURL == "" {
		t.Error("a clean run that self-reported an incident must still open a PR")
	}
	// The incident lands on the run's audit trail, stamped with the reporting agent.
	if len(r.Incidents) != 1 {
		t.Fatalf("want 1 incident on the run, got %+v", r.Incidents)
	}
	n := r.Incidents[0]
	if n.Summary != "worked around a stale lockfile" {
		t.Errorf("incident summary wrong: %q", n.Summary)
	}
	if n.Agent != "a" {
		t.Errorf("incident should be stamped with the reporting agent %q, got %q", "a", n.Agent)
	}
	if n.Severity != "warn" {
		t.Errorf("incident severity wrong: %q", n.Severity)
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

	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

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

	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 15*time.Second)
	if r.Error == "" {
		t.Fatal("a non-zero claude exit must record an error")
	}
	if !strings.Contains(r.Error, "boom") {
		t.Errorf("the run error should surface claude's stderr, got %q", r.Error)
	}
}

func TestStopHaltsWithoutFalseGreen(t *testing.T) {
	c, _ := deliveryConductor(t, slowCoder)
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000") // don't let the stall watchdog fire during the test
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "2")

	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

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
	// The coder is stamped terminal "stopped" once the run unwinds — never left
	// "working", which would render as a live "Working" agent card beside the
	// run's stopped status (the stopped+Working dashboard contradiction).
	r = waitFor(t, c, id, func(r run.Run) bool {
		for _, a := range r.Agents {
			if a.ID == "a" {
				return a.State == "stopped"
			}
		}
		return false
	}, 15*time.Second)
	for _, a := range r.Agents {
		if a.ID == "a" && a.State != "stopped" {
			t.Errorf("coder killed by stop should be stamped %q, got %q", "stopped", a.State)
		}
	}
	if r.TasksGreen != 0 {
		t.Errorf("tasksGreen=%d want 0 after stopping an in-flight run", r.TasksGreen)
	}
	if r.PrURL != "" {
		t.Errorf("a stopped run must not have opened a PR, got %q", r.PrURL)
	}
}

// A usage-limit pause must not consume the resume spawn's attempt budget. The
// first spawn dies with a limit whose window (~1s) outlasts the per-attempt wall
// clock (300ms); streamOnce arms the gate, waits it out, then resumes. The resume
// must run under a FRESH attempt deadline — a deadline created before the pause
// would already be elapsed, so spawnStream's procCtx would be Done on arrival and
// killTree would kill the resume on spawn. Guards the shared-attemptCtx regression.
func TestResumeAfterLimitPauseGetsFreshDeadline(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "limit-hit")
	stub := "#!/usr/bin/env bash\n" +
		"prompt=\"$2\"\n" +
		"if [[ ! -f \"" + marker + "\" ]]; then\n" +
		"  touch \"" + marker + "\"\n" +
		"  echo \"Claude AI usage limit reached|$(( $(date +%s) + 1 ))\" >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Write\",\"input\":{\"file\":\"x\"}}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"resumed green\",\"usage\":{\"output_tokens\":3}}'\n"
	writeFakeClaude(t, stub)
	// The per-attempt wall clock is far shorter than the ~1s limit window, so a
	// deadline shared across the pause would be dead by the time the resume runs.
	t.Setenv("CANDYLAND_AGENT_TIMEOUT_MS", "300")
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000")

	c := New(nil)
	id := c.Create(run.Spec{Prompt: "do the thing"})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan attemptOutcome, 1)
	go func() {
		done <- streamOnce(ctx, c, id, "a", "go on", t.TempDir(), nil)
	}()

	var out attemptOutcome
	select {
	case out = <-done:
	case <-ctx.Done():
		t.Fatal("streamOnce never returned — the resume spawn was likely killed by a stale attempt deadline")
	}

	if out.startErr != nil || out.runErr != nil {
		t.Fatalf("resume spawn failed instead of completing green: startErr=%v runErr=%v stderr=%q", out.startErr, out.runErr, out.stderr)
	}
	if out.stalled {
		t.Fatal("resume spawn was killed (stalled) — its attempt deadline elapsed during the limit pause")
	}
	if !out.sawTool {
		t.Errorf("resume spawn did no work (sawTool=false) — expected the resumed session to run green")
	}

	// The pause+resume must be visible in the run: a paused status/resumeAt was
	// armed and then cleared, and the agent stream records the pause.
	r, _ := c.Get(id)
	paused := false
	for _, a := range r.Agents {
		if a.ID != "a" {
			continue
		}
		for _, e := range a.Events {
			if strings.Contains(e.Text, "usage limit reached (pause") {
				paused = true
			}
		}
	}
	if !paused {
		t.Error("expected a usage-limit pause event in the agent's stream (resume-after-pause path not exercised)")
	}
	if r.ResumeAt != "" {
		t.Errorf("resumeAt should be cleared once the run resumed, got %q", r.ResumeAt)
	}
}
