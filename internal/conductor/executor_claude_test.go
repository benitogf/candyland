package conductor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// ensureAgent must seed a fresh agent with a non-nil Events slice so a
// newly-recorded agent serializes as an empty array, never `"events":null`.
func TestEnsureAgentInitializesEvents(t *testing.T) {
	var agents []run.Agent

	a := ensureAgent(&agents, "coder-1")
	if a.Events == nil {
		t.Fatal("ensureAgent: seeded agent has nil Events, want non-nil empty slice")
	}
	if len(a.Events) != 0 {
		t.Fatalf("ensureAgent: seeded agent Events len = %d, want 0", len(a.Events))
	}

	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal agent: %v", err)
	}
	if strings.Contains(string(b), `"events":null`) {
		t.Fatalf("agent JSON contains events:null: %s", b)
	}
}

// A returning ensureAgent lookup must yield the same agent, preserving events.
func TestEnsureAgentReturnsExisting(t *testing.T) {
	var agents []run.Agent
	ensureAgent(&agents, "coder-1").Events = append(ensureAgent(&agents, "coder-1").Events, run.Event{T: "system", Text: "hi"})

	a := ensureAgent(&agents, "coder-1")
	if len(a.Events) != 1 {
		t.Fatalf("ensureAgent: existing agent Events len = %d, want 1", len(a.Events))
	}
	if len(agents) != 1 {
		t.Fatalf("ensureAgent: agents len = %d, want 1", len(agents))
	}
}
