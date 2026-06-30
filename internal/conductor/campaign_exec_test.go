package conductor

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// waitForCampaign polls a campaign's persisted state until `until` holds or the
// deadline passes, mirroring waitForQuest.
func waitForCampaign(t *testing.T, c *Conductor, id string, until func(run.Campaign) bool, d time.Duration) run.Campaign {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		cam, _ := c.GetCampaign(id)
		if until(cam) {
			return cam
		}
		time.Sleep(20 * time.Millisecond)
	}
	cam, _ := c.GetCampaign(id)
	return cam
}

// campaignClaude is a scripted stub `claude` driving the whole campaign supervisor
// with no real model. Composed from the stubClaude harness (see stubclaude_test.go);
// it branches on the spawn's role (the prompt) and a per-stage fixture/counter file:
//   - the INTENT LEAD fails the brief gate ONCE (a goal that shares no terms with the
//     original input — recorded via CANDYLAND_BRIEF_FIXTURE), then on the route-back
//     emits a consistent brief with one draft task and two commitments c1/c2.
//   - the child run's TECH LEAD emits a one-task PARTITION; its CODER writes a file +
//     a green TEST; its code REVIEWER returns REVIEW_CLEAN — the existing run executor
//     then COMMITS onto the campaign branch and opens NO PR (Deliver=branch).
//   - the INTENT REVIEWER emits a per-commitment INTENT_REVIEW: c1 satisfied, and c2
//     either `missed` (blocks the PR) or `partial` (annotates only) per
//     CANDYLAND_TEST_VERDICT — the lever the oracle flips to assert both gates.
var campaignClaude = stubClaude(
	role("intent lead", `if [[ -f "$CANDYLAND_BRIEF_FIXTURE" ]]; then
  `+emitText(`INTENT_BRIEF {\"restatedGoal\":\"add csv export to the reports page\",\"scopeByDomain\":[\"backend\"],\"draftTasks\":[\"implement csv export endpoint\",\"add csv export button\"],\"commitments\":[{\"id\":\"c1\",\"statement\":\"export endpoint exists\"},{\"id\":\"c2\",\"statement\":\"export includes totals\"}]}`)+`  `+emitResult("brief", 2)+`else
  touch "$CANDYLAND_BRIEF_FIXTURE"
  `+emitText(`INTENT_BRIEF {\"restatedGoal\":\"totally unrelated nonsense\",\"commitments\":[{\"id\":\"c1\",\"statement\":\"x\"}]}`)+`  `+emitResult("brief", 1)+`fi
`),
	// The intent reviewer's verdict for c2 is interpolated from the env at run time
	// (CANDYLAND_TEST_VERDICT), so the INTENT_REVIEW line is double-escaped and echoed
	// directly rather than built from emitText.
	role("intent reviewer", `echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
echo "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"INTENT_REVIEW {\\\"verdicts\\\":[{\\\"commitmentId\\\":\\\"c1\\\",\\\"verdict\\\":\\\"satisfied\\\",\\\"evidence\\\":[\\\"endpoint added in handler.go\\\"]},{\\\"commitmentId\\\":\\\"c2\\\",\\\"verdict\\\":\\\"$CANDYLAND_TEST_VERDICT\\\",\\\"evidence\\\":[\\\"totals column not wired\\\"]}]}\"}]}}"
`+emitResult("reviewed", 1)),
	roleCleanReviewer,
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	// Each coder writes a file UNIQUE to its worktree (PID-named) so two child runs
	// sharing the campaign branch don't collide — their commits ACCUMULATE on it.
	coder(writeWorktreeFile("work_$$.txt"), emitTest(1, 0)),
)

// The ORACLE for the campaign execution layer. A scripted-stub run drives the full
// intent→delivery supervisor deterministically and asserts every gate:
//   - the BRIEF GATE fails the inconsistent first brief and routes back (bounded),
//     then passes the consistent one;
//   - the PLAN GATE passes (a child was decomposed for the commitments);
//   - the FINAL INTENT REVIEW carries a per-commitment verdict {satisfied|partial|
//     missed} with cited evidence;
//   - a `partial` verdict ANNOTATES the PR but the PR STILL OPENS (campaign done).
//
// The `missed`-blocks-the-PR half is the sibling test below.
func TestCampaignDeliversWithPartialAnnotation(t *testing.T) {
	c, repo := deliveryConductor(t, campaignClaude)
	t.Setenv("CANDYLAND_BRIEF_FIXTURE", filepath.Join(t.TempDir(), "brief-first"))
	t.Setenv("CANDYLAND_TEST_VERDICT", "partial") // c2 partial → annotate, do NOT block

	id := c.CreateCampaign(run.CampaignSpec{
		Input:         "add CSV export to the reports page",
		Folders:       []string{repo},
		AutonomyLevel: run.AutonomyUnattended,
	})
	if !c.BeginCampaign(id) {
		t.Fatal("BeginCampaign returned false for a fresh campaign")
	}

	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "done" || cam.Status == "blocked"
	}, 90*time.Second)
	if cam.Status != "done" {
		t.Fatalf("campaign did not finish: status=%q reason=%q", cam.Status, cam.PauseReason)
	}

	// BRIEF GATE: failed the first inconsistent brief, then passed on route-back.
	if !cam.BriefGate.Passed || cam.BriefGate.DecidedAt == "" {
		t.Errorf("brief gate must end passed+decided, got %+v", cam.BriefGate)
	}
	if cam.IntentBrief.RestatedGoal == "" || len(cam.IntentBrief.Commitments) != 2 {
		t.Fatalf("settled brief wrong: %+v", cam.IntentBrief)
	}

	// PLAN GATE: passed (a child run was decomposed for the commitments).
	if !cam.PlanGate.Passed || cam.PlanGate.DecidedAt == "" {
		t.Errorf("plan gate must end passed+decided, got %+v", cam.PlanGate)
	}

	// Two child runs were launched (one per draft task), each branch-delivered (no
	// child PR of its own) — their commits ACCUMULATE on the shared campaign branch.
	if len(cam.RunIDs) != 2 {
		t.Fatalf("expected two child runs (one per draft task), got %v", cam.RunIDs)
	}
	for _, rid := range cam.RunIDs {
		child, ok := c.Get(rid)
		if !ok {
			t.Fatalf("child run %q not tracked", rid)
		}
		if child.CampaignID != id {
			t.Errorf("child %s CampaignID = %q, want %q", rid, child.CampaignID, id)
		}
		if child.Deliver != run.DeliverBranch {
			t.Errorf("child %s must deliver to branch, got %q", rid, child.Deliver)
		}
		if child.PrURL != "" {
			t.Errorf("a branch-delivered child must open NO PR, got %q", child.PrURL)
		}
	}

	// INTENT REVIEW: per-commitment verdict schema {satisfied|partial|missed} + evidence.
	v := cam.IntentReview.Verdicts
	if len(v) != 2 || cam.IntentReview.ReviewedAt == "" {
		t.Fatalf("intent review must carry 2 verdicts + a timestamp, got %+v", cam.IntentReview)
	}
	byID := map[string]run.CommitmentVerdict{}
	for _, vv := range v {
		byID[vv.CommitmentID] = vv
	}
	if byID["c1"].Verdict != "satisfied" || len(byID["c1"].Evidence) == 0 {
		t.Errorf("c1 must be satisfied with cited evidence, got %+v", byID["c1"])
	}
	if byID["c2"].Verdict != "partial" || len(byID["c2"].Evidence) == 0 {
		t.Errorf("c2 must be partial with cited evidence, got %+v", byID["c2"])
	}

	// DELIVERY GATE: no `missed` → the campaign opens ONE PR per repo; the `partial`
	// annotates the PR body but did NOT block (the PR opened).
	if len(cam.PRs) != 1 || cam.PRs[0].URL == "" {
		t.Fatalf("a no-missed campaign must open one PR, got %+v", cam.PRs)
	}

	// ACCUMULATION: the two children deliver to the SHARED campaign branch, each
	// child basing its integration off the prior tip (executor_claude.go integrateRepo:
	// resolve the existing branch tip to a SHA before addWorktree's branch -D), so
	// sibling commits ACCUMULATE rather than the second clobbering the first. Assert
	// the final campaign branch carries BOTH children's distinct PID-named files —
	// the exact clobber the SHA-pinning guards against.
	branch := CampaignBranch(cam, repo)
	out := gitOut(t, repo, "ls-tree", "-r", "--name-only", branch)
	var workFiles []string
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(f, "work_") && strings.HasSuffix(f, ".txt") {
			workFiles = append(workFiles, f)
		}
	}
	if len(workFiles) != 2 {
		t.Fatalf("campaign branch %s must accumulate BOTH children's files, got %v (full tree:\n%s)", branch, workFiles, out)
	}
	// And both children's commits are reachable from the tip (the second is a
	// descendant that did not reset away the first).
	logOut := gitOut(t, repo, "log", "--name-only", "--pretty=format:", branch)
	for _, wf := range workFiles {
		if !strings.Contains(logOut, wf) {
			t.Errorf("commit adding %s is not in the campaign branch history:\n%s", wf, logOut)
		}
	}
}

// gitOut runs git in dir and returns trimmed stdout, failing the test on error.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// The `missed`-blocks-the-PR half of the oracle: a single `missed` commitment
// verdict BLOCKS that repo's PR — the campaign stays blocked with a visible reason
// and the branch persists; no PR opens.
func TestCampaignMissedCommitmentBlocksPR(t *testing.T) {
	c, repo := deliveryConductor(t, campaignClaude)
	t.Setenv("CANDYLAND_BRIEF_FIXTURE", filepath.Join(t.TempDir(), "brief-first"))
	t.Setenv("CANDYLAND_TEST_VERDICT", "missed") // c2 missed → BLOCK the PR

	id := c.CreateCampaign(run.CampaignSpec{
		Input:         "add CSV export to the reports page",
		Folders:       []string{repo},
		AutonomyLevel: run.AutonomyUnattended,
	})
	c.BeginCampaign(id)

	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "blocked" || cam.Status == "done"
	}, 90*time.Second)
	if cam.Status != "blocked" {
		t.Fatalf("a missed commitment must BLOCK delivery, got status=%q reason=%q", cam.Status, cam.PauseReason)
	}
	if cam.PauseReason == "" {
		t.Error("a blocked campaign must carry a visible reason")
	}
	// The review still ran and recorded the missed verdict (it's why delivery blocked).
	missed := false
	for _, v := range cam.IntentReview.Verdicts {
		if v.CommitmentID == "c2" && v.Verdict == "missed" {
			missed = true
		}
	}
	if !missed {
		t.Errorf("the blocking missed verdict must be recorded, got %+v", cam.IntentReview.Verdicts)
	}
	// No PR opened (a missed verdict blocks it); the branch persists for resume.
	for _, pr := range cam.PRs {
		if pr.URL != "" {
			t.Errorf("no PR may open when a commitment is missed, got %q", pr.URL)
		}
	}
}

// briefGate / planGate are deterministic checks; pin their contract so a change to
// the gate logic is a deliberate, test-visible edit. (The agent doctrine is composed
// via kb_get in the prompts; the gates themselves are mechanical consistency checks.)
func TestCampaignGates(t *testing.T) {
	input := "add CSV export to the reports page"
	// Brief gate: missing goal / no commitments / drifted goal all fail; a consistent
	// brief with checkable commitments passes.
	if _, ok := briefGate(input, run.IntentBrief{Commitments: []run.Commitment{{Statement: "x"}}}); ok {
		t.Error("a brief with no restated goal must fail the gate")
	}
	if _, ok := briefGate(input, run.IntentBrief{RestatedGoal: "add csv export"}); ok {
		t.Error("a brief with no commitments must fail the gate")
	}
	if _, ok := briefGate(input, run.IntentBrief{RestatedGoal: "unrelated nonsense", Commitments: []run.Commitment{{Statement: "x"}}}); ok {
		t.Error("a brief whose goal shares no terms with the input must fail the gate")
	}
	if _, ok := briefGate(input, run.IntentBrief{RestatedGoal: "add csv export to reports", Commitments: []run.Commitment{{Statement: "endpoint exists"}}}); !ok {
		t.Error("a consistent brief with a checkable commitment must pass the gate")
	}

	// Plan gate: zero children, or a brief with no commitments, fails; otherwise passes.
	brief := run.IntentBrief{Commitments: []run.Commitment{{ID: "c1", Statement: "x"}}}
	if _, ok := planGate(brief, nil); ok {
		t.Error("zero decomposed children must fail the plan gate")
	}
	if _, ok := planGate(run.IntentBrief{}, []childPrompt{{title: "t"}}); ok {
		t.Error("a brief with no commitments must fail the plan gate")
	}
	if _, ok := planGate(brief, []childPrompt{{title: "t"}}); !ok {
		t.Error("a child decomposed for a commitment must pass the plan gate")
	}
}

// parseIntentBrief / parseIntentReview are the fenced agent-verdict conventions
// (INTENT_BRIEF / INTENT_REVIEW lines), mirroring parseWorkItems/parseReview. Pin
// them so the contract the stub and a real agent share can't drift silently.
func TestParseCampaignVerdicts(t *testing.T) {
	brief, ok := parseIntentBrief(`preamble
INTENT_BRIEF {"restatedGoal":"g","commitments":[{"id":"c1","statement":"s"}]}`)
	if !ok || brief.RestatedGoal != "g" || len(brief.Commitments) != 1 {
		t.Fatalf("INTENT_BRIEF must parse: ok=%v brief=%+v", ok, brief)
	}
	if _, ok := parseIntentBrief("no verdict here"); ok {
		t.Error("text with no INTENT_BRIEF line must report ok=false (never a silent pass)")
	}

	review, ok := parseIntentReview(`INTENT_REVIEW {"verdicts":[{"commitmentId":"c1","verdict":"satisfied","evidence":["e"]},{"commitmentId":"c2","verdict":"missed","evidence":["m"]}]}`)
	if !ok || len(review.Verdicts) != 2 {
		t.Fatalf("INTENT_REVIEW must parse: ok=%v review=%+v", ok, review)
	}
	if review.Verdicts[1].Verdict != "missed" || len(review.Verdicts[1].Evidence) != 1 {
		t.Errorf("verdict schema not parsed: %+v", review.Verdicts[1])
	}
	if _, ok := parseIntentReview("no verdict"); ok {
		t.Error("text with no INTENT_REVIEW line must report ok=false")
	}
}
