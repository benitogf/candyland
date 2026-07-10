package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// ArchiveQuest sets the Archived flag (dashboard dismiss) while keeping the record
// in storage (the Work history) — hide, never delete.
func TestArchiveQuest(t *testing.T) {
	c, _ := newQuestServer(t)

	qID := c.CreateQuest(run.QuestSpec{Objective: "x", Folders: []string{"/repo"}})
	if !c.ArchiveQuest(qID) {
		t.Fatal("ArchiveQuest should succeed for a known quest")
	}
	q, ok := c.GetQuest(qID)
	if !ok || !q.Archived {
		t.Errorf("quest should be archived and still readable: ok=%v archived=%v", ok, q.Archived)
	}

	if c.ArchiveQuest("nope") {
		t.Error("archiving an unknown quest must return false")
	}
}
