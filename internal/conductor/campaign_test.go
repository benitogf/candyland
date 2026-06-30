package conductor

import (
	"os"
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// newCampaignServer builds a serverful conductor backed by an in-memory ooo store
// with the campaigns/* filter open, matching how newQuestServer sets up a quest
// server.
func newCampaignServer(t *testing.T) (*Conductor, *ooo.Server) {
	t.Helper()
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	srv.OpenFilter("campaigns/*")
	c := New(srv)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(os.Interrupt) })
	return c, srv
}

// CreateCampaign persists a campaign and GetCampaign round-trips it, including the
// immutable OriginalInput, the L2 autonomy/budget, and that brief commitments
// survive once written.
func TestCreateCampaignRoundTrips(t *testing.T) {
	c, _ := newCampaignServer(t)

	id := c.CreateCampaign(run.CampaignSpec{
		Input:         "ship the billing redesign across api and web",
		Folders:       []string{"/repo"},
		AutonomyLevel: run.AutonomyUnattended,
		TokenBudget:   90000,
	})
	if id != "c1" {
		t.Fatalf("first campaign id = %q, want c1", id)
	}

	cam, ok := c.GetCampaign(id)
	if !ok {
		t.Fatalf("GetCampaign(%q) not found", id)
	}
	if cam.OriginalInput != "ship the billing redesign across api and web" {
		t.Errorf("originalInput not captured: %q", cam.OriginalInput)
	}
	if cam.AutonomyLevel != run.AutonomyUnattended {
		t.Errorf("autonomyLevel = %q, want %q", cam.AutonomyLevel, run.AutonomyUnattended)
	}
	if cam.TokenBudget != 90000 {
		t.Errorf("tokenBudget = %d, want 90000", cam.TokenBudget)
	}
	if cam.Status != "running" {
		t.Errorf("status = %q, want running", cam.Status)
	}
	if cam.TraceVersion != run.TraceVersion {
		t.Errorf("traceVersion = %d, want %d", cam.TraceVersion, run.TraceVersion)
	}
	if cam.CreatedAt == "" || cam.UpdatedAt == "" {
		t.Errorf("timestamps not stamped: created=%q updated=%q", cam.CreatedAt, cam.UpdatedAt)
	}
	if len(cam.QuestIDs) != 0 || len(cam.RunIDs) != 0 {
		t.Errorf("fresh campaign should have no children: quests=%v runs=%v", cam.QuestIDs, cam.RunIDs)
	}
}

// CreateCampaign seeds the children slices non-nil so the persisted JSON carries
// [] (not null) — the UI reads them as arrays, matching how quests seed
// WorkItems/Ticks.
func TestCreateCampaignChildrenMarshalAsArrays(t *testing.T) {
	c, srv := newCampaignServer(t)
	id := c.CreateCampaign(run.CampaignSpec{Input: "x"})
	obj, err := srv.Storage.Get("campaigns/" + id)
	if err != nil {
		t.Fatalf("storage get: %v", err)
	}
	body := string(obj.Data)
	if !strings.Contains(body, `"questIds":[]`) || !strings.Contains(body, `"runIds":[]`) {
		t.Errorf("children should marshal as [] not null: %s", body)
	}
}

// CreateCampaign defaults AutonomyLevel to L2 — and is NEVER L1 — when the spec
// leaves it empty or (defensively) passes the report-only floor.
func TestCreateCampaignAutonomyDefaultL2NotL1(t *testing.T) {
	c, _ := newCampaignServer(t)

	empty, ok := c.GetCampaign(c.CreateCampaign(run.CampaignSpec{Input: "do the thing"}))
	if !ok {
		t.Fatal("campaign not found")
	}
	if empty.AutonomyLevel != run.AutonomyGatePR {
		t.Errorf("empty autonomy default = %q, want %q (L2)", empty.AutonomyLevel, run.AutonomyGatePR)
	}

	// A report-only request must not strand a campaign with no PR: it is lifted to L2.
	l1, ok := c.GetCampaign(c.CreateCampaign(run.CampaignSpec{Input: "do it", AutonomyLevel: run.AutonomyReportOnly}))
	if !ok {
		t.Fatal("campaign not found")
	}
	if l1.AutonomyLevel == run.AutonomyReportOnly {
		t.Errorf("campaign autonomy = %q, must never be L1", l1.AutonomyLevel)
	}
	if l1.AutonomyLevel != run.AutonomyGatePR {
		t.Errorf("L1 request lifted to %q, want %q (L2)", l1.AutonomyLevel, run.AutonomyGatePR)
	}
}

// UpdateCampaign mutates a campaign durably (the change survives a fresh
// GetCampaign read from storage), including the brief commitments, gates, children,
// delivery PRs, and intent-review verdicts — and stamps UpdatedAt.
func TestUpdateCampaignDurable(t *testing.T) {
	c, _ := newCampaignServer(t)
	id := c.CreateCampaign(run.CampaignSpec{Input: "consolidate the gateway"})

	if !c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.Status = "blocked"
		cam.PauseReason = "open question unresolved"
		cam.TokensUsed = 4200
		cam.IntentBrief.RestatedGoal = "merge the v3 gateway"
		cam.IntentBrief.Commitments = append(cam.IntentBrief.Commitments,
			run.Commitment{ID: "k1", Statement: "the gateway compiles and serves baccarat"})
		cam.BriefGate = run.GateResult{Passed: true, Reason: "scope clear", DecidedAt: "2026-06-30T00:00:00Z"}
		cam.QuestIDs = append(cam.QuestIDs, "q9")
		cam.RunIDs = append(cam.RunIDs, "r9")
		cam.PRs = append(cam.PRs, run.PR{Repo: "/repo", URL: "http://pr/1"})
		cam.ReviewRouting = append(cam.ReviewRouting, "backend lead")
		cam.IntentReview.Verdicts = append(cam.IntentReview.Verdicts,
			run.CommitmentVerdict{CommitmentID: "k1", Verdict: "satisfied", Evidence: []string{"go build ./... green"}})
	}) {
		t.Fatal("UpdateCampaign returned false for a known campaign")
	}

	cam, ok := c.GetCampaign(id)
	if !ok {
		t.Fatal("campaign gone after update")
	}
	if cam.Status != "blocked" || cam.PauseReason != "open question unresolved" {
		t.Errorf("status/pauseReason not persisted: %q / %q", cam.Status, cam.PauseReason)
	}
	if cam.TokensUsed != 4200 {
		t.Errorf("tokensUsed = %d, want 4200", cam.TokensUsed)
	}
	if cam.IntentBrief.RestatedGoal != "merge the v3 gateway" {
		t.Errorf("brief goal not persisted: %q", cam.IntentBrief.RestatedGoal)
	}
	if len(cam.IntentBrief.Commitments) != 1 || cam.IntentBrief.Commitments[0].ID != "k1" {
		t.Errorf("commitments not persisted: %+v", cam.IntentBrief.Commitments)
	}
	if !cam.BriefGate.Passed || cam.BriefGate.Reason != "scope clear" {
		t.Errorf("briefGate not persisted: %+v", cam.BriefGate)
	}
	if len(cam.QuestIDs) != 1 || cam.QuestIDs[0] != "q9" || len(cam.RunIDs) != 1 || cam.RunIDs[0] != "r9" {
		t.Errorf("children not persisted: quests=%v runs=%v", cam.QuestIDs, cam.RunIDs)
	}
	if len(cam.PRs) != 1 || cam.PRs[0].URL != "http://pr/1" {
		t.Errorf("delivery PRs not persisted: %+v", cam.PRs)
	}
	if len(cam.IntentReview.Verdicts) != 1 || cam.IntentReview.Verdicts[0].Verdict != "satisfied" {
		t.Errorf("intent-review verdicts not persisted: %+v", cam.IntentReview.Verdicts)
	}
	if len(cam.IntentReview.Verdicts[0].Evidence) != 1 {
		t.Errorf("verdict evidence not persisted: %+v", cam.IntentReview.Verdicts[0])
	}

	if c.UpdateCampaign("nope", func(*run.Campaign) {}) {
		t.Error("UpdateCampaign on an unknown campaign should return false")
	}
}

// A durable supervisor note (e.g. a token-cap degrade-to-partial) SURVIVES the
// transient PauseReason lifecycle: clean delivery clears PauseReason and a block
// overwrites it, but neither wipes Notes — so the operator still learns the campaign
// delivered partial after a clean PR. This pins the fix for the silently-wiped note.
func TestCampaignNotesSurviveDeliveryAndBlock(t *testing.T) {
	c, _ := newCampaignServer(t)
	id := c.CreateCampaign(run.CampaignSpec{Input: "ship the export across api and web"})

	note := "token cap reached (100/100) — skipped 1 remaining child run(s), delivering partial"
	c.appendCampaignNote(id, note)

	// Clean delivery clears the transient PauseReason (mirrors deliverCampaign).
	c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.Status = "done"
		cam.PauseReason = ""
	})
	cam, _ := c.GetCampaign(id)
	if cam.PauseReason != "" {
		t.Errorf("clean delivery must clear PauseReason, got %q", cam.PauseReason)
	}
	if len(cam.Notes) != 1 || cam.Notes[0] != note {
		t.Fatalf("the durable degrade note must survive delivery, got %v", cam.Notes)
	}

	// A subsequent block overwrites PauseReason but still must not wipe Notes.
	c.blockCampaign(id, "some later blocker")
	cam, _ = c.GetCampaign(id)
	// (blockCampaign no-ops on a done campaign; flip to a non-terminal status first.)
	c.UpdateCampaign(id, func(cam *run.Campaign) { cam.Status = "running" })
	c.blockCampaign(id, "some later blocker")
	cam, _ = c.GetCampaign(id)
	if cam.PauseReason != "some later blocker" {
		t.Errorf("block must set its own PauseReason, got %q", cam.PauseReason)
	}
	if len(cam.Notes) != 1 || cam.Notes[0] != note {
		t.Errorf("the durable degrade note must survive a block, got %v", cam.Notes)
	}

	// appendCampaignNote accumulates rather than replacing.
	c.appendCampaignNote(id, "second note")
	cam, _ = c.GetCampaign(id)
	if len(cam.Notes) != 2 {
		t.Errorf("appendCampaignNote must accumulate notes, got %v", cam.Notes)
	}
}

// CampaignBranch derives campaign/<id> for a campaign with an id, and "" when the
// id is unset.
func TestCampaignBranchDerivation(t *testing.T) {
	if b := CampaignBranch(run.Campaign{ID: "c42"}); b != "campaign/c42" {
		t.Errorf("campaign branch = %q, want campaign/c42", b)
	}
	if b := CampaignBranch(run.Campaign{}); b != "" {
		t.Errorf("unset-id campaign branch = %q, want empty", b)
	}
}
