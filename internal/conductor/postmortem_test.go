package conductor

import (
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// stuckClaude: the quest lead surfaces no verdict AND the escalation decider emits no
// DECISION — so a quest give-up escalation stays UNRESOLVED → terminal blocked, which
// must carry a schema-valid postmortem (E2). The "resolving a decision" fragment is
// ordered first so the decider spawn (decisionBootstrap) matches it, not the quest lead.
var stuckClaude = stubClaude(
	role("resolving a decision", emitText("I cannot resolve this")+emitResult("x", 1)),
	role("quest lead", emitText("no idea")+emitResult("x", 1)),
	coder(emitResult("noop", 0)),
)

// E2 invariant: a quest that terminates blocked (discovery failed, escalation reached
// no resolution) carries a schema-valid postmortem — no bare blocked write.
func TestBlockedQuestCarriesPostmortem(t *testing.T) {
	c, repo := deliveryConductor(t, stuckClaude)
	id := c.CreateQuest(run.QuestSpec{Objective: "tidy", Folders: []string{repo}})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false")
	}
	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "blocked" }, 60*time.Second)
	if q.Status != "blocked" {
		t.Fatalf("quest must terminate blocked, got %q", q.Status)
	}
	if ok, why := validatePostmortem(q.Postmortem); !ok {
		t.Fatalf("a blocked quest must carry a schema-valid postmortem: %+v (%s)", q.Postmortem, why)
	}
}

// E2 invariant: a blocked campaign carries a schema-valid postmortem.
func TestBlockedCampaignCarriesPostmortem(t *testing.T) {
	c, _ := newCampaignServer(t)
	cid := c.CreateCampaign(run.CampaignSpec{Input: "ship the thing", Folders: []string{"/repo"}})
	c.blockCampaign(cid, "gate 2 still finds gaps after remediation")
	cam, _ := c.GetCampaign(cid)
	if cam.Status != "blocked" {
		t.Fatalf("campaign must be blocked, got %q", cam.Status)
	}
	if ok, why := validatePostmortem(cam.Postmortem); !ok {
		t.Fatalf("a blocked campaign must carry a schema-valid postmortem: %+v (%s)", cam.Postmortem, why)
	}
}

// E2 invariant: every run block via fail() (the single choke point, incl. delivery
// blocks) carries a schema-valid postmortem.
func TestFailAttachesRunPostmortem(t *testing.T) {
	c, _ := newQuestServer(t)
	id := c.Create(run.Spec{Prompt: "x", Folders: []string{"/repo"}})
	fail(t.Context(), c, id, "tl", "No pull request could be opened. push failed")
	r, _ := c.Get(id)
	if r.Error == "" {
		t.Fatal("fail must record an error")
	}
	if ok, why := validatePostmortem(r.Postmortem); !ok {
		t.Fatalf("a blocked run via fail() must carry a schema-valid postmortem: %+v (%s)", r.Postmortem, why)
	}
}

// E2 (§3): a postmortem is valid only with ALL fields; a missing field is rejected
// and the reason names the first missing field (so the bounce-back is actionable).
func TestValidatePostmortem(t *testing.T) {
	if ok, _ := validatePostmortem(nil); ok {
		t.Error("a nil postmortem must be rejected")
	}
	incomplete := &run.Postmortem{Attempts: []string{"a"}, FailingCapability: "x"}
	ok, why := validatePostmortem(incomplete)
	if ok {
		t.Error("an incomplete postmortem must be rejected")
	}
	if !strings.Contains(why, "evidence") {
		t.Errorf("the reject reason must name the first missing field, got %q", why)
	}
	if ok, why := validatePostmortem(synthPostmortem("tl", "cap", "ev", 2, "branch x")); !ok {
		t.Fatalf("a synthesised postmortem must be schema-valid: %s", why)
	}
}

// item 3 (postmortem-attr): a synthesised postmortem attributes the conductor
// mechanism that produced the block — the mechanism string is carried into the root
// cause, so a reader knows not just what capability failed but which mechanism it
// failed under. Absent a mechanism, the generic root cause is used (backward compat).
func TestSynthPostmortemCarriesMechanism(t *testing.T) {
	pm := synthPostmortem("tl", "review of x has unresolved blockers", "ev", 1, "branch x", reviewGateMechanism)
	if ok, why := validatePostmortem(pm); !ok {
		t.Fatalf("a mechanism-attributed postmortem must be schema-valid: %s", why)
	}
	if !strings.Contains(pm.RootCauseSoFar, reviewGateMechanism) {
		t.Errorf("the root cause must name the mechanism %q, got %q", reviewGateMechanism, pm.RootCauseSoFar)
	}

	generic := synthPostmortem("tl", "cap", "ev", 1, "br")
	if strings.Contains(generic.RootCauseSoFar, reviewGateMechanism) {
		t.Errorf("without a mechanism the root cause must not fabricate one, got %q", generic.RootCauseSoFar)
	}
}

// item 3 (postmortem-attr): failReview — the single review-phase choke point — stamps
// its review-gate mechanism onto the postmortem it attaches when it blocks a quest.
func TestFailReviewCarriesMechanism(t *testing.T) {
	c, _ := newQuestServer(t)
	id := c.CreateQuest(run.QuestSpec{Objective: "tidy", Folders: []string{"/repo"}})
	c.failReview(t.Context(), id, reviewerID, "Review of x did not converge. No PR is opened until review is clean.")
	q, ok := c.GetQuest(id)
	if !ok {
		t.Fatal("quest must exist")
	}
	if valid, why := validatePostmortem(q.Postmortem); !valid {
		t.Fatalf("failReview must attach a schema-valid postmortem: %+v (%s)", q.Postmortem, why)
	}
	if !strings.Contains(q.Postmortem.RootCauseSoFar, reviewGateMechanism) {
		t.Errorf("failReview's postmortem must attribute the %q mechanism, got %q", reviewGateMechanism, q.Postmortem.RootCauseSoFar)
	}
}

// E2 reject path: a `blocked` write with an incomplete postmortem is REJECTED (not
// persisted); a schema-valid one is accepted and persisted on the run record.
func TestAttachRunPostmortemRejectsInvalid(t *testing.T) {
	c, _ := newQuestServer(t)
	id := c.Create(run.Spec{Prompt: "x", Folders: []string{"/repo"}})

	if c.attachRunPostmortem(id, &run.Postmortem{FailingCapability: "x"}) {
		t.Error("an incomplete postmortem must be rejected by the gate")
	}
	if r, _ := c.Get(id); r.Postmortem != nil {
		t.Error("a rejected postmortem must not persist")
	}
	if !c.attachRunPostmortem(id, synthPostmortem("tl", "cap", "ev", 1, "br")) {
		t.Fatal("a schema-valid postmortem must attach")
	}
	if r, _ := c.Get(id); r.Postmortem == nil || r.Postmortem.FailingCapability != "cap" {
		t.Fatalf("a valid postmortem must persist on the run, got %+v", r.Postmortem)
	}
}

// blockerPostmortemFor uses the agent's OWN valid POSTMORTEM line when present, and
// falls back to a synthesised (always-valid) one when the agent's is absent or
// incomplete — the reject-and-loop-back landing on a guaranteed-valid postmortem.
func TestBlockerPostmortemFromAgentThenSynth(t *testing.T) {
	c, _ := newQuestServer(t)
	agentEmitted := `POSTMORTEM {"attempts":["1: gh auth failed"],"failingCapability":"gh not authenticated","evidence":"gh: HTTP 401","rootCauseSoFar":"no token","humanUnblockAction":"run gh auth login","partialWorkState":"branch feat/x pushed"}`
	pm := c.blockerPostmortemFor("tl", agentEmitted, "fallback cap", "fallback ev", 3, "")
	if ok, _ := validatePostmortem(pm); !ok {
		t.Fatal("the agent-emitted postmortem must be used and be valid")
	}
	if pm.FailingCapability != "gh not authenticated" {
		t.Errorf("the agent's own postmortem must win, got %q", pm.FailingCapability)
	}
	// An incomplete agent postmortem is bounced to the synthesised one.
	synth := c.blockerPostmortemFor("tl", `POSTMORTEM {"failingCapability":"partial"}`, "synth cap", "synth ev", 2, "")
	if ok, _ := validatePostmortem(synth); !ok {
		t.Fatal("an incomplete agent postmortem must fall back to a valid synthesised one")
	}
	if synth.FailingCapability != "synth cap" {
		t.Errorf("the incomplete agent postmortem must be bounced to synth, got %q", synth.FailingCapability)
	}
}
