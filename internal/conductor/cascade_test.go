package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// Stopping a quest cascades to its child runs. QuestChildRuns is read from storage,
// so a persisted child run with QuestID set is halted via Command. A run with no
// live executor simply no-ops (Command returns false) rather than erroring — the
// cascade must not depend on a live executor being present for every child.
func TestStopQuestCascadeIsSafeWithoutLiveRuns(t *testing.T) {
	c, _ := newQuestServer(t)
	qID := c.CreateQuest(run.QuestSpec{Objective: "x", Folders: []string{"/repo"}})

	// A persisted, untracked child run (no live executor) — the cascade reaches it
	// via QuestChildRuns and issues a no-op stop; StopQuest must still succeed.
	rID := c.Create(run.Spec{Prompt: "child", Folders: []string{"/repo"}})
	c.Update(rID, func(r *run.Run) { r.QuestID = qID })

	if !c.StopQuest(qID, "halt") {
		t.Fatal("StopQuest should succeed even when a child run has no live executor")
	}
	q, _ := c.GetQuest(qID)
	if q.Status != "stopped" {
		t.Errorf("quest status = %q, want stopped", q.Status)
	}
}

// Stopping a quest stamps its OWN coordinating agents (quest lead) terminal — not
// just its child runs' agents. Without the stamp the dashboard renders a killed
// coordinator as "Working" beside a stopped parent (the stopped+Working
// contradiction). Genuinely terminal agents keep their real outcome.
func TestStopStampsCoordinatingAgentsTerminal(t *testing.T) {
	c, _ := newQuestServer(t)

	qID := c.CreateQuest(run.QuestSpec{Objective: "a child quest", Folders: []string{"/repo"}})

	// Seed in-flight + terminal coordinating agents the way mapAgentLine would.
	c.UpdateQuest(qID, func(q *run.Quest) {
		q.Agents = []run.Agent{
			{ID: questLeadID, Role: RoleQuestLead, State: "working"},
		}
	})

	if !c.StopQuest(qID, "operator halt") {
		t.Fatal("StopQuest should succeed")
	}

	q, _ := c.GetQuest(qID)
	for _, a := range q.Agents {
		if a.ID == questLeadID && a.State != "stopped" {
			t.Errorf("quest lead state = %q, want stopped", a.State)
		}
	}
}
