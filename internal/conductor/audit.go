package conductor

import (
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// writeAudit derives a queryable audit record from a run's final state and
// stores it at ooo key audits/<id> (reusing ko/ooo — no new store), then offers
// it to the central-server sink seam. Called from Execute once the run reaches a
// terminal status ("done", with Error set on a failure) — a paused/stopped run
// is not audited (it isn't complete). Nil-guarded like publish so the serverless
// test conductor is unaffected. Per-task pass/fail come from the agents' test
// events (the t:"test" stream emissions parsed from each coder's `TEST {json}`).
func (c *Conductor) writeAudit(id string) {
	if c.server == nil {
		return
	}
	r, ok := c.Get(id)
	if !ok {
		return
	}
	audit := run.Audit{
		RunID:  id,
		Status: r.Status,
		Phase:  r.Phase,
		Tokens: r.TokensUsed,
		PrURL:  r.PrURL,
		Error:  r.Error,
		// Non-nil so a run that failed before partitioning (no tasks) serializes
		// as "tasks":[] not "tasks":null — the UI reads this shape with no
		// client-side null guard (see run/types.go), matching how the conductor
		// keeps Run.Agents/Tasks non-nil for the same reason.
		Tasks:   make([]run.TaskAudit, 0, len(r.Tasks)),
		EndedAt: time.Now().UTC().Format(time.RFC3339),
	}
	for _, t := range r.Tasks {
		ta := run.TaskAudit{ID: t.ID, State: t.State}
		for _, a := range r.Agents {
			if a.ID != t.ID {
				continue
			}
			for _, ev := range a.Events {
				if ev.T == "test" {
					ta.Pass += ev.Pass
					ta.Fail += ev.Fail
				}
			}
		}
		audit.Tasks = append(audit.Tasks, ta)
	}

	data, err := json.Marshal(audit)
	if err != nil {
		return
	}
	if _, err := c.server.Storage.Set("audits/"+id, data); err != nil {
		return
	}
	c.postAudit(audit)
}

// postAudit is the central-server sync seam: the audit is always kept locally
// (audits/<id> in ooo); when CANDYLAND_AUDIT_SINK names a central analytics
// server, a future build POSTs the record there for cross-run results analysis.
// Built local-first — a no-op until a sink is configured, so nothing depends on
// a central server existing.
func (c *Conductor) postAudit(a run.Audit) {
	sink := os.Getenv("CANDYLAND_AUDIT_SINK")
	if sink == "" {
		return // local-first: no central sink configured
	}
	// Seam: POST `a` (JSON) to sink. Intentionally a one-function boundary —
	// central analytics sync is a separate, future deliverable. Until it exists,
	// surface the configured-but-inert sink so an operator who set the env var
	// isn't left wondering why nothing arrives (the audit is kept locally).
	log.Printf("candyland: CANDYLAND_AUDIT_SINK=%q is set but the central audit sink is not implemented yet; audit %s kept locally only", sink, a.RunID)
}
