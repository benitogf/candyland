package conductor

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// reportOnlyClaude surfaces ONE work item on the first tick (recorded via the
// fixture marker) and then nothing — but the quest runs at L1 (report-only), so
// the loop launches no child run: the item is recorded SKIPPED and the drive
// finishes after a single surfacing pass. This is the zero-delivery no-op the
// Q2 surfaced-only terminal state and the Q4 mismatch guard key on.
var reportOnlyClaude = stubClaude(
	role("quest lead", `if [[ -f "$CANDYLAND_QUEST_FIXTURE" ]]; then
  `+emitText("WORKITEMS_NONE")+`  `+emitResult("WORKITEMS_NONE", 1)+`else
  touch "$CANDYLAND_QUEST_FIXTURE"
  `+emitText(`WORKITEMS [{\"title\":\"add the export endpoint\",\"evidence\":\"missing\",\"classification\":\"feature\",\"decision\":\"do\"}]`)+`  `+emitResult("done", 2)+`fi
`),
	coder(emitResult("noop", 1)),
)

// Q2: an L1 (report-only) quest that surfaces work but executes nothing and opens
// no PR must reach a DISTINCT terminal state — surfaced-only, NOT the plain "done"
// of a quest that shipped. Its Summary must name the no-op accounting.
func TestQuestReportOnlyReachesSurfacedOnly(t *testing.T) {
	c, repo := deliveryConductor(t, reportOnlyClaude)
	t.Setenv("CANDYLAND_QUEST_FIXTURE", filepath.Join(t.TempDir(), "first-tick"))

	id := c.CreateQuest(run.QuestSpec{
		Objective:     "implement the CSV export endpoint",
		Folders:       []string{repo},
		AutonomyLevel: run.AutonomyReportOnly,
	})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false for a fresh quest")
	}

	q := waitForQuest(t, c, id, func(q run.Quest) bool {
		return q.Status == "surfaced-only" || q.Status == "done"
	}, 60*time.Second)
	if q.Status != "surfaced-only" {
		t.Fatalf("a report-only zero-delivery quest must terminate surfaced-only, got %q (summary=%q)", q.Status, q.Summary)
	}
	if q.Summary == "" {
		t.Error("surfaced-only must carry a Summary naming the no-op (N surfaced, 0 executed, 0 PRs)")
	}
	if q.ItemsCompleted != 0 || q.PRsOpened != 0 {
		t.Errorf("expected zero delivery, got completed=%d prs=%d", q.ItemsCompleted, q.PRsOpened)
	}
	if q.ItemsSkipped == 0 {
		t.Error("the surfaced item should be recorded skipped")
	}

	// A surfaced-only quest is terminal — it cannot be begun/resumed again.
	if c.BeginQuest(id) {
		t.Error("a surfaced-only quest must not be begin-able (terminal)")
	}
}

// Q2 carve-out: a branch-delivered quest (campaign-owned, Deliver=branch) that
// COMPLETED its items with prsOpened:0 is legitimately done — its delivery is the
// branch commit, not a PR. It must NOT be flagged surfaced-only.
func TestQuestBranchDeliveryNotSurfacedOnly(t *testing.T) {
	q := &run.Quest{
		Deliver:        run.DeliverBranch,
		CampaignID:     "c1",
		AutonomyLevel:  run.AutonomyUnattended,
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
	noop := &run.Quest{AutonomyLevel: run.AutonomyReportOnly, ItemsSkipped: 3}
	if !questIsNoOp(noop) {
		t.Error("a quest with 0 executed, 0 PRs and surfaced/skipped items is a no-op")
	}
	if st := questTerminalStatus(noop); st != "surfaced-only" {
		t.Errorf("zero-delivery quest terminal status = %q, want surfaced-only", st)
	}
}

// Q4: an L1 (report-only) quest whose objective IMPLIES execution, with a
// 100%-skip first-and-only tick, is a strong misconfig signal — the Summary must
// WARN about the intent↔autonomy mismatch rather than finish silently green.
func TestQuestIntentAutonomyMismatchWarns(t *testing.T) {
	c, repo := deliveryConductor(t, reportOnlyClaude)
	t.Setenv("CANDYLAND_QUEST_FIXTURE", filepath.Join(t.TempDir(), "first-tick"))

	id := c.CreateQuest(run.QuestSpec{
		Objective:     "implement and add the CSV export endpoint", // execute-intent
		Folders:       []string{repo},
		AutonomyLevel: run.AutonomyReportOnly, // but report-only — mismatch
	})
	c.BeginQuest(id)

	q := waitForQuest(t, c, id, func(q run.Quest) bool {
		return q.Status == "surfaced-only" || q.Status == "done"
	}, 60*time.Second)
	if q.Summary == "" || !containsFold(q.Summary, "mismatch") {
		t.Errorf("an execute-objective L1 no-op must warn about the intent/autonomy mismatch, summary=%q", q.Summary)
	}
}

// objectiveImpliesExecution is the detection the Q4 guard keys on (separate from
// the terminal-state computation).
func TestObjectiveImpliesExecution(t *testing.T) {
	for _, obj := range []string{"implement the endpoint", "add a button", "fix the bug", "refactor X"} {
		if !objectiveImpliesExecution(obj) {
			t.Errorf("%q should imply execution", obj)
		}
	}
	for _, obj := range []string{"review the code for issues", "audit the dependencies", "report on test coverage"} {
		if objectiveImpliesExecution(obj) {
			t.Errorf("%q should NOT imply execution", obj)
		}
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

// O5: a standalone quest child run serializes deliver:"pr" (present, not omitted)
// so the frontend can key UI on r.deliver. Empty/omitted would break the UI.
func TestStandaloneChildSerializesDeliverPR(t *testing.T) {
	c, _ := newQuestServer(t)
	childID := c.linkQuestChild(run.Quest{ID: "q1", Folders: []string{"/repo"}}, run.Spec{Folders: []string{"/repo"}, Prompt: "p", Title: "t"})
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
	if !isSeededReview(it) {
		t.Error("the seeded item must be recognized as the seeded review (launches even at L1)")
	}
	// An agent-authored item that merely reuses the classification string must NOT be
	// treated as the seeded review — only the conductor's `seeded` flag counts.
	if isSeededReview(questWorkItem{Classification: reviewClassification}) {
		t.Error("an agent item with the pr-review classification must not impersonate the seeded review")
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
