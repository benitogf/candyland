package conductor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// writeFakeClaude drops an executable fake `claude` and points the executor at it.
func writeFakeClaude(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_CLAUDE", fake)
	t.Setenv("CANDYLAND_EXECUTOR", "claude")
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

// A coder that defers/asks a question on the first try (base prompt) and only
// does real work once the prompt has been hardened with the autonomy reminder.
// This exercises the non-compliance → retry-with-firmer-prompt → success path.
const flakyThenCompliant = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"role\":\"Backend\",\"emoji\":\"⚙️\",\"files\":[\"x.go\"],\"test\":\"x_test\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"partition emitted","usage":{"output_tokens":10}}'
elif [[ "$prompt" == *"AUTONOMY REQUIRED"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file":"x.go"}}]}}'
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":20}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Could you clarify which columns the export should include?"}]}}'
  echo '{"type":"result","subtype":"success","result":"I will defer the rest to a later step.","usage":{"output_tokens":5}}'
fi
`

func TestRetryRecoversNonCompliantAgent(t *testing.T) {
	writeFakeClaude(t, flakyThenCompliant)
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "3")

	c := New(nil)
	id := c.Create(run.Spec{Mode: "developer", Workspace: "web", Prompt: "add a CSV export"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 10*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q", r.Status)
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
	writeFakeClaude(t, hangingTechLead)
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "200")
	t.Setenv("CANDYLAND_AGENT_TIMEOUT_MS", "4000")
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "2")

	c := New(nil)
	id := c.Create(run.Spec{Mode: "developer", Workspace: "web", Prompt: "do the thing"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 8*time.Second)
	if r.Status != "done" {
		t.Fatalf("stalled run never terminated: status=%q", r.Status)
	}
	if r.Error == "" {
		t.Fatal("stalled run reported no error — it should fail honestly")
	}
	if !strings.Contains(strings.ToLower(r.Error), "stall") {
		t.Errorf("error should mention the stall, got %q", r.Error)
	}
	// An errored run must NOT claim the final PR phase or 100% progress.
	if r.Phase == len(run.Phases)-1 || r.Progress >= 1 {
		t.Errorf("errored run falsely claimed completion: phase=%d progress=%v", r.Phase, r.Progress)
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
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"x.go\"],\"test\":\"x_test\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"starting work"}]}}'
  sleep 30
fi
`

func TestStopHaltsWithoutFalseGreen(t *testing.T) {
	writeFakeClaude(t, slowCoder)
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000") // don't let the stall watchdog fire during the test
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "2")

	c := New(nil)
	id := c.Create(run.Spec{Mode: "developer", Workspace: "web", Prompt: "do the thing"})
	c.Begin(id, nil)

	// Wait until the coder is spawned and in flight, then stop the run.
	r := waitFor(t, c, id, func(r run.Run) bool {
		for _, a := range r.Agents {
			if a.ID == "a" && a.State == "working" {
				return true
			}
		}
		return false
	}, 5*time.Second)
	if !c.Command(id, "stop") {
		t.Fatal("stop command was dropped")
	}

	r = waitFor(t, c, id, func(r run.Run) bool { return r.Status == "paused" }, 5*time.Second)
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
}
