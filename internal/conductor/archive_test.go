package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// ArchiveQuest / ArchiveCampaign set the Archived flag (dashboard dismiss) while
// keeping the record in storage (the Work history) — hide, never delete.
func TestArchiveQuestAndCampaign(t *testing.T) {
	c, _ := newQuestServer(t)

	qID := c.CreateQuest(run.QuestSpec{Objective: "x", Folders: []string{"/repo"}})
	if !c.ArchiveQuest(qID) {
		t.Fatal("ArchiveQuest should succeed for a known quest")
	}
	q, ok := c.GetQuest(qID)
	if !ok || !q.Archived {
		t.Errorf("quest should be archived and still readable: ok=%v archived=%v", ok, q.Archived)
	}

	camID := c.CreateCampaign(run.CampaignSpec{Input: "y", Folders: []string{"/repo"}})
	if !c.ArchiveCampaign(camID) {
		t.Fatal("ArchiveCampaign should succeed for a known campaign")
	}
	cam, ok := c.GetCampaign(camID)
	if !ok || !cam.Archived {
		t.Errorf("campaign should be archived and still readable: ok=%v archived=%v", ok, cam.Archived)
	}

	if c.ArchiveQuest("nope") || c.ArchiveCampaign("nope") {
		t.Error("archiving an unknown quest/campaign must return false")
	}
}
