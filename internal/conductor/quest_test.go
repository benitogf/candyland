package conductor

import (
	"os"
	"testing"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// newQuestServer builds a serverful conductor backed by an in-memory ooo store
// with the quests/* filter open, matching how audit_test sets up a run server.
func newQuestServer(t *testing.T) (*Conductor, *ooo.Server) {
	t.Helper()
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	srv.OpenFilter("quests/*")
	c := New(srv)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(os.Interrupt) })
	return c, srv
}

// CreateQuest persists a quest and GetQuest round-trips it, including the settled
// launch fields (AutonomyLevel, Deliver, CampaignID, TokenBudget, objective).
func TestCreateQuestRoundTrips(t *testing.T) {
	c, _ := newQuestServer(t)

	id := c.CreateQuest(run.QuestSpec{
		Objective:     "keep the lint clean",
		Folders:       []string{"/repo"},
		Scope:         "internal/ only",
		Safety:        "do not touch vendor/",
		Verify:        []string{"go build ./...", "go vet ./..."},
		Stop:          "no items two ticks running",
		AutonomyLevel: run.AutonomyUnattended,
		TokenBudget:   5000,
		Deliver:       run.DeliverBranch,
		CampaignID:    "c7",
	})
	if id != "q1" {
		t.Fatalf("first quest id = %q, want q1", id)
	}

	q, ok := c.GetQuest(id)
	if !ok {
		t.Fatalf("GetQuest(%q) not found", id)
	}
	if q.OriginalObjective != "keep the lint clean" || q.Objective != "keep the lint clean" {
		t.Errorf("objective not captured: original=%q working=%q", q.OriginalObjective, q.Objective)
	}
	if q.AutonomyLevel != run.AutonomyUnattended {
		t.Errorf("autonomyLevel = %q, want %q", q.AutonomyLevel, run.AutonomyUnattended)
	}
	if q.Deliver != run.DeliverBranch {
		t.Errorf("deliver = %q, want %q", q.Deliver, run.DeliverBranch)
	}
	if q.CampaignID != "c7" {
		t.Errorf("campaignId = %q, want c7", q.CampaignID)
	}
	if q.TokenBudget != 5000 {
		t.Errorf("tokenBudget = %d, want 5000", q.TokenBudget)
	}
	if q.Status != "running" {
		t.Errorf("status = %q, want running", q.Status)
	}
	if q.TraceVersion != run.TraceVersion {
		t.Errorf("traceVersion = %d, want %d", q.TraceVersion, run.TraceVersion)
	}
	if q.CreatedAt == "" || q.UpdatedAt == "" {
		t.Errorf("timestamps not stamped: created=%q updated=%q", q.CreatedAt, q.UpdatedAt)
	}
}

// CreateQuest applies the safe defaults (L1 report-only, deliver pr) when the spec
// leaves AutonomyLevel/Deliver empty.
func TestCreateQuestDefaults(t *testing.T) {
	c, _ := newQuestServer(t)
	q, ok := c.GetQuest(c.CreateQuest(run.QuestSpec{Objective: "audit"}))
	if !ok {
		t.Fatal("quest not found")
	}
	if q.AutonomyLevel != run.AutonomyReportOnly {
		t.Errorf("default autonomyLevel = %q, want %q", q.AutonomyLevel, run.AutonomyReportOnly)
	}
	if q.Deliver != run.DeliverPR {
		t.Errorf("default deliver = %q, want %q", q.Deliver, run.DeliverPR)
	}
	if q.CampaignID != "" {
		t.Errorf("standalone quest campaignId = %q, want empty", q.CampaignID)
	}
}

// UpdateQuest mutates a quest durably (the change survives a fresh GetQuest read
// from storage), and stamps UpdatedAt.
func TestUpdateQuestDurable(t *testing.T) {
	c, _ := newQuestServer(t)
	id := c.CreateQuest(run.QuestSpec{Objective: "fix flakes"})

	if !c.UpdateQuest(id, func(q *run.Quest) {
		q.Status = "blocked"
		q.PauseReason = "needs human review"
		q.TokensUsed = 1200
		q.WorkItems = append(q.WorkItems, run.WorkItem{ID: "w1", SourceTick: "t1", Disposition: "completed"})
		q.Ticks = append(q.Ticks, run.Tick{ID: "t1", StartedAt: "2026-06-30T00:00:00Z"})
		q.ItemsCompleted = 1
		q.PRsOpened = 1
	}) {
		t.Fatal("UpdateQuest returned false for a known quest")
	}

	q, ok := c.GetQuest(id)
	if !ok {
		t.Fatal("quest gone after update")
	}
	if q.Status != "blocked" || q.PauseReason != "needs human review" {
		t.Errorf("status/pauseReason not persisted: %q / %q", q.Status, q.PauseReason)
	}
	if q.TokensUsed != 1200 || q.ItemsCompleted != 1 || q.PRsOpened != 1 {
		t.Errorf("rollups not persisted: tokens=%d completed=%d prs=%d", q.TokensUsed, q.ItemsCompleted, q.PRsOpened)
	}
	if len(q.WorkItems) != 1 || q.WorkItems[0].ID != "w1" {
		t.Errorf("work items not persisted: %+v", q.WorkItems)
	}
	if len(q.Ticks) != 1 || q.Ticks[0].ID != "t1" {
		t.Errorf("ticks not persisted: %+v", q.Ticks)
	}

	if c.UpdateQuest("nope", func(*run.Quest) {}) {
		t.Error("UpdateQuest on an unknown quest should return false")
	}
}

// QuestBranch derives campaign/<id> only for a campaign-owned (branch-delivered)
// quest, and "" for a standalone (pr-delivered) quest.
func TestQuestBranchDerivation(t *testing.T) {
	branch := QuestBranch(run.Quest{CampaignID: "c42", Deliver: run.DeliverBranch})
	if branch != "campaign/c42" {
		t.Errorf("branch-delivered quest branch = %q, want campaign/c42", branch)
	}
	if b := QuestBranch(run.Quest{CampaignID: "c42", Deliver: run.DeliverPR}); b != "" {
		t.Errorf("pr-delivered quest branch = %q, want empty", b)
	}
	// branch delivery with no campaign link has no shared branch to derive.
	if b := QuestBranch(run.Quest{Deliver: run.DeliverBranch}); b != "" {
		t.Errorf("orphan branch quest = %q, want empty", b)
	}
}
