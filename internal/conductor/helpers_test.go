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
