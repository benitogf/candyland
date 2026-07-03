package conductor

import (
	"context"
	"encoding/json"
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
	"Call the brief_get tool FIRST to read the escalated decision (the question and its context). " +
	"Decide it yourself using your authority: pick the best option given the context and the flow's scope; do NOT ask a human, do NOT defer, do NOT stop the flow. " +
	"Then emit EXACTLY ONE line and stop: `DECISION ` followed by JSON " +
	`{"answer":"the decision, stated so the lower tier can act on it"}` + "."

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

// escalateDecision escalates one decision one tier up to `decider` (also the settings
// role key and bus identity), spawns that tier to decide it, records the resolution
// on the host record (run/quest/campaign, by id-kind), and returns the Escalation.
// It NEVER pauses for a human: a decider that emits no DECISION verdict still yields
// a recorded autonomous fallback (the smith rule), so the flow always proceeds.
func (c *Conductor) escalateDecision(ctx context.Context, hostID, from, decider, question, workdir string, extra []string) run.Escalation {
	model, thinking := c.agentConfig(decider)
	c.putBrief(decider, bus.Brief{
		To:     decider,
		Role:   decider,
		Prompt: "ESCALATED DECISION (decide autonomously; never ask a human, never stop the flow):\n" + question,
	})
	out := streamOnce(ctx, c, hostID, decider, decisionBootstrap, workdir, extra, spawnOpts{model: model, thinking: thinking})
	answer, ok := parseDecision(out.allText)
	if !ok {
		// Top-tier smith fallback: decide autonomously and record it (never a human).
		answer = "decided autonomously: proceed with the safest in-scope option and record the assumption"
	}
	esc := run.Escalation{
		From:     from,
		To:       decider,
		Question: question,
		Decider:  decider,
		Answer:   answer,
		At:       time.Now().UTC().Format(time.RFC3339),
	}
	c.recordEscalation(hostID, esc)
	return esc
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
func (c *Conductor) escalateRunDecision(ctx context.Context, r run.Run, question, workdir string, extra []string) run.Escalation {
	if r.QuestID != "" {
		return c.escalateDecision(ctx, r.ID, "tech-lead", RoleQuestLead, question, workdir, extra)
	}
	return c.escalateDecision(ctx, r.ID, "tech-lead", RoleTechLead, question, workdir, extra)
}

// escalateQuestDecision escalates a quest-lead's decision ONE tier up: to the
// campaign tech-manager when the quest is a campaign child, else the quest-lead is
// the top tier and decides+records itself. Recorded on the quest record.
func (c *Conductor) escalateQuestDecision(ctx context.Context, q run.Quest, question, workdir string, extra []string) run.Escalation {
	if q.CampaignID != "" {
		return c.escalateDecision(ctx, q.ID, "quest-lead", RoleTechManager, question, workdir, extra)
	}
	return c.escalateDecision(ctx, q.ID, "quest-lead", RoleQuestLead, question, workdir, extra)
}

// escalateCampaignDecision escalates a tech-manager's decision ONE tier up to the
// intent-manager — the campaign top tier, which decides+records. On the campaign record.
func (c *Conductor) escalateCampaignDecision(ctx context.Context, cam run.Campaign, question, workdir string, extra []string) run.Escalation {
	return c.escalateDecision(ctx, cam.ID, "tech-manager", RoleIntentManager, question, workdir, extra)
}
