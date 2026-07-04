package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// runBranch is the single definition of a run's branch format, shared by Create
// and Edit. Pin it so a change to the prefix/slug/id-suffix rule is a deliberate,
// test-visible edit rather than a silent divergence between the two call sites.
func TestRunBranch(t *testing.T) {
	cases := []struct {
		spec run.Spec
		id   string
		want string
	}{
		{run.Spec{Title: "CSV Export", Prompt: "ignored"}, "abc", "feat/csv-export-abc"}, // title wins, slugged
		{run.Spec{Prompt: "Add export"}, "id1", "feat/add-export-id1"},                   // falls back to prompt
		{run.Spec{}, "id1", "feat/run-id1"},                                              // falls back to "run"
	}
	for _, c := range cases {
		if got := runBranch(c.spec, c.id); got != c.want {
			t.Errorf("runBranch(%+v, %q) = %q, want %q", c.spec, c.id, got, c.want)
		}
	}
}

// stopInFlightAgents stamps in-flight agents terminal on a stop but must preserve
// a genuine outcome — a green/done/blocked agent keeps its real state, so a stop
// never rewrites finished work as "stopped".
func TestStopInFlightAgents(t *testing.T) {
	agents := []run.Agent{
		{ID: "idle", State: "idle"},
		{ID: "working", State: "working"},
		{ID: "retrying", State: "retrying"},
		{ID: "integrating", State: "integrating"},
		{ID: "green", State: "green"},
		{ID: "done", State: "done"},
		{ID: "blocked", State: "blocked"},
	}
	stopInFlightAgents(agents)
	want := map[string]string{
		"idle": "stopped", "working": "stopped", "retrying": "stopped", "integrating": "stopped",
		"green": "green", "done": "done", "blocked": "blocked",
	}
	for _, a := range agents {
		if a.State != want[a.ID] {
			t.Errorf("agent %q: got state %q, want %q", a.ID, a.State, want[a.ID])
		}
	}
}
