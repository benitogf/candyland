package conductor

import (
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// acceptanceClaude drives a full clean delivery (tech lead → coder → clean review →
// clean fix pass). It is used with a prompt that carries an `## Acceptance` section so
// the pre-`done` acceptance pass runs on the integrated branch.
var acceptanceClaude = stubClaude(
	roleCleanReviewer,
	role("review findings", coderFixWrite),
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

// coderFixWrite is a fix-pass body that makes a real edit (so fixReviewFindings
// commits), letting the acceptance re-run be the decider.
const coderFixWrite = "echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Edit\",\"input\":{\"file\":\"a.txt\"}}]}}'\n" +
	"printf 'fix attempt\\n' >> a.txt\n" +
	"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"fixed\",\"usage\":{\"output_tokens\":1}}'\n"

// #63: a run whose `## Acceptance` section carries a command that FAILS on the
// integrated branch must NOT reach a clean `done` — the failing check folds into the
// bounded remediation, re-runs, and (still failing) terminates the run blocked.
func TestAcceptanceFailureBlocksDelivery(t *testing.T) {
	c, _ := deliveryConductor(t, acceptanceClaude)
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "3")
	prompt := "do the thing\n\n## Acceptance\n```sh\nfalse\n```\n"
	id := c.Create(run.Spec{Prompt: prompt})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool {
		return r.Status == "blocked" || r.Status == "done" || r.Status == "delivery-failed"
	}, 60*time.Second)
	if r.Status == "done" {
		t.Fatalf("a failing acceptance check must not reach clean done (error=%q)", r.Error)
	}
	if r.Status != "blocked" {
		t.Fatalf("a failing acceptance check must terminate blocked, got %q (error=%q)", r.Status, r.Error)
	}
	if r.PrURL != "" {
		t.Errorf("a failing acceptance check must not open a PR, got %q", r.PrURL)
	}
}

// #63 regression: a run whose prompt has NO runnable acceptance section delivers
// exactly as before (clean done + PR opened).
func TestNoAcceptanceSectionDeliversAsBefore(t *testing.T) {
	c, _ := deliveryConductor(t, acceptanceClaude)
	id := c.Create(run.Spec{Prompt: "do the thing (no acceptance section here)"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("a run with no acceptance section must deliver clean, got status=%q error=%q", r.Status, r.Error)
	}
	if r.PrURL == "" {
		t.Error("a clean run with no acceptance section must open a PR")
	}
}
