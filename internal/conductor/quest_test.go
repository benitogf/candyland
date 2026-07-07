package conductor

import (
	"os"
	"testing"
	"time"

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
// launch fields (Deliver, CampaignID, TokenBudget, objective).
func TestCreateQuestRoundTrips(t *testing.T) {
	c, _ := newQuestServer(t)

	id := c.CreateQuest(run.QuestSpec{
		Objective:   "keep the lint clean",
		Folders:     []string{"/repo"},
		Scope:       "internal/ only",
		Safety:      "do not touch vendor/",
		Verify:      []string{"go build ./...", "go vet ./..."},
		Stop:        "no items two ticks running",
		TokenBudget: 5000,
		Deliver:     run.DeliverBranch,
		CampaignID:  "c7",
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

// CreateQuest applies the safe defaults (deliver pr) when the spec leaves
// Deliver empty.
func TestCreateQuestDefaults(t *testing.T) {
	c, _ := newQuestServer(t)
	q, ok := c.GetQuest(c.CreateQuest(run.QuestSpec{Objective: "audit"}))
	if !ok {
		t.Fatal("quest not found")
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

// A burst of coordinating-agent writes on a quest coalesces: with the flush window
// held open, none reach storage; a single boundary flush persists all of them at
// once, and the in-memory buffer never loses an event.
func TestCoalesceQuestAgentWrites(t *testing.T) {
	c, _ := newQuestServer(t)
	c.coalesceWindow = time.Hour // hold the debounce open so only the explicit flush writes
	id := c.CreateQuest(run.QuestSpec{Objective: "keep lint clean"})

	const writes = 25
	for i := 0; i < writes; i++ {
		c.recordAgentEvent(id, func(agents *[]run.Agent) {
			appendToAgentIn(agents, questLeadID, run.Event{T: "text", Text: "tok"}, 0)
		})
	}

	// Still buffered: the durable record has not seen any of the per-token writes.
	if q, _ := c.GetQuest(id); len(q.Agents) != 0 {
		t.Fatalf("agent writes reached storage before flush: %d agents", len(q.Agents))
	}

	c.flushAgentWrites(id)

	q, ok := c.GetQuest(id)
	if !ok {
		t.Fatal("quest gone after flush")
	}
	if len(q.Agents) != 1 || q.Agents[0].ID != questLeadID {
		t.Fatalf("coalesced agent not persisted: %+v", q.Agents)
	}
	if got := len(q.Agents[0].Events); got != writes {
		t.Errorf("coalesced events lost: got %d, want %d", got, writes)
	}
}

// QuestBranch derives the shared branch a quest's child runs accumulate on:
// campaign/<id> for a campaign-child quest (any policy), quest/<id> for a standalone
// converge quest, and "" for a perFinding (adventure) or feedback/review quest.
func TestQuestBranchDerivation(t *testing.T) {
	// Campaign-child quest → the campaign branch, regardless of convergence policy.
	if b := QuestBranch(run.Quest{ID: "q1", CampaignID: "c42", Deliver: run.DeliverBranch}); b != "campaign/c42" {
		t.Errorf("campaign-child quest branch = %q, want campaign/c42", b)
	}
	if b := QuestBranch(run.Quest{ID: "q1", CampaignID: "c42", Convergence: run.ConvergePerFinding}); b != "campaign/c42" {
		t.Errorf("campaign-child quest (perFinding) branch = %q, want campaign/c42", b)
	}
	// Standalone converge quest → its own quest/<id> branch.
	if b := QuestBranch(run.Quest{ID: "q7", Convergence: run.ConvergeConverge}); b != "quest/q7" {
		t.Errorf("standalone converge quest branch = %q, want quest/q7", b)
	}
	// Standalone perFinding (adventure) quest → no shared branch (a PR per finding).
	if b := QuestBranch(run.Quest{ID: "q7", Convergence: run.ConvergePerFinding}); b != "" {
		t.Errorf("perFinding quest branch = %q, want empty", b)
	}
	// Feedback/review quest works the target PR's head branch — no owned branch.
	if b := QuestBranch(run.Quest{ID: "q7", Deliver: run.DeliverReview, TargetPR: 9}); b != "" {
		t.Errorf("feedback/review quest branch = %q, want empty", b)
	}
}
