package conductor

import (
	"context"
	"sync"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// O4 — a campaign's/quest's OWN coordinating agents (the supervisor's intent-lead/
// reviewer, the quest-lead) are spawned under the campaign/quest id, and the
// agent-recording path (streamOnce → mapAgentLine → updateAgentHost) must land
// their state+events on the PARENT record so the dashboard can show what the
// campaign/quest itself is doing — not drop them onto a non-existent run runtime.
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

// TestSupervisorAgentsRecordOnParent drives streamOnce directly against a campaign
// id and a quest id (the exact ids the real supervisor/quest-lead run under) and
// asserts each parent record carries the coordinating agent with its events — the
// hasAgents-equivalent the dashboard reads. Without O4's routing these landed on
// c.runs[<campaign/quest id>] (nil) and were silently dropped.
func TestSupervisorAgentsRecordOnParent(t *testing.T) {
	c, repo := deliveryConductor(t, supervisorAgentClaude)

	campaignID := c.CreateCampaign(run.CampaignSpec{Input: "ship the thing", Folders: []string{repo}})
	questID := c.CreateQuest(run.QuestSpec{Objective: "tidy the thing", Folders: []string{repo}})

	// Freshly created, neither parent has spawned a coordinating agent yet.
	if cam, ok := c.GetCampaign(campaignID); !ok || len(cam.Agents) != 0 {
		t.Fatalf("new campaign should start with no agents, got %d", len(cam.Agents))
	}
	if q, ok := c.GetQuest(questID); !ok || len(q.Agents) != 0 {
		t.Fatalf("new quest should start with no agents, got %d", len(q.Agents))
	}

	// Run the two coordinating agents concurrently through the REAL spawn path.
	// Exact-count WaitGroup(2): one Done per streamOnce — no sync.Once, the count
	// is a hard assertion that both spawns completed.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamOnce(context.Background(), c, campaignID, intentLeadID, "supervisor", repo, nil)
	}()
	go func() {
		defer wg.Done()
		streamOnce(context.Background(), c, questID, questLeadID, "supervisor", repo, nil)
	}()
	wg.Wait()

	// The campaign's intent-lead is recorded on the campaign record (not dropped).
	cam, ok := c.GetCampaign(campaignID)
	if !ok {
		t.Fatal("campaign lost")
	}
	a := findAgent(cam.Agents, intentLeadID)
	if a == nil {
		t.Fatalf("campaign %s has no %s agent — supervisor state was dropped (hasAgents:false)", campaignID, intentLeadID)
	}
	if len(a.Events) == 0 {
		t.Fatalf("campaign %s agent %s recorded no events", campaignID, intentLeadID)
	}

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
