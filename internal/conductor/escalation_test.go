package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// decisionClaude answers any escalated-decision spawn with a DECISION verdict.
var decisionClaude = stubClaude(
	role("resolving a decision", emitText(`DECISION {\"answer\":\"narrow the scope to one unit and proceed\"}`)+emitResult("decided", 1)),
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

	esc, resolved := c.escalateRunDecision(t.Context(), r, "cannot find a working split — decide", repo, nil)
	if !resolved {
		t.Fatal("the decider emitted a DECISION — the escalation must report resolved")
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
// rule) under the real "tl" identity — no phantom second agent.
func TestEscalateStandaloneRunSelfDecides(t *testing.T) {
	c, repo := deliveryConductor(t, decisionClaude)
	id := c.Create(run.Spec{Prompt: "x", Folders: []string{repo}})
	r, _ := c.Get(id) // no QuestID/CampaignID
	esc, resolved := c.escalateRunDecision(t.Context(), r, "decide the trade-off", repo, nil)
	if esc.Decider != RoleTechLead {
		t.Errorf("a standalone run's top tier decider = %q, want tech-lead", esc.Decider)
	}
	if !resolved || esc.Answer == "" {
		t.Error("the top tier must still record a resolved decision")
	}
	// The decider reused the "tl" agent identity — no phantom second agent id.
	got, _ := c.Get(id)
	seen := map[string]int{}
	for _, a := range got.Agents {
		seen[a.ID]++
	}
	if seen["tech-lead"] > 0 {
		t.Errorf("standalone-run decider must reuse the 'tl' identity, not spawn a 'tech-lead' agent; agents=%v", seen)
	}
}

// E3: a quest-lead's give-up escalates ONE tier up. For a campaign-child quest that
// is the campaign tech-manager; the resolution is recorded on the quest and the flow
// proceeds.
func TestEscalateQuestDecisionReachesResolution(t *testing.T) {
	c, repo := deliveryConductor(t, decisionClaude)
	qid := c.CreateQuest(run.QuestSpec{Objective: "tidy", Folders: []string{repo}, CampaignID: "c1"})
	q, _ := c.GetQuest(qid)

	esc, resolved := c.escalateQuestDecision(t.Context(), q, "discovery stuck — decide", repo, nil)
	if !resolved {
		t.Fatal("the decider emitted a DECISION — must be resolved")
	}
	if esc.From != "quest-lead" || esc.Decider != RoleTechManager {
		t.Errorf("campaign-child quest escalates quest-lead → tech-manager, got from=%q decider=%q", esc.From, esc.Decider)
	}
	got, _ := c.GetQuest(qid)
	if len(got.Escalations) != 1 || got.Escalations[0].Decider != RoleTechManager {
		t.Fatalf("the escalation must be recorded on the quest, got %+v", got.Escalations)
	}
}

// E3: a campaign tech-manager's give-up escalates ONE tier up to the intent-manager
// (the campaign top tier); the resolution is recorded on the campaign.
func TestEscalateCampaignDecisionReachesResolution(t *testing.T) {
	c, repo := deliveryConductor(t, decisionClaude)
	cid := c.CreateCampaign(run.CampaignSpec{Input: "ship the thing", Folders: []string{repo}})
	cam, _ := c.GetCampaign(cid)

	esc, resolved := c.escalateCampaignDecision(t.Context(), cam, "gaps remain after remediation — decide", repo, nil)
	if !resolved {
		t.Fatal("the decider emitted a DECISION — must be resolved")
	}
	if esc.From != "tech-manager" || esc.Decider != RoleIntentManager {
		t.Errorf("campaign escalates tech-manager → intent-manager, got from=%q decider=%q", esc.From, esc.Decider)
	}
	got, _ := c.GetCampaign(cid)
	if len(got.Escalations) != 1 || got.Escalations[0].Decider != RoleIntentManager {
		t.Fatalf("the escalation must be recorded on the campaign, got %+v", got.Escalations)
	}
}

// parseDecision pins the DECISION verdict convention; decisionBlocks classifies a
// block-signalling answer.
func TestParseDecisionAndBlocks(t *testing.T) {
	ans, ok := parseDecision(`preamble
DECISION {"answer":"go with option B"}`)
	if !ok || ans != "go with option B" {
		t.Fatalf("DECISION must parse, got ok=%v ans=%q", ok, ans)
	}
	if _, ok := parseDecision("no verdict"); ok {
		t.Error("text without a DECISION line must report ok=false")
	}
	if !decisionBlocks("this is a hard blocker, cannot proceed") {
		t.Error("a block-signalling answer must classify as blocks")
	}
	if decisionBlocks("narrow the scope and proceed") {
		t.Error("a proceed answer must not classify as blocks")
	}
}
