package conductor

import (
	"context"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// O4 — a quest's OWN coordinating agent (the quest-lead) is spawned under the quest
// id, and the agent-recording path (streamOnce → mapAgentLine → updateAgentHost)
// must land its state+events on the PARENT record so the dashboard can show what the
// quest itself is doing — not drop them onto a non-existent run runtime.
//
// supervisorAgentClaude is a minimal stub: any spawn emits one tool_use and one
// text block then a terminal result. It carries no verdict line — this test asserts
// the RECORDING side (the parent's Agents slice), not a parse/transition.
var supervisorAgentClaude = stubClaude(
	coder(
		writeWorktreeFile("noop.txt"),
		emitText("coordinating the program"),
		emitResult("done", 3),
	),
)

// TestSupervisorAgentsRecordOnParent drives streamOnce directly against a quest id
// (the exact id the real quest-lead runs under) and asserts the parent record
// carries the coordinating agent with its events — the hasAgents-equivalent the
// dashboard reads. Without O4's routing these landed on c.runs[<quest id>] (nil)
// and were silently dropped.
func TestSupervisorAgentsRecordOnParent(t *testing.T) {
	c, repo := deliveryConductor(t, supervisorAgentClaude)

	questID := c.CreateQuest(run.QuestSpec{Objective: "tidy the thing", Folders: []string{repo}})

	// Freshly created, the parent has spawned no coordinating agent yet.
	if q, ok := c.GetQuest(questID); !ok || len(q.Agents) != 0 {
		t.Fatalf("new quest should start with no agents, got %d", len(q.Agents))
	}

	// Run the coordinating agent through the REAL spawn path.
	streamOnce(context.Background(), c, questID, questLeadID, "supervisor", repo, nil)

	// The quest-lead is recorded on the quest record.
	q, ok := c.GetQuest(questID)
	if !ok {
		t.Fatal("quest lost")
	}
	qa := findAgent(q.Agents, questLeadID)
	if qa == nil {
		t.Fatalf("quest %s has no %s agent — quest-lead state was dropped (hasAgents:false)", questID, questLeadID)
	}
	if len(qa.Events) == 0 {
		t.Fatalf("quest %s agent %s recorded no events", questID, questLeadID)
	}
}

// findAgent returns the agent with id in the slice, or nil. Test-only lookup over
// the host's recorded coordinating agents.
func findAgent(agents []run.Agent, id string) *run.Agent {
	for i := range agents {
		if agents[i].ID == id {
			return &agents[i]
		}
	}
	return nil
}
