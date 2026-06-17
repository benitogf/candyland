package conductor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// A fake `claude` binary: emits a PARTITION line for the tech-lead prompt, and
// a small transcript for each coder. Lets us verify the real fan-out logic
// (partition parse → spawn one process per task → aggregate per-agent state)
// deterministically, with no Anthropic API.
const fakeClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"system","subtype":"init","session_id":"s1"}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"role\":\"Backend\",\"emoji\":\"⚙️\",\"files\":[\"x.go\"],\"test\":\"x_test\"},{\"id\":\"b\",\"title\":\"task b\",\"role\":\"Frontend\",\"emoji\":\"🎨\",\"files\":[\"y.js\"],\"test\":\"y_test\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"partition emitted","usage":{"output_tokens":1000}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"implementing"}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file":"x"}}]}}'
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2000}}'
fi
`

func TestClaudeFanOut(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_CLAUDE", fake)
	t.Setenv("CANDYLAND_EXECUTOR", "claude")

	c := New(nil) // serverless: state is read via Get, not ooo
	id := c.Create(run.Spec{Mode: "developer", Workspace: "web", Prompt: "add a CSV export"})
	c.Begin(id, nil)

	deadline := time.Now().Add(10 * time.Second)
	var r run.Run
	for time.Now().Before(deadline) {
		r, _ = c.Get(id)
		if r.Status == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q", r.Status)
	}
	if len(r.Tasks) != 2 {
		t.Fatalf("partition not parsed: got %d tasks, want 2", len(r.Tasks))
	}
	// agents = tech-lead + one coder per task.
	if len(r.Agents) != 3 {
		t.Fatalf("fan-out wrong: got %d agents, want 3 (tl + 2 coders)", len(r.Agents))
	}
	byID := map[string]run.Agent{}
	for _, a := range r.Agents {
		byID[a.ID] = a
	}
	for _, id := range []string{"tl", "a", "b"} {
		a, ok := byID[id]
		if !ok {
			t.Fatalf("missing agent %q", id)
		}
		if len(a.Events) == 0 {
			t.Errorf("agent %q has no events (process output not aggregated)", id)
		}
	}
	if byID["a"].State != "green" || byID["b"].State != "green" {
		t.Errorf("coders not marked green: a=%s b=%s", byID["a"].State, byID["b"].State)
	}
	if r.TasksGreen != 2 {
		t.Errorf("tasksGreen=%d want 2", r.TasksGreen)
	}
}
