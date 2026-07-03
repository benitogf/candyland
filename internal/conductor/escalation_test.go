package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// decisionClaude answers any escalated-decision spawn with a DECISION verdict.
var decisionClaude = stubClaude(
	role("resolving a decision", emitText(`DECISION {\"answer\":\"narrow the split to one file and proceed\"}`)+emitResult("decided", 1)),
	coder(emitResult("noop", 0)),
)

// E3: a decision escalated one tier up (run tech-lead → owning quest-lead) reaches a
// RESOLUTION (the decider's DECISION answer), is recorded on the run, and the flow
// proceeds — never pausing for a human.
func TestEscalateRunDecisionReachesResolution(t *testing.T) {
	c, repo := deliveryConductor(t, decisionClaude)
	id := c.Create(run.Spec{Prompt: "x", Folders: []string{repo}})
	c.Update(id, func(r *run.Run) { r.QuestID = "q1" }) // a quest child → escalates to quest-lead
	r, _ := c.Get(id)

	esc := c.escalateRunDecision(t.Context(), r, "cannot find a working split — decide", repo, nil)
	if esc.Answer == "" {
		t.Fatal("the escalation must resolve with a decider answer")
	}
	if esc.From != "tech-lead" || esc.Decider != RoleQuestLead {
		t.Errorf("escalation tiers wrong: from=%q decider=%q (want tech-lead → quest-lead)", esc.From, esc.Decider)
	}
	if !containsFold(esc.Answer, "narrow") {
		t.Errorf("the decider's DECISION answer must be used, got %q", esc.Answer)
	}
	got, _ := c.Get(id)
	if len(got.Escalations) != 1 || got.Escalations[0].Decider != RoleQuestLead {
		t.Fatalf("the escalation+resolution must be recorded on the run, got %+v", got.Escalations)
	}
}

// A standalone run's tech-lead is the top tier: it decides+records itself (the smith
// rule), escalating to no one above.
func TestEscalateStandaloneRunSelfDecides(t *testing.T) {
	c, repo := deliveryConductor(t, decisionClaude)
	id := c.Create(run.Spec{Prompt: "x", Folders: []string{repo}})
	r, _ := c.Get(id) // no QuestID/CampaignID
	esc := c.escalateRunDecision(t.Context(), r, "decide the trade-off", repo, nil)
	if esc.Decider != RoleTechLead {
		t.Errorf("a standalone run's top tier decider = %q, want tech-lead", esc.Decider)
	}
	if esc.Answer == "" {
		t.Error("the top tier must still record a decision")
	}
}

// parseDecision pins the DECISION verdict convention.
func TestParseDecision(t *testing.T) {
	ans, ok := parseDecision(`preamble
DECISION {"answer":"go with option B"}`)
	if !ok || ans != "go with option B" {
		t.Fatalf("DECISION must parse, got ok=%v ans=%q", ok, ans)
	}
	if _, ok := parseDecision("no verdict"); ok {
		t.Error("text without a DECISION line must report ok=false")
	}
}
