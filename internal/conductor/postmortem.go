package conductor

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/benitogf/candyland/internal/run"
)

// postmortem.go implements the E2 rule (contract §3): a terminal `blocked` — a
// capability failure, the "last breath" of §1 — is INVALID without a schema-valid
// Postmortem. A block write missing any field is incomplete → rejected and bounced
// back to the agent (the reject-and-loop-back path). The conductor is itself an
// agent tier: when the failing agent literally cannot answer (e.g. claude could not
// start), the conductor SYNTHESISES a schema-valid postmortem from the attempt data
// it already holds, so a blocker is always fully explained, never a bare stop.

// validatePostmortem reports whether a postmortem carries ALL §3 fields. A missing
// field means the postmortem is incomplete (the block is rejected). The reason names
// the first missing field so the bounce-back tells the agent exactly what to add.
func validatePostmortem(pm *run.Postmortem) (bool, string) {
	if pm == nil {
		return false, "postmortem is required for a terminal blocked (capability failure) — none provided"
	}
	if len(pm.Attempts) == 0 {
		return false, "postmortem missing: attempts"
	}
	switch {
	case strings.TrimSpace(pm.FailingCapability) == "":
		return false, "postmortem missing: failingCapability"
	case strings.TrimSpace(pm.Evidence) == "":
		return false, "postmortem missing: evidence"
	case strings.TrimSpace(pm.RootCauseSoFar) == "":
		return false, "postmortem missing: rootCauseSoFar"
	case strings.TrimSpace(pm.HumanUnblockAction) == "":
		return false, "postmortem missing: humanUnblockAction"
	case strings.TrimSpace(pm.PartialWorkState) == "":
		return false, "postmortem missing: partialWorkState"
	}
	return true, ""
}

// parsePostmortem extracts an agent-emitted postmortem from a `POSTMORTEM <json>`
// verdict line (the fenced convention, like PARTITION/REVIEW_FINDINGS). ok is false
// when no such line is present. The last line wins. A parsed-but-incomplete
// postmortem still returns ok=true — validity is a separate check so the caller can
// bounce it back with the specific missing field.
func parsePostmortem(text string) (*run.Postmortem, bool) {
	var pm *run.Postmortem
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "POSTMORTEM ") {
			continue
		}
		var p run.Postmortem
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "POSTMORTEM ")), &p) == nil {
			pm = &p
		}
	}
	return pm, pm != nil
}

// synthPostmortem builds a schema-valid postmortem from the attempt data the
// conductor already holds, for the capability failures where the agent cannot
// itself answer (its process could not start, or it failed every attempt). This is
// the conductor applying the smith rule at its own tier — a real explanation, not a
// placeholder.
func synthPostmortem(agentID, failingCapability, evidence string, attempts int, partial string) *run.Postmortem {
	attemptLines := make([]string, 0, attempts)
	for i := 1; i <= attempts; i++ {
		attemptLines = append(attemptLines, fmt.Sprintf("attempt %d/%d: %s", i, attempts, failingCapability))
	}
	if len(attemptLines) == 0 {
		attemptLines = []string{"1/1: " + failingCapability}
	}
	return &run.Postmortem{
		Attempts:           attemptLines,
		FailingCapability:  failingCapability,
		Evidence:           orDefault(strings.TrimSpace(evidence), failingCapability),
		RootCauseSoFar:     "a capability outside the repo failed for agent " + agentID + "; no decision could resolve it (§1)",
		HumanUnblockAction: "restore the failing capability (see failingCapability/evidence), then start a new run",
		PartialWorkState:   orDefault(strings.TrimSpace(partial), "no partial work landed for "+agentID),
	}
}

// attachRunPostmortem persists a postmortem on a run's record ONLY when it is
// schema-valid (§3). It returns false — rejecting the write — when the postmortem
// is incomplete, so the caller loops back (bounded) to obtain a valid one. This is
// the gate a `blocked` write must pass.
func (c *Conductor) attachRunPostmortem(id string, pm *run.Postmortem) bool {
	if ok, _ := validatePostmortem(pm); !ok {
		return false
	}
	c.Update(id, func(r *run.Run) { r.Postmortem = pm })
	return true
}

// blockerPostmortemFor obtains a schema-valid postmortem for a capability blocker,
// reusing the existing bounded retry as the reject-and-loop-back path: it first
// tries the agent's own emitted POSTMORTEM line (parsed from agentText); if that is
// absent or incomplete, the conductor synthesises one from the attempt data (the
// agent may be unable to answer — e.g. its process could not start). The returned
// postmortem is always schema-valid.
func (c *Conductor) blockerPostmortemFor(agentID, agentText, failingCapability, evidence string, attempts int, partial string) *run.Postmortem {
	if pm, ok := parsePostmortem(agentText); ok {
		if valid, _ := validatePostmortem(pm); valid {
			return pm
		}
		// Incomplete agent postmortem — rejected; fall through to the synthesised one.
	}
	return synthPostmortem(agentID, failingCapability, evidence, attempts, partial)
}

// attachQuestPostmortem persists a schema-valid postmortem on a quest's record — the
// E2 invariant for a blocked quest. It synthesises one from the block reason via
// blockerPostmortemFor (guaranteeing all six §3 fields), so a quest never writes a
// terminal `blocked` with a nil postmortem. Returns false only if the quest is unknown.
func (c *Conductor) attachQuestPostmortem(id, agentID, failingCapability, evidence string) bool {
	pm := c.blockerPostmortemFor(agentID, "", failingCapability, evidence, 1, "quest "+id)
	if ok, _ := validatePostmortem(pm); !ok {
		return false
	}
	return c.UpdateQuest(id, func(q *run.Quest) { q.Postmortem = pm })
}
