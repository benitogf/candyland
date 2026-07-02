package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// Stopping a campaign CASCADES to its child quests: each is marked stopped (and its
// tick drive halted) so no quest keeps ticking — and launching runs — after the
// campaign is stopped. The run subtree is halted via Command (covered by
// TestStopHaltsWithoutFalseGreen); here we pin the campaign→quest cascade.
func TestStopCampaignCascadesToChildQuests(t *testing.T) {
	c, _ := newQuestServer(t)

	camID := c.CreateCampaign(run.CampaignSpec{Input: "build the thing", Folders: []string{"/repo"}})
	qID := c.CreateQuest(run.QuestSpec{Objective: "a child quest", Folders: []string{"/repo"}, CampaignID: camID})

	if !c.StopCampaign(camID, "operator halt") {
		t.Fatal("StopCampaign should succeed")
	}

	cam, _ := c.GetCampaign(camID)
	if cam.Status != "stopped" {
		t.Errorf("campaign status = %q, want stopped", cam.Status)
	}
	q, _ := c.GetQuest(qID)
	if q.Status != "stopped" {
		t.Errorf("child quest status = %q, want stopped (cascade)", q.Status)
	}
	if q.PauseReason != "campaign stopped" {
		t.Errorf("child quest stop reason = %q, want %q", q.PauseReason, "campaign stopped")
	}
}

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
