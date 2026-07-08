package conductor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// allSkipQuestClaude: the quest lead surfaces items but triages EVERY one "skip"
// (no accepted work), driving the F1 production skip path — no hand-set ItemsSkipped.
var allSkipQuestClaude = stubClaude(
	role("quest lead", emitText(`WORKITEMS [{\"title\":\"risky refactor\",\"evidence\":\"out of scope\",\"classification\":\"cleanup\",\"decision\":\"skip\"},{\"title\":\"noisy rename\",\"evidence\":\"not safe\",\"classification\":\"cleanup\",\"decision\":\"skip\"}]`)+emitResult("done", 1)),
	coder(emitResult("noop", 0)),
)

// nothingFoundQuestClaude: the quest lead surfaces nothing at all (WORKITEMS_NONE).
var nothingFoundQuestClaude = stubClaude(
	role("quest lead", emitText("WORKITEMS_NONE")+emitResult("none", 1)),
	coder(emitResult("noop", 0)),
)

// F1 (the 2026-07-03 scar): an all-skip quest must record its skip TRIAGE decisions
// as "skipped" WorkItems (the ledger writer PR #30 removed) and terminate
// surfaced-only with a no-op summary — NEVER plain "done". This drives the PRODUCTION
// skip path (the quest-lead emits all-skip WORKITEMS), not a hand-set ItemsSkipped.
func TestQuestAllSkipTerminatesSurfacedOnly(t *testing.T) {
	c, repo := deliveryConductor(t, allSkipQuestClaude)
	id := c.CreateQuest(run.QuestSpec{Objective: "keep it tidy", Folders: []string{repo}})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false")
	}
	q := waitForQuest(t, c, id, func(q run.Quest) bool {
		return q.Status == "surfaced-only" || q.Status == "done"
	}, 60*time.Second)
	if q.Status != "surfaced-only" {
		t.Fatalf("an all-skip quest must terminate surfaced-only, got %q (summary %q)", q.Status, q.Summary)
	}
	if q.ItemsSkipped != 2 {
		t.Errorf("ItemsSkipped = %d, want 2 (both skip-triaged items entered the ledger)", q.ItemsSkipped)
	}
	skipped := 0
	for _, w := range q.WorkItems {
		if w.Disposition == "skipped" {
			skipped++
		}
	}
	if skipped != 2 {
		t.Errorf("ledger skipped WorkItems = %d, want 2 (F1 restored writer)", skipped)
	}
	if !containsFold(q.Summary, "surfaced-only") || !containsFold(q.Summary, "0 executed") {
		t.Errorf("terminal summary must account the no-op, got %q", q.Summary)
	}
}

// F1: a genuinely-nothing-found quest terminates "done" but stamps an EXPLICIT
// non-empty terminal Summary rather than an undifferentiated empty one.
func TestQuestNothingFoundStampsSummary(t *testing.T) {
	c, repo := deliveryConductor(t, nothingFoundQuestClaude)
	id := c.CreateQuest(run.QuestSpec{Objective: "keep it tidy", Folders: []string{repo}})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false")
	}
	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "done" }, 60*time.Second)
	if q.Status != "done" {
		t.Fatalf("a nothing-found quest terminates done, got %q", q.Status)
	}
	if !containsFold(q.Summary, "nothing to do") {
		t.Errorf("a nothing-found quest must stamp an explicit terminal summary, got %q", q.Summary)
	}
}

// Q2 carve-out: a branch-delivered quest (campaign-owned, Deliver=branch) that
// COMPLETED its items with prsOpened:0 is legitimately done — its delivery is the
// branch commit, not a PR. It must NOT be flagged surfaced-only.
func TestQuestBranchDeliveryNotSurfacedOnly(t *testing.T) {
	q := &run.Quest{
		Deliver:        run.DeliverBranch,
		CampaignID:     "c1",
		ItemsCompleted: 2,
		PRsOpened:      0, // branch delivery opens no PR — legitimate
	}
	if questIsNoOp(q) {
		t.Error("a branch-delivered quest with completed items + 0 PRs must NOT be a no-op")
	}
	if st := questTerminalStatus(q); st != "done" {
		t.Errorf("branch-delivered completed quest terminal status = %q, want done", st)
	}

	// A real zero-delivery quest IS a no-op.
	noop := &run.Quest{ItemsSkipped: 3}
	if !questIsNoOp(noop) {
		t.Error("a quest with 0 executed, 0 PRs and surfaced/skipped items is a no-op")
	}
	if st := questTerminalStatus(noop); st != "surfaced-only" {
		t.Errorf("zero-delivery quest terminal status = %q, want surfaced-only", st)
	}
}

// c-terminal-fidelity: a converge quest that COMPLETED work but whose terminal
// per-repo PR delivery errored outright (every PR recorded an Err, none opened)
// must terminate "delivery-failed" — never a misleading "done". Partial delivery
// (one PR opened) is a real success and stays "done".
func TestQuestDeliveryFailureNeverDone(t *testing.T) {
	failed := &run.Quest{
		Deliver:        run.DeliverBranch, // carve-out would say "not a no-op"; delivery failure still wins
		ItemsCompleted: 2,
		PRs:            []run.PR{{Repo: "candyland", Err: "push failed: no origin"}},
	}
	if !questDeliveryFailed(failed) {
		t.Fatal("a quest whose only PR errored must be flagged delivery-failed")
	}
	if st := questTerminalStatus(failed); st != "delivery-failed" {
		t.Errorf("terminal status = %q, want delivery-failed", st)
	}
	if s := questTerminalSummary(failed); !containsFold(s, "delivery-failed") || !containsFold(s, "push failed") {
		t.Errorf("summary must account the delivery failure, got %q", s)
	}

	// Partial delivery — one repo opened, one errored — is a real (partial) success.
	partial := &run.Quest{
		ItemsCompleted: 2,
		PRs:            []run.PR{{Repo: "a", URL: "http://x/pull/1"}, {Repo: "b", Err: "push failed"}},
	}
	if questDeliveryFailed(partial) {
		t.Error("a partial delivery (one PR opened) is not a delivery failure")
	}
	if st := questTerminalStatus(partial); st != "done" {
		t.Errorf("partial-delivery terminal status = %q, want done", st)
	}

	// A quest that opened no terminal PRs by design (no q.PRs) is not delivery-failed.
	if questDeliveryFailed(&run.Quest{ItemsCompleted: 1}) {
		t.Error("a quest with no terminal PRs recorded must not be delivery-failed")
	}
}

// O3: a campaign's child run is linked BOTH ways right at launch — the child
// carries CampaignID, and the parent's RunIDs lists the child — so the rollup is
// never empty while the campaign runs.
func TestCampaignChildLinkedBothWaysAtLaunch(t *testing.T) {
	c, _ := newCampaignServer(t)
	camID := c.CreateCampaign(run.CampaignSpec{Input: "x", Folders: []string{"/repo"}})

	childID := c.linkCampaignChild(camID, run.Spec{Folders: []string{"/repo"}, Prompt: "do a task", Title: "task"})

	child, ok := c.Get(childID)
	if !ok {
		t.Fatalf("child run %q not tracked", childID)
	}
	if child.CampaignID != camID {
		t.Errorf("child CampaignID = %q, want %q (parent link not stamped at launch)", child.CampaignID, camID)
	}
	if child.Deliver != run.DeliverBranch {
		t.Errorf("campaign child Deliver = %q, want branch", child.Deliver)
	}
	cam, _ := c.GetCampaign(camID)
	found := false
	for _, rid := range cam.RunIDs {
		if rid == childID {
			found = true
		}
	}
	if !found {
		t.Errorf("parent campaign RunIDs = %v, must list child %q at launch", cam.RunIDs, childID)
	}
}

// O5: a standalone perFinding (adventure) quest child run serializes deliver:"pr"
// (present, not omitted) so the frontend can key UI on r.deliver. Empty/omitted
// would break the UI. (A converge quest's child delivers "branch" — see
// TestQuestConvergeOpensOnePRAtTerminal.)
func TestStandaloneChildSerializesDeliverPR(t *testing.T) {
	c, _ := newQuestServer(t)
	childID := c.linkQuestChild(run.Quest{ID: "q1", Folders: []string{"/repo"}, Convergence: run.ConvergePerFinding}, run.Spec{Folders: []string{"/repo"}, Prompt: "p", Title: "t"})
	child, ok := c.Get(childID)
	if !ok {
		t.Fatalf("child %q not tracked", childID)
	}
	if child.Deliver != run.DeliverPR {
		t.Errorf("standalone quest child Deliver = %q, want %q", child.Deliver, run.DeliverPR)
	}
	if child.QuestID != "q1" {
		t.Errorf("child QuestID = %q, want q1", child.QuestID)
	}
	// And it must SERIALIZE the field (no omitempty) — present even when "pr".
	js := marshalRun(t, child)
	if !containsFold(js, `"deliver":"pr"`) {
		t.Errorf("run JSON must serialize deliver:\"pr\", got %s", js)
	}
}

func marshalRun(t *testing.T, r run.Run) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && indexFold(s, sub) >= 0
}

func indexFold(s, sub string) int {
	ls, lsub := toLowerASCII(s), toLowerASCII(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return i
		}
	}
	return -1
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// A targeted review/feedback quest must actually review its PR before it can
// terminate: when discovery surfaces nothing and no prior tick reviewed the PR,
// seedReviewItem yields the review itself as a work item (so a child run runs
// against the PR). Once a tick has launched a run, the review already ran and a
// later empty discovery is a legitimate stop, not a re-seed.
func TestSeedReviewItem(t *testing.T) {
	review := run.Quest{Deliver: run.DeliverReview, TargetPR: 712}
	it, ok := seedReviewItem(review)
	if !ok {
		t.Fatal("a review quest with a target PR and no prior review must seed the PR review")
	}
	if it.Classification != reviewClassification || it.Decision != "do" {
		t.Errorf("seeded item must be a do-decision pr-review, got %+v", it)
	}
	if !containsFold(it.Title, "712") {
		t.Errorf("seeded review title must name the target PR, got %q", it.Title)
	}
	if !it.seeded {
		t.Error("the seeded item must carry the conductor-only seeded flag")
	}

	// Feedback delivery seeds too, with feedback wording.
	fb, ok := seedReviewItem(run.Quest{Deliver: run.DeliverFeedback, TargetPR: 5})
	if !ok || !containsFold(fb.Title, "feedback") {
		t.Errorf("a feedback quest must seed a feedback review, got ok=%v item=%+v", ok, fb)
	}

	// Once a tick launched a run, the PR was reviewed — no re-seed.
	reviewed := run.Quest{Deliver: run.DeliverReview, TargetPR: 712, Ticks: []run.Tick{{LaunchedRunIDs: []string{"r9"}}}}
	if _, ok := seedReviewItem(reviewed); ok {
		t.Error("a quest that already launched a review run must not re-seed")
	}

	// A non-targeted quest (no PR, or a build quest) never seeds a PR review.
	if _, ok := seedReviewItem(run.Quest{Deliver: run.DeliverReview}); ok {
		t.Error("a review quest with no target PR must not seed")
	}
	if _, ok := seedReviewItem(run.Quest{Deliver: run.DeliverPR, TargetPR: 712}); ok {
		t.Error("a normal (non review/feedback) quest must not seed a PR review")
	}
}

// A review quest's terminal summary must name the PR it reviewed and never claim a
// clean "no actionable findings" in a way that hides whether a review ran: after a
// review item completes it reports the PR + completed count; only a genuinely
// reviewed-with-nothing quest reports "no actionable findings" (and still names the PR).
func TestReviewQuestTerminalSummaryNamesPR(t *testing.T) {
	ran := &run.Quest{Deliver: run.DeliverReview, TargetPR: 712, ItemsCompleted: 1}
	if s := questTerminalSummary(ran); !containsFold(s, "712") || !containsFold(s, "review") {
		t.Errorf("a completed review must name the PR, got %q", s)
	}
	none := &run.Quest{Deliver: run.DeliverReview, TargetPR: 712}
	if s := questTerminalSummary(none); !containsFold(s, "712") {
		t.Errorf("even a no-finding review must name the PR, got %q", s)
	}
}

// finishQuest is idempotent at the PR layer: if the quest's shared branch already
// has an open PR (a re-finish after a resume), it reuses that PR's URL instead of
// erroring on a duplicate `gh pr create` or recording a delivery failure.
func TestFinishQuestReusesExistingOpenPR(t *testing.T) {
	c, repo := deliveryConductor(t, stubClaude())
	// gh stub: `pr list --json url` reports an already-open PR; anything else prints a URL.
	gh := filepath.Join(t.TempDir(), "gh")
	script := "#!/usr/bin/env bash\n" +
		"if [[ \"$*\" == *defaultBranchRef* ]]; then echo 'main'; exit 0; fi\n" +
		"if [[ \"$*\" == *'pr list'* ]]; then echo '[{\"url\":\"https://github.com/example/repo/pull/42\"}]'; exit 0; fi\n" +
		"echo 'https://github.com/example/repo/pull/99'\n"
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_GH", gh)

	id := c.CreateQuest(run.QuestSpec{Objective: "do it", Folders: []string{repo}})
	if _, err := git(context.Background(), repo, "branch", "quest/"+id); err != nil {
		t.Fatalf("seed quest branch: %v", err)
	}
	c.UpdateQuest(id, func(q *run.Quest) {
		q.Status = "running"
		q.WorkItems = append(q.WorkItems, run.WorkItem{ID: "t1-w0", SourceTick: "t1", Disposition: "completed"})
		recomputeQuestRollups(q)
	})
	c.finishQuest(context.Background(), id)

	q, _ := c.GetQuest(id)
	if len(q.PRs) != 1 || q.PRs[0].URL != "https://github.com/example/repo/pull/42" {
		t.Fatalf("finishQuest must reuse the existing open PR URL, got %+v", q.PRs)
	}
	if q.PRs[0].Err != "" {
		t.Errorf("a reused PR carries no error, got %q", q.PRs[0].Err)
	}
	if q.Status == "delivery-failed" {
		t.Errorf("reusing an existing PR is not a delivery failure (status=%q)", q.Status)
	}
}
