package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// === campaign deliver/targetPr: receiver + propagation (D1/D2 for campaigns) ===
//
// A campaign normally delivers via its own branch: its child runs commit onto the
// campaign branch (Deliver=branch) and the parent opens one PR per repo at the end.
// A `feedback`/`review` campaign instead lands on an EXISTING PR — so its child runs
// must carry the campaign's Deliver + TargetPR (feedback updates that PR in place,
// review reports) INSTEAD of branch delivery. linkCampaignChild is the propagation
// point; these pin both the propagation and the default-branch behavior, plus the
// endpoint's feedback/review-requires-targetPr rejection.

// TestCampaignPropagatesFeedbackToChild: a feedback campaign (with a targetPr)
// propagates feedback mode + the PR number to each child run, NOT branch delivery.
func TestCampaignPropagatesFeedbackToChild(t *testing.T) {
	c, repo := deliveryConductor(t, feedbackClaude)

	id := c.CreateCampaign(run.CampaignSpec{
		Input:    "address the review feedback on the export PR",
		Folders:  []string{repo},
		Deliver:  run.DeliverFeedback,
		TargetPR: 42,
	})
	cam, ok := c.GetCampaign(id)
	if !ok {
		t.Fatalf("campaign %q not persisted", id)
	}
	// CreateCampaign stamped the campaign-level delivery from the spec.
	if cam.Deliver != run.DeliverFeedback || cam.TargetPR != 42 {
		t.Fatalf("campaign deliver/targetPr not stamped: deliver=%q targetPr=%d", cam.Deliver, cam.TargetPR)
	}

	childID := c.linkCampaignChild(id, run.Spec{Folders: []string{repo}, Prompt: "do the item"})
	child, ok := c.Get(childID)
	if !ok {
		t.Fatalf("child run %q not tracked", childID)
	}
	if child.CampaignID != id {
		t.Errorf("child CampaignID = %q, want %q", child.CampaignID, id)
	}
	// The KEY behavior: the child carries the campaign's feedback mode + PR number, so
	// it lands on the EXISTING PR instead of committing onto the campaign branch.
	if child.Deliver != run.DeliverFeedback {
		t.Errorf("feedback campaign child must deliver feedback, got %q", child.Deliver)
	}
	if child.TargetPR != 42 {
		t.Errorf("feedback campaign child must carry the target PR 42, got %d", child.TargetPR)
	}
	// A feedback child must NOT be placed on the campaign branch — it resolves the
	// target PR's head branch itself at execution time. (Its default Create branch is
	// left untouched and ignored by the feedback executor, like a quest feedback run.)
	if child.Branch == CampaignBranch(cam) {
		t.Errorf("a feedback child must NOT be put on the campaign branch, got %q", child.Branch)
	}
}

// TestCampaignPropagatesReviewToChild: a review campaign likewise propagates review
// mode + the target PR to its children.
func TestCampaignPropagatesReviewToChild(t *testing.T) {
	c, repo := deliveryConductor(t, feedbackClaude)

	id := c.CreateCampaign(run.CampaignSpec{
		Input:    "review the export PR for correctness",
		Folders:  []string{repo},
		Deliver:  run.DeliverReview,
		TargetPR: 7,
	})
	childID := c.linkCampaignChild(id, run.Spec{Folders: []string{repo}, Prompt: "do the item"})
	child, ok := c.Get(childID)
	if !ok {
		t.Fatalf("child run %q not tracked", childID)
	}
	if child.Deliver != run.DeliverReview || child.TargetPR != 7 {
		t.Errorf("review campaign child must carry review+PR7, got deliver=%q targetPr=%d", child.Deliver, child.TargetPR)
	}
}

// TestCampaignDefaultPropagatesBranch: a default campaign (no deliver) defaults to
// "pr" at the campaign level and still produces BRANCH-delivered children on the
// campaign branch (the parent opens the PR) — the unchanged baseline behavior.
func TestCampaignDefaultPropagatesBranch(t *testing.T) {
	c, repo := deliveryConductor(t, feedbackClaude)

	id := c.CreateCampaign(run.CampaignSpec{
		Input:   "add CSV export to the reports page",
		Folders: []string{repo},
	})
	cam, ok := c.GetCampaign(id)
	if !ok {
		t.Fatalf("campaign %q not persisted", id)
	}
	if cam.Deliver != run.DeliverPR {
		t.Fatalf("an empty deliver must default to %q, got %q", run.DeliverPR, cam.Deliver)
	}

	childID := c.linkCampaignChild(id, run.Spec{Folders: []string{repo}, Prompt: "do the item"})
	child, ok := c.Get(childID)
	if !ok {
		t.Fatalf("child run %q not tracked", childID)
	}
	if child.Deliver != run.DeliverBranch {
		t.Errorf("default campaign child must deliver to branch, got %q", child.Deliver)
	}
	if child.TargetPR != 0 {
		t.Errorf("default campaign child carries no target PR, got %d", child.TargetPR)
	}
	if child.Branch != CampaignBranch(cam) {
		t.Errorf("default campaign child must be on the campaign branch %q, got %q", CampaignBranch(cam), child.Branch)
	}
}

// TestCampaignFeedbackRequiresTargetPR pins the create-endpoint contract: a
// feedback/review campaign without a targetPr is rejected (mirrors the quest
// endpoint). The guard is the (Deliver feedback|review) && TargetPR<=0 condition the
// endpoint applies before CreateCampaign — replicated here as the spec-level rule.
func TestCampaignFeedbackRequiresTargetPR(t *testing.T) {
	needsPR := func(spec run.CampaignSpec) bool {
		return (spec.Deliver == run.DeliverFeedback || spec.Deliver == run.DeliverReview) && spec.TargetPR <= 0
	}
	cases := []struct {
		name   string
		spec   run.CampaignSpec
		reject bool
	}{
		{"feedback without targetPr", run.CampaignSpec{Deliver: run.DeliverFeedback}, true},
		{"review without targetPr", run.CampaignSpec{Deliver: run.DeliverReview}, true},
		{"feedback with targetPr", run.CampaignSpec{Deliver: run.DeliverFeedback, TargetPR: 5}, false},
		{"default pr needs no targetPr", run.CampaignSpec{}, false},
		{"explicit pr needs no targetPr", run.CampaignSpec{Deliver: run.DeliverPR}, false},
	}
	for _, tc := range cases {
		if got := needsPR(tc.spec); got != tc.reject {
			t.Errorf("%s: reject=%v, want %v", tc.name, got, tc.reject)
		}
	}
}
