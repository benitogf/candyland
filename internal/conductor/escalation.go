package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/candyland/internal/run"
)

// escalation.go implements the E3 upward DECISION-escalation ladder (contract §2).
// A decision a lower tier cannot resolve alone falls exactly ONE tier up, is decided
// at the lowest tier with authority (never by a human), and the escalation +
// resolution is RECORDED on the record (feeding L2 telemetry / the read-only audit):
//
//	coder → tech-lead → quest-lead → campaign tech-manager → intent-manager
//
// The coder → tech-lead hop already exists (the bus reactor). This adds the upward
// hops it mirrors: run tech-lead → owning quest-lead, and quest-lead → tech-manager
// → intent-manager. At the top tier (standalone-run = tech-lead, standalone-quest =
// quest-lead, campaign = intent-manager) the decider applies the smith rule: decide
// + record. It NEVER pauses for a human.

// decisionBootstrap is the CONSTANT prompt for a tier resolving a decision escalated
// from below. Like the other bootstraps it carries no context on argv — the
// escalated question rides the brief (brief_get). It must decide autonomously and
// emit ONE machine-readable DECISION line.
const decisionBootstrap = "You are a manager resolving a decision escalated up to you from a lower tier — this is a launched flow with NO human available. " +
	"Call the brief_get tool FIRST to read the escalated decision (the question and its context)." + briefGetToolHint + " " +
	"Decide it yourself using your authority: pick the best option given the context and the flow's scope; do NOT ask a human, do NOT defer, do NOT stop the flow. " +
	"Then emit EXACTLY ONE line and stop: `DECISION ` followed by JSON " +
	`{"answer":"the decision, stated so the lower tier can act on it"}` + "." + incidentDoctrine

// parseDecision extracts a decider's answer from a `DECISION <json>` line. ok is
// false when no such line is present. The last line wins.
func parseDecision(text string) (string, bool) {
	answer, ok := "", false
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "DECISION ") {
			continue
		}
		var d struct {
			Answer string `json:"answer"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "DECISION ")), &d) == nil {
			answer, ok = d.Answer, true
		}
	}
	return answer, ok
}

// escalateDecision escalates one decision one tier up: it spawns `deciderRole` (the
// settings role key + brief Role, also the recorded tier name) under the bus identity
// `deciderID` (distinct so the standalone-run top tier reuses the real "tl" agent
// rather than showing a phantom second agent), records the resolution on the host
// record (run/quest/campaign, by id-kind), and returns the Escalation plus whether it
// RESOLVED (the decider emitted an actual DECISION verdict). It NEVER pauses for a
// human: a decider that emits no verdict yields a recorded fallback with
// resolved=false, so the caller decides the terminal disposition — a real capability
// failure hits the same wall at the decider and comes back unresolved (→ terminal
// blocked); a genuine decision comes back resolved (→ the caller acts on it).
func (c *Conductor) escalateDecision(ctx context.Context, hostID, from, deciderID, deciderRole, question, workdir string, extra []string) (run.Escalation, bool) {
	model, thinking := c.agentConfig(deciderRole)
	c.putBrief(deciderID, bus.Brief{
		To:     deciderID,
		Role:   deciderRole,
		Prompt: escalationBriefPrompt(question, c.hostEscalations(hostID)),
	})
	out := streamOnce(ctx, c, hostID, deciderID, decisionBootstrap, workdir, extra, spawnOpts{model: model, thinking: thinking})
	answer, resolved := parseDecision(out.allText)
	if !resolved {
		answer = "no resolution reached at this tier (decider produced no DECISION verdict)"
	}
	esc := run.Escalation{
		From:     from,
		To:       deciderRole,
		Question: question,
		Decider:  deciderRole,
		Answer:   answer,
		At:       time.Now().UTC().Format(time.RFC3339),
	}
	c.recordEscalation(hostID, esc)
	return esc, resolved
}

// maxPriorDecisionLines bounds the PRIOR DECISIONS section of a decider's brief
// to the most recent entries (oldest dropped first).
const maxPriorDecisionLines = 10

// escalationBriefPrompt is the decider's whole brief: the escalated question
// plus the decision memory already recorded on the host record, so a later
// decider never contradicts a prior ruling unknowingly. Pure function so it is
// directly unit-testable; with no prior escalations the brief is byte-for-byte
// the question-only form.
func escalationBriefPrompt(question string, prior []run.Escalation) string {
	var b strings.Builder
	b.WriteString("ESCALATED DECISION (decide autonomously; never ask a human, never stop the flow):\n" + question)
	if len(prior) == 0 {
		return b.String()
	}
	b.WriteString("\nPRIOR DECISIONS on this record (do not contradict without stating why):\n")
	if drop := len(prior) - maxPriorDecisionLines; drop > 0 {
		fmt.Fprintf(&b, "(+%d earlier decisions)\n", drop)
		prior = prior[drop:]
	}
	for _, e := range prior {
		fmt.Fprintf(&b, "- %s → %s\n", truncate(firstLine(e.Question), 160), truncate(firstLine(e.Answer), 160))
	}
	return b.String()
}

// hostEscalations reads the escalations already recorded on the record that
// owns hostID, switching on the id-kind prefix exactly as recordEscalation
// persists them: quest (q<N>), campaign (c<N>), else run.
func (c *Conductor) hostEscalations(hostID string) []run.Escalation {
	switch {
	case strings.HasPrefix(hostID, "q"):
		if q, ok := c.GetQuest(hostID); ok {
			return q.Escalations
		}
	case strings.HasPrefix(hostID, "c"):
		if cam, ok := c.GetCampaign(hostID); ok {
			return cam.Escalations
		}
	default:
		if r, ok := c.Get(hostID); ok {
			return r.Escalations
		}
	}
	return nil
}

// decisionBlocks reports whether a decider's answer indicates the flow must NOT
// proceed (a genuine block) rather than a path forward. Best-effort keyword signal:
// absent an explicit block signal, a resolved decision is treated as "proceed" (the
// decider had authority and chose to move on). Advisory — the hard bound is
// `resolved` (whether a DECISION was emitted at all).
func decisionBlocks(answer string) bool {
	a := strings.ToLower(answer)
	for _, kw := range []string{"block", "cannot proceed", "can't proceed", "do not proceed", "don't proceed", "hard blocker", "needs a human", "no path forward"} {
		if strings.Contains(a, kw) {
			return true
		}
	}
	return false
}

// recordEscalation persists an Escalation on the record that OWNS hostID, detected
// by id-kind prefix (mirrors updateAgentHost): run (r<N>) → Run.Escalations, quest
// (q<N>) → Quest.Escalations, campaign (c<N>) → Campaign.Escalations.
func (c *Conductor) recordEscalation(hostID string, esc run.Escalation) {
	switch {
	case strings.HasPrefix(hostID, "q"):
		c.UpdateQuest(hostID, func(q *run.Quest) { q.Escalations = append(q.Escalations, esc) })
	case strings.HasPrefix(hostID, "c"):
		c.UpdateCampaign(hostID, func(cam *run.Campaign) { cam.Escalations = append(cam.Escalations, esc) })
	default:
		c.Update(hostID, func(r *run.Run) { r.Escalations = append(r.Escalations, esc) })
	}
}

// escalateRunDecision escalates a run tech-lead's decision ONE tier up: to the owning
// quest-lead when the run is a quest child, else the tech-lead is the top tier and
// decides+records itself (the smith rule). Recorded on the run record.
func (c *Conductor) escalateRunDecision(ctx context.Context, r run.Run, question, workdir string, extra []string) (run.Escalation, bool) {
	if r.QuestID != "" {
		return c.escalateDecision(ctx, r.ID, "tech-lead", questLeadID, RoleQuestLead, question, workdir, extra)
	}
	// Standalone-run top tier: the tech-lead decides itself — reuse the real "tl"
	// agent identity so it doesn't appear as a phantom second agent on the run.
	return c.escalateDecision(ctx, r.ID, "tech-lead", "tl", RoleTechLead, question, workdir, extra)
}

// escalateQuestDecision escalates a quest-lead's decision ONE tier up: to the
// campaign tech-manager when the quest is a campaign child, else the quest-lead is
// the top tier and decides+records itself. Recorded on the quest record.
func (c *Conductor) escalateQuestDecision(ctx context.Context, q run.Quest, question, workdir string, extra []string) (run.Escalation, bool) {
	if q.CampaignID != "" {
		return c.escalateDecision(ctx, q.ID, "quest-lead", techManagerID, RoleTechManager, question, workdir, extra)
	}
	return c.escalateDecision(ctx, q.ID, "quest-lead", questLeadID, RoleQuestLead, question, workdir, extra)
}

// escalateCampaignDecision escalates a tech-manager's decision ONE tier up to the
// intent-manager — the campaign top tier, which decides+records. On the campaign record.
func (c *Conductor) escalateCampaignDecision(ctx context.Context, cam run.Campaign, question, workdir string, extra []string) (run.Escalation, bool) {
	return c.escalateDecision(ctx, cam.ID, "tech-manager", intentManagerID, RoleIntentManager, question, workdir, extra)
}
