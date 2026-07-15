package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// ActiveRuns counts only in-memory runtimes in the "running" state; Active
// Quests counts only quests persisted as "running". These are the activity
// signals /api/health exposes and detritus reads to decide a smart takeover.
func TestActiveRunsAndQuestsCounts(t *testing.T) {
	c := newIncidentServer(t)

	c.mu.Lock()
	c.runs["r1"] = &runtime{r: run.Run{ID: "r1", Status: "running"}}
	c.runs["r2"] = &runtime{r: run.Run{ID: "r2", Status: "done"}}
	c.runs["r3"] = &runtime{r: run.Run{ID: "r3", Status: "planning"}}
	c.mu.Unlock()

	if got := c.ActiveRuns(); got != 1 {
		t.Errorf("ActiveRuns = %d, want 1 (only r1 is running)", got)
	}

	c.publishQuest(run.Quest{ID: "q1", Status: "running"})
	c.publishQuest(run.Quest{ID: "q2", Status: "done"})
	c.publishQuest(run.Quest{ID: "q3", Status: "running"})

	if got := c.ActiveQuests(); got != 2 {
		t.Errorf("ActiveQuests = %d, want 2 (q1, q3 running)", got)
	}
}
