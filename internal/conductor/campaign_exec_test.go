package conductor

import (
	"context"
	"os"
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

// --- reusable campaign stub fragments (the two managers + the child quest pipeline) ---

// campIntentLead fails the brief gate ONCE (a goal that shares no terms with the
// original input — recorded via CANDYLAND_BRIEF_FIXTURE), then on the route-back
// emits a consistent brief with one draft task and two commitments c1/c2.
var campIntentLead = role("intent lead", `if [[ -f "$CANDYLAND_BRIEF_FIXTURE" ]]; then
  `+emitText(`INTENT_BRIEF {\"restatedGoal\":\"add csv export to the reports page\",\"scopeByDomain\":[\"backend\"],\"draftTasks\":[\"implement csv export endpoint\"],\"commitments\":[{\"id\":\"c1\",\"statement\":\"export endpoint exists\"},{\"id\":\"c2\",\"statement\":\"export includes totals\"}]}`)+`  `+emitResult("brief", 2)+`else
  touch "$CANDYLAND_BRIEF_FIXTURE"
  `+emitText(`INTENT_BRIEF {\"restatedGoal\":\"totally unrelated nonsense\",\"commitments\":[{\"id\":\"c1\",\"statement\":\"x\"}]}`)+`  `+emitResult("brief", 1)+`fi
`)

// campQuestLead surfaces ONE work item per tick. Every child quest runs with
// CANDYLAND_QUEST_MAX_TICKS=1 (set by setCampaignFixtures), so each quest does exactly
// one tick — surface → launch a child run → the drive pauses at the tick bound — which
// works uniformly for the initial and the remediation quests (no shared tick marker).
// It appends the cwd to CANDYLAND_ORDER_LOG so a deps test can assert ordering, and
// sleeps briefly so a concurrency test has a window to observe overlap.
var campQuestLead = role("quest lead", `[[ -n "$CANDYLAND_ORDER_LOG" ]] && echo "$(pwd)" >> "$CANDYLAND_ORDER_LOG"
sleep 0.3
`+emitText(`WORKITEMS [{\"title\":\"do the campaign item\",\"evidence\":\"needed for the goal\",\"classification\":\"feature\",\"decision\":\"do\"}]`)+`  `+emitResult("work", 2))

// campIntentReviewer emits a per-commitment INTENT_REVIEW: c1 satisfied, and c2's
// verdict from CANDYLAND_TEST_VERDICT (partial → annotate; missed → remediate/block).
var campIntentReviewer = role("intent reviewer", `echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
echo "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"INTENT_REVIEW {\\\"verdicts\\\":[{\\\"commitmentId\\\":\\\"c1\\\",\\\"verdict\\\":\\\"satisfied\\\",\\\"evidence\\\":[\\\"endpoint added in handler.go\\\"]},{\\\"commitmentId\\\":\\\"c2\\\",\\\"verdict\\\":\\\"$CANDYLAND_TEST_VERDICT\\\",\\\"evidence\\\":[\\\"totals column not wired\\\"]}]}\"}]}}"
`+emitResult("reviewed", 1))

// campTechDone always confirms technical done (gate 2a) — the intent review is the
// lever the oracles flip.
var campTechDone = role("technical sign-off", emitTechDone(true, "integrated green on the campaign branch"))

// campChildPipeline is the run-level pipeline every child quest drives: a tech lead
// PARTITION, a coder that writes a PID-named file + a green TEST, and a clean reviewer.
var campChildTechLead = role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`))
var campChildCoder = coder(writeWorktreeFile("work_$$.txt"), emitTest(1, 0))

// campaignClaude drives the whole two-manager campaign supervisor with no real model:
// the tech manager emits a ONE-quest QUESTS partition, the intent manager AGREES at
// gate 1, the child quest drives its discover→run→review→branch pipeline, and gate 2
// dual sign-off (tech done + intent review) decides delivery.
var campaignClaude = stubClaude(
	campIntentLead,
	role("intent manager", emitPartitionReview(true, "the quest covers both commitments")),
	campIntentReviewer,
	campTechDone,
	role("tech manager", emitQuestsLine(`[{"id":"q1","title":"csv export","objective":"implement csv export end to end","folders":[],"deps":[]}]`)),
	campQuestLead,
	roleCleanReviewer,
	campChildTechLead,
	campChildCoder,
)

// setCampaignFixtures sets the per-test marker/verdict env the campaign stubs read.
func setCampaignFixtures(t *testing.T, c2Verdict string) {
	t.Helper()
	t.Setenv("CANDYLAND_BRIEF_FIXTURE", filepath.Join(t.TempDir(), "brief-first"))
	t.Setenv("CANDYLAND_QUEST_MAX_TICKS", "1") // one surface→launch tick per child quest
	if c2Verdict != "" {
		t.Setenv("CANDYLAND_TEST_VERDICT", c2Verdict)
	}
}

// The ORACLE for the campaign execution layer: a scripted-stub run drives the full
// two-manager intent→delivery supervisor deterministically and asserts every stage:
//   - the BRIEF GATE fails the inconsistent first brief and routes back, then passes;
//   - the tech manager decomposes into a child QUEST (not a bare run) via QUESTS parse;
//   - GATE 1 (the intent manager) agrees the partition covers the commitments;
//   - GATE 2 dual sign-off runs (tech done + per-commitment intent review);
//   - a `partial` verdict ANNOTATES the PR but the PR STILL OPENS (campaign done).
func TestCampaignDecomposesIntoQuestAndDelivers(t *testing.T) {
	c, repo := deliveryConductor(t, campaignClaude)
	setCampaignFixtures(t, "partial") // c2 partial → annotate, do NOT block

	id := c.CreateCampaign(run.CampaignSpec{
		Input:   "add CSV export to the reports page",
		Folders: []string{repo},
	})
	if !c.BeginCampaign(id) {
		t.Fatal("BeginCampaign returned false for a fresh campaign")
	}

	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "done" || cam.Status == "blocked"
	}, 120*time.Second)
	if cam.Status != "done" {
		t.Fatalf("campaign did not finish: status=%q reason=%q", cam.Status, cam.PauseReason)
	}

	// BRIEF GATE: failed the first inconsistent brief, then passed on route-back.
	if !cam.BriefGate.Passed || cam.BriefGate.DecidedAt == "" {
		t.Errorf("brief gate must end passed+decided, got %+v", cam.BriefGate)
	}
	// GATE 1 (recorded on PlanGate): the intent manager agreed the partition.
	if !cam.PlanGate.Passed || cam.PlanGate.DecidedAt == "" {
		t.Errorf("gate 1 must end passed+decided, got %+v", cam.PlanGate)
	}

	// DECOMPOSED INTO A CHILD QUEST (not a bare run).
	if len(cam.QuestIDs) != 1 {
		t.Fatalf("campaign must decompose into one child QUEST, got questIds=%v runIds=%v", cam.QuestIDs, cam.RunIDs)
	}
	child, ok := c.GetQuest(cam.QuestIDs[0])
	if !ok {
		t.Fatalf("child quest %q not tracked", cam.QuestIDs[0])
	}
	if child.CampaignID != id {
		t.Errorf("child quest CampaignID = %q, want %q", child.CampaignID, id)
	}
	if child.Title != "csv export" {
		t.Errorf("child quest title must come from the QUESTS emission, got %q", child.Title)
	}
	// The child quest integrates onto the CAMPAIGN branch and opens NO PR of its own.
	if QuestBranch(child) != CampaignBranch(cam) {
		t.Errorf("child quest must integrate onto the campaign branch %q, got %q", CampaignBranch(cam), QuestBranch(child))
	}
	if len(child.PRs) != 0 {
		t.Errorf("a campaign-child quest must open NO PR of its own, got %+v", child.PRs)
	}

	// GATE 2: per-commitment verdicts recorded {satisfied|partial|missed} + evidence.
	byID := map[string]run.CommitmentVerdict{}
	for _, vv := range cam.IntentReview.Verdicts {
		byID[vv.CommitmentID] = vv
	}
	if byID["c1"].Verdict != "satisfied" || byID["c2"].Verdict != "partial" {
		t.Errorf("gate 2 verdicts wrong: %+v", cam.IntentReview.Verdicts)
	}

	// DELIVERY: no `missed` and tech done → ONE PR per repo; the `partial` annotated it.
	if len(cam.PRs) != 1 || cam.PRs[0].URL == "" {
		t.Fatalf("a clean-gate-2 campaign must open one PR, got %+v", cam.PRs)
	}
	// The child quest's work landed on the campaign branch.
	branch := CampaignBranch(cam)
	out := gitOut(t, repo, "ls-tree", "-r", "--name-only", branch)
	if !strings.Contains(out, "work_") {
		t.Errorf("campaign branch %s must carry the child quest's work file, tree:\n%s", branch, out)
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

// The `missed`-blocks-the-PR half of the oracle: a `missed` commitment that cannot be
// remediated (the reviewer keeps returning missed) BLOCKS delivery after the bounded
// remediation rounds — the campaign stays blocked with a visible reason, no PR opens.
func TestCampaignMissedCommitmentBlocksPR(t *testing.T) {
	c, repo := deliveryConductor(t, campaignClaude)
	setCampaignFixtures(t, "missed") // c2 always missed → block after remediation bound
	t.Setenv("CANDYLAND_CAMPAIGN_REMEDIATION_ROUNDS", "1")

	id := c.CreateCampaign(run.CampaignSpec{
		Input:   "add CSV export to the reports page",
		Folders: []string{repo},
	})
	c.BeginCampaign(id)

	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "blocked" || cam.Status == "done"
	}, 120*time.Second)
	if cam.Status != "blocked" {
		t.Fatalf("an un-remediable missed commitment must BLOCK delivery, got status=%q reason=%q", cam.Status, cam.PauseReason)
	}
	if cam.PauseReason == "" {
		t.Error("a blocked campaign must carry a visible reason")
	}
	missed := false
	for _, v := range cam.IntentReview.Verdicts {
		if v.CommitmentID == "c2" && v.Verdict == "missed" {
			missed = true
		}
	}
	if !missed {
		t.Errorf("the blocking missed verdict must be recorded, got %+v", cam.IntentReview.Verdicts)
	}
	for _, pr := range cam.PRs {
		if pr.URL != "" {
			t.Errorf("no PR may open when a commitment is missed, got %q", pr.URL)
		}
	}
}

// campaignRemediationClaude flips c2 from `missed` (first review) to `satisfied` (the
// re-review after remediation) via CANDYLAND_REVIEW_FIXTURE. It proves a campaign does
// NOT park in "blocked" on the first miss — the tech manager spawns a remediation
// QUEST targeting the missed commitment, re-reviews, and delivers.
var campaignRemediationClaude = stubClaude(
	campIntentLead,
	role("intent manager", emitPartitionReview(true, "the quest covers both commitments")),
	role("intent reviewer", `if [[ -f "$CANDYLAND_REVIEW_FIXTURE" ]]; then verdict=satisfied; else verdict=missed; touch "$CANDYLAND_REVIEW_FIXTURE"; fi
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
echo "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"INTENT_REVIEW {\\\"verdicts\\\":[{\\\"commitmentId\\\":\\\"c1\\\",\\\"verdict\\\":\\\"satisfied\\\",\\\"evidence\\\":[\\\"endpoint added\\\"]},{\\\"commitmentId\\\":\\\"c2\\\",\\\"verdict\\\":\\\"$verdict\\\",\\\"evidence\\\":[\\\"totals column\\\"]}]}\"}]}}"
`+emitResult("reviewed", 1)),
	campTechDone,
	role("tech manager", emitQuestsLine(`[{"id":"q1","title":"csv export","objective":"implement csv export","folders":[],"deps":[]}]`)),
	campQuestLead,
	roleCleanReviewer,
	campChildTechLead,
	campChildCoder,
)

// A missed commitment must NOT immediately park the campaign in "blocked": gate 2
// spawns a remediation QUEST for the missed commitment, re-reviews, and — once the
// re-review clears — delivers the PR.
func TestCampaignRemediatesMissedThenDelivers(t *testing.T) {
	c, repo := deliveryConductor(t, campaignRemediationClaude)
	setCampaignFixtures(t, "")
	t.Setenv("CANDYLAND_REVIEW_FIXTURE", filepath.Join(t.TempDir(), "review-first"))

	id := c.CreateCampaign(run.CampaignSpec{
		Input:   "add CSV export to the reports page",
		Folders: []string{repo},
	})
	c.BeginCampaign(id)

	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "done" || cam.Status == "blocked"
	}, 150*time.Second)
	if cam.Status != "done" {
		t.Fatalf("campaign must remediate the missed commitment and deliver, got status=%q reason=%q", cam.Status, cam.PauseReason)
	}
	// An initial child quest PLUS a remediation quest for the missed c2.
	if len(cam.QuestIDs) < 2 {
		t.Fatalf("expected an initial child quest + a remediation quest, got questIds=%v", cam.QuestIDs)
	}
	c2 := ""
	for _, v := range cam.IntentReview.Verdicts {
		if v.CommitmentID == "c2" {
			c2 = v.Verdict
		}
	}
	if c2 != "satisfied" {
		t.Errorf("final review must show c2 satisfied after remediation, got %q", c2)
	}
	note := false
	for _, n := range cam.Notes {
		if strings.Contains(n, "remediation round") {
			note = true
		}
	}
	if !note {
		t.Errorf("a remediation round note must be recorded, got notes=%v", cam.Notes)
	}
	if len(cam.PRs) != 1 || cam.PRs[0].URL == "" {
		t.Errorf("a remediated campaign must open its PR, got %+v", cam.PRs)
	}
}

// campaignGate1Claude makes the intent manager REJECT the tech manager's partition
// ONCE (via CANDYLAND_GATE1_FIXTURE) then AGREE, and counts the tech-manager spawns in
// CANDYLAND_TECHMGR_LOG. It proves gate 1 loops back to the tech manager (bounded by
// maxPartitionAttempts) before work launches.
var campaignGate1Claude = stubClaude(
	campIntentLead,
	role("intent manager", `if [[ -f "$CANDYLAND_GATE1_FIXTURE" ]]; then
  `+emitPartitionReview(true, "now the partition covers both commitments")+`else
  touch "$CANDYLAND_GATE1_FIXTURE"
  `+emitPartitionReview(false, "the partition misses commitment c2")+`fi
`),
	campIntentReviewer,
	campTechDone,
	role("tech manager", `echo "spawn" >> "$CANDYLAND_TECHMGR_LOG"
`+emitQuestsLine(`[{"id":"q1","title":"csv export","objective":"implement csv export","folders":[],"deps":[]}]`)),
	campQuestLead,
	roleCleanReviewer,
	campChildTechLead,
	campChildCoder,
)

// GATE 1 loop-back: the intent manager rejects the first partition, the tech manager
// re-emits, the intent manager agrees, and the campaign proceeds — bounded by
// maxPartitionAttempts (default 2, so exactly one re-emit).
func TestCampaignGate1LoopsBackThenProceeds(t *testing.T) {
	c, repo := deliveryConductor(t, campaignGate1Claude)
	setCampaignFixtures(t, "partial")
	t.Setenv("CANDYLAND_GATE1_FIXTURE", filepath.Join(t.TempDir(), "gate1-first"))
	techLog := filepath.Join(t.TempDir(), "techmgr.log")
	t.Setenv("CANDYLAND_TECHMGR_LOG", techLog)

	id := c.CreateCampaign(run.CampaignSpec{
		Input:   "add CSV export to the reports page",
		Folders: []string{repo},
	})
	c.BeginCampaign(id)

	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "done" || cam.Status == "blocked"
	}, 120*time.Second)
	if cam.Status != "done" {
		t.Fatalf("campaign must converge after a gate-1 loop-back and deliver, got status=%q reason=%q", cam.Status, cam.PauseReason)
	}
	if !cam.PlanGate.Passed {
		t.Errorf("gate 1 must end passed after the loop-back, got %+v", cam.PlanGate)
	}
	// The tech manager was spawned TWICE for the partition (initial + one re-emit) —
	// proof gate 1 routed back rather than launching the rejected partition.
	spawns := strings.Count(gitReadFile(t, techLog), "spawn")
	if spawns < 2 {
		t.Errorf("gate 1 must loop back to the tech manager (>=2 partition spawns), got %d", spawns)
	}
}

// campaignGate1MalformedClaude makes the tech manager emit an INVALID QUESTS line
// (two quests sharing the id "q1") on EVERY attempt, logging each spawn to
// CANDYLAND_TECHMGR_LOG. parseQuests rejects the colliding ids with a reason, so
// emitQuests routes it back as retryFeedback — never reaching the intent manager
// (gate 1's reviewer). Because the partition is malformed on every attempt, the
// convergence loop exhausts maxPartitionAttempts and blocks HONESTLY rather than
// looping forever or proceeding with zero quests.
var campaignGate1MalformedClaude = stubClaude(
	campIntentLead,
	role("intent manager", emitPartitionReview(true, "should never be consulted on a malformed partition")),
	campIntentReviewer,
	campTechDone,
	role("tech manager", `echo "spawn" >> "$CANDYLAND_TECHMGR_LOG"
`+emitQuestsLine(`[{"id":"q1","title":"a","objective":"do a","folders":[],"deps":[]},{"id":"q1","title":"b","objective":"do b","folders":[],"deps":[]}]`)),
	campQuestLead,
	roleCleanReviewer,
	campChildTechLead,
	campChildCoder,
)

// GATE 1 malformed-partition exhaustion: a tech manager that emits a colliding-id
// (invalid) QUESTS partition on EVERY attempt must cause partitionUntilGated to
// exhaust maxPartitionAttempts and block the campaign honestly — a "failed gate 1"
// block, NOT an infinite loop and NOT silently proceeding with zero quests.
func TestCampaignGate1MalformedPartitionExhaustsAndBlocks(t *testing.T) {
	c, repo := deliveryConductor(t, campaignGate1MalformedClaude)
	setCampaignFixtures(t, "partial")
	t.Setenv("CANDYLAND_CAMPAIGN_PARTITION_ATTEMPTS", "2") // pin the bound for determinism
	techLog := filepath.Join(t.TempDir(), "techmgr.log")
	t.Setenv("CANDYLAND_TECHMGR_LOG", techLog)

	id := c.CreateCampaign(run.CampaignSpec{
		Input:   "add CSV export to the reports page",
		Folders: []string{repo},
	})
	c.BeginCampaign(id)

	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "done" || cam.Status == "blocked"
	}, 120*time.Second)

	if cam.Status != "blocked" {
		t.Fatalf("a partition malformed on every attempt must block the campaign, got status=%q reason=%q", cam.Status, cam.PauseReason)
	}
	reason := strings.ToLower(cam.PauseReason)
	if !strings.Contains(reason, "gate 1") || !strings.Contains(reason, "partition") {
		t.Errorf("block reason must name the failed gate-1 partition convergence, got %q", cam.PauseReason)
	}
	if cam.PlanGate.Passed {
		t.Errorf("gate 1 must not be recorded as passed on an exhausted partition, got %+v", cam.PlanGate)
	}
	// No child quests may launch when gate 1 never converges.
	if len(cam.QuestIDs) != 0 {
		t.Errorf("no child quests may launch on a failed gate 1, got %v", cam.QuestIDs)
	}
	// The loop is BOUNDED: the tech manager is spawned exactly maxPartitionAttempts
	// times for the partition (no infinite retry). The malformed partition is
	// rejected before the intent manager is ever consulted, so every spawn is a
	// tech-manager re-emission attempt.
	spawns := strings.Count(gitReadFile(t, techLog), "spawn")
	if spawns != maxPartitionAttempts() {
		t.Errorf("gate 1 must bound the tech-manager to exactly %d partition attempts, got %d", maxPartitionAttempts(), spawns)
	}
}

// campaignConcurrencyClaude has the tech manager emit TWO INDEPENDENT quests, each
// scoped to its own repo (folders), so they run CONCURRENTLY.
var campaignConcurrencyClaude = stubClaude(
	campIntentLead,
	role("intent manager", emitPartitionReview(true, "both quests are needed")),
	campIntentReviewer,
	campTechDone,
	role("tech manager", emitQuestsLine(`[{"id":"q1","title":"alpha work","objective":"do alpha","folders":["alpha"],"deps":[]},{"id":"q2","title":"beta work","objective":"do beta","folders":["beta"],"deps":[]}]`)),
	campQuestLead,
	roleCleanReviewer,
	campChildTechLead,
	campChildCoder,
)

// perRepoConductor is multiRepoConductor with a folders override that routes each run
// to its OWN spec folders (so a child quest in repo "alpha" doesn't span "beta").
func perRepoConductor(t *testing.T, script string, names ...string) (*Conductor, []string) {
	c, repos := multiRepoConductor(t, script, names...)
	writeFakeGh(t)
	c.folders = func(r run.Run) ([]string, error) {
		if len(r.Folders) > 0 {
			return r.Folders, nil
		}
		return repos, nil
	}
	return c, repos
}

// GATE-launched child quests with NO deps between them run CONCURRENTLY: at some
// moment both child quests are "running" at once.
func TestCampaignRunsIndependentQuestsConcurrently(t *testing.T) {
	c, repos := perRepoConductor(t, campaignConcurrencyClaude, "alpha", "beta")
	setCampaignFixtures(t, "partial")

	id := c.CreateCampaign(run.CampaignSpec{
		Input:   "add CSV export across alpha and beta",
		Folders: repos,
	})
	c.BeginCampaign(id)

	// Poll for a moment where >=2 of this campaign's quests are running at once.
	sawConcurrent := false
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		running := 0
		for _, q := range c.CampaignChildQuests(id) {
			if q.Status == "running" {
				running++
			}
		}
		if running >= 2 {
			sawConcurrent = true
			break
		}
		if cam, _ := c.GetCampaign(id); cam.Status == "done" || cam.Status == "blocked" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawConcurrent {
		t.Error("independent child quests must run concurrently (never observed 2 running at once)")
	}
	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "done" || cam.Status == "blocked"
	}, 120*time.Second)
	if len(cam.QuestIDs) != 2 {
		t.Errorf("expected two child quests, got %v", cam.QuestIDs)
	}
}

// campaignDepsClaude emits TWO quests where q2 depends on q1, so q1 must finish before
// q2 starts. Each quest-lead appends its cwd to CANDYLAND_ORDER_LOG on its work tick.
var campaignDepsClaude = stubClaude(
	campIntentLead,
	role("intent manager", emitPartitionReview(true, "the ordering is sound")),
	campIntentReviewer,
	campTechDone,
	role("tech manager", emitQuestsLine(`[{"id":"q1","title":"alpha first","objective":"do alpha first","folders":["alpha"],"deps":[]},{"id":"q2","title":"beta second","objective":"do beta after alpha","folders":["beta"],"deps":["q1"]}]`)),
	campQuestLead,
	roleCleanReviewer,
	campChildTechLead,
	campChildCoder,
)

// A declared dependency SEQUENCES the dependent quest behind its dependency: q1 (in
// alpha) always reaches its work tick before q2 (in beta).
func TestCampaignDepsSequenceQuests(t *testing.T) {
	c, repos := perRepoConductor(t, campaignDepsClaude, "alpha", "beta")
	setCampaignFixtures(t, "partial")
	orderLog := filepath.Join(t.TempDir(), "order.log")
	t.Setenv("CANDYLAND_ORDER_LOG", orderLog)

	id := c.CreateCampaign(run.CampaignSpec{
		Input:   "add CSV export across alpha and beta",
		Folders: repos,
	})
	c.BeginCampaign(id)

	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "done" || cam.Status == "blocked"
	}, 150*time.Second)
	if cam.Status != "done" {
		t.Fatalf("dep campaign did not finish: status=%q reason=%q", cam.Status, cam.PauseReason)
	}
	order := gitReadFile(t, orderLog)
	iAlpha := strings.Index(order, "alpha")
	iBeta := strings.Index(order, "beta")
	if iAlpha < 0 || iBeta < 0 {
		t.Fatalf("both quests must have run (order log: %q)", order)
	}
	if iAlpha > iBeta {
		t.Errorf("q1 (alpha) must reach its work tick before q2 (beta) — deps not respected. order log:\n%s", order)
	}
}

// gitReadFile reads a file written by the stubs (best-effort; empty if absent).
func gitReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// briefGate is a deterministic check; pin its contract so a change to the gate logic
// is a deliberate, test-visible edit. (Gate 1/2 doctrine is composed via kb_get in the
// agent prompts; the brief gate itself is a mechanical consistency check.)
func TestCampaignBriefGate(t *testing.T) {
	input := "add CSV export to the reports page"
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
}

// parseIntentBrief / parseIntentReview / parseQuests / parsePartitionVerdict /
// parseTechDone are the fenced agent-verdict conventions. Pin them so the contract the
// stub and a real agent share can't drift silently.
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
	if !ok || len(review.Verdicts) != 2 || review.Verdicts[1].Verdict != "missed" {
		t.Fatalf("INTENT_REVIEW must parse: ok=%v review=%+v", ok, review)
	}

	quests, ok, reason := parseQuests(`QUESTS [{"id":"q1","title":"t","objective":"o","folders":["web"],"deps":["q0"]}]`)
	if !ok || reason != "" || len(quests) != 1 || quests[0].ID != "q1" || quests[0].Deps[0] != "q0" {
		t.Fatalf("QUESTS must parse: ok=%v reason=%q quests=%+v", ok, reason, quests)
	}
	if _, ok, reason := parseQuests("no verdict"); ok || reason != "" {
		t.Errorf("text with no QUESTS line must report ok=false with no reason, got ok=%v reason=%q", ok, reason)
	}

	pv, ok := parsePartitionVerdict(`PARTITION_REVIEW {"agree":false,"reason":"gap"}`)
	if !ok || pv.Agree || pv.Reason != "gap" {
		t.Fatalf("PARTITION_REVIEW must parse: ok=%v pv=%+v", ok, pv)
	}

	td, ok := parseTechDone(`TECH_DONE {"done":true,"reason":"green"}`)
	if !ok || !td.Done || td.Reason != "green" {
		t.Fatalf("TECH_DONE must parse: ok=%v td=%+v", ok, td)
	}
}

// sanitizeDeps drops unknown dep ids and breaks cycles (clearing all deps) so the
// dependency waits can never deadlock.
func TestSanitizeDeps(t *testing.T) {
	// Unknown dep id dropped.
	out := sanitizeDeps(nil, "", []questPartitionItem{
		{ID: "a"},
		{ID: "b", Deps: []string{"a", "ghost"}},
	})
	if len(out[1].Deps) != 1 || out[1].Deps[0] != "a" {
		t.Errorf("unknown dep must be dropped, got %v", out[1].Deps)
	}
	// A cycle clears all deps.
	cyc := sanitizeDeps(nil, "", []questPartitionItem{
		{ID: "a", Deps: []string{"b"}},
		{ID: "b", Deps: []string{"a"}},
	})
	for _, q := range cyc {
		if len(q.Deps) != 0 {
			t.Errorf("a dep cycle must be broken (deps cleared), got %q deps=%v", q.ID, q.Deps)
		}
	}
}

// TestParseQuestsRejectsCollidingIDs pins the doneCh-keying invariant: parseQuests must
// reject a partition whose quest ids are empty or duplicated, since executeChildQuests
// builds one done-channel per id and closes it exactly once — a collision would panic
// the whole sidecar with "close of closed channel". A rejection carries a reason so the
// gate-1 loop can re-request a clean partition (ok=false but reason!="").
func TestParseQuestsRejectsCollidingIDs(t *testing.T) {
	// A duplicate id is rejected with a reason (recoverable → loop back).
	if q, ok, reason := parseQuests(`QUESTS [{"id":"q1","title":"a"},{"id":"q1","title":"b"}]`); ok || reason == "" {
		t.Errorf("duplicate id must be rejected with a reason, got ok=%v reason=%q quests=%+v", ok, reason, q)
	}
	// An empty id is rejected with a reason.
	if q, ok, reason := parseQuests(`QUESTS [{"id":"q1","title":"a"},{"id":"","title":"b"}]`); ok || reason == "" {
		t.Errorf("empty id must be rejected with a reason, got ok=%v reason=%q quests=%+v", ok, reason, q)
	}
	// A whitespace-only id is treated as empty.
	if q, ok, reason := parseQuests(`QUESTS [{"id":"  ","title":"a"}]`); ok || reason == "" {
		t.Errorf("whitespace-only id must be rejected, got ok=%v reason=%q quests=%+v", ok, reason, q)
	}
	// A clean, unique partition still passes with no reason.
	if q, ok, reason := parseQuests(`QUESTS [{"id":"q1","title":"a"},{"id":"q2","title":"b"}]`); !ok || reason != "" || len(q) != 2 {
		t.Errorf("a clean partition must pass, got ok=%v reason=%q quests=%+v", ok, reason, q)
	}
}

// TestExecuteChildQuestsNoPanicOnDuplicateIDs is the belt-and-suspenders guard: even if
// a colliding-id partition ever reaches executeChildQuests (bypassing parseQuests), the
// per-id dedup must ensure exactly one goroutine owns (and closes) each done-channel, so
// the -race run does NOT hit "close of closed channel". A cancelled context makes every
// goroutine close its channel and return before launching any real quest, which is
// precisely the window where an unguarded double-close would panic.
func TestExecuteChildQuestsNoPanicOnDuplicateIDs(t *testing.T) {
	c, repo := deliveryConductor(t, "")
	id := c.CreateCampaign(run.CampaignSpec{Input: "x", Folders: []string{repo}})
	cam, ok := c.GetCampaign(id)
	if !ok {
		t.Fatalf("campaign %s not found", id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled up front: goroutines close-and-return, never launch a real quest
	// Deliberately malformed: two "dup" ids and an empty id. Without the dedup guard
	// three goroutines would close two channels → panic.
	quests := []questPartitionItem{
		{ID: "dup", Title: "a"},
		{ID: "dup", Title: "b"},
		{ID: "", Title: "c"},
	}
	if got := c.executeChildQuests(ctx, id, cam, []string{repo}, quests, true); got {
		t.Errorf("executeChildQuests on a cancelled ctx must report false, got true")
	}
}

// relaunchReuseClaude drives the DELIVERY stages of a campaign (gate 2 + child
// pipeline) but wires the two PRE-LAUNCH managers as TRIPWIRES: if the intent
// lead or tech manager is spawned, it touches a marker file. A relaunch that
// reuses the persisted brief + partition must never spawn either — the markers
// must stay absent.
var relaunchReuseClaude = stubClaude(
	role("intent lead", `touch "$CANDYLAND_LEAD_TRIP"
`+emitText(`INTENT_BRIEF {\"restatedGoal\":\"x\",\"commitments\":[{\"id\":\"c1\",\"statement\":\"x\"}]}`)+emitResult("brief", 1)),
	role("intent manager", emitPartitionReview(true, "covers the commitments")),
	campIntentReviewer,
	// campTechDone ("technical sign-off") MUST precede the "tech manager" tripwire:
	// the gate-2 sign-off prompt contains both substrings, so the sign-off branch has
	// to win. Only the DECOMPOSE spawn (which lacks "technical sign-off") falls through
	// to the tripwire — which a reuse-relaunch must never reach.
	campTechDone,
	role("tech manager", `touch "$CANDYLAND_TECH_TRIP"
`+emitQuestsLine(`[{"id":"q1","title":"csv export","objective":"implement csv export end to end","folders":[],"deps":[]}]`)),
	campQuestLead,
	roleCleanReviewer,
	campChildTechLead,
	campChildCoder,
)

// A campaign RELAUNCH reuses the settled brief + gate-1-approved partition from
// durable state instead of re-running the intent lead / tech manager. Seeds a
// campaign as a prior drive would have left it (both gates passed, partition
// persisted, resumable status), then BeginCampaign must deliver WITHOUT spawning
// either pre-launch manager (tripwires stay absent).
func TestCampaignRelaunchReusesSettledGates(t *testing.T) {
	c, repo := deliveryConductor(t, relaunchReuseClaude)
	setCampaignFixtures(t, "satisfied") // both commitments satisfied → clean gate 2 → deliver
	leadTrip := filepath.Join(t.TempDir(), "intent-lead-spawned")
	techTrip := filepath.Join(t.TempDir(), "tech-manager-spawned")
	t.Setenv("CANDYLAND_LEAD_TRIP", leadTrip)
	t.Setenv("CANDYLAND_TECH_TRIP", techTrip)

	id := c.CreateCampaign(run.CampaignSpec{
		Input:   "add CSV export to the reports page",
		Folders: []string{repo},
	})
	now := time.Now().UTC().Format(time.RFC3339)
	c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.IntentBrief = run.IntentBrief{
			RestatedGoal: "add csv export to the reports page",
			Commitments: []run.Commitment{
				{ID: "c1", Statement: "export endpoint exists"},
				{ID: "c2", Statement: "export includes totals"},
			},
		}
		cam.BriefGate = run.GateResult{Passed: true, DecidedAt: now}
		cam.PlanGate = run.GateResult{Passed: true, DecidedAt: now}
		cam.Partition = []run.QuestPartitionItem{{ID: "q1", Title: "csv export", Objective: "implement csv export end to end"}}
		cam.Status = "blocked" // resumable
		cam.PauseReason = "seeded relaunch"
	})

	if !c.BeginCampaign(id) {
		t.Fatal("BeginCampaign returned false for a resumable campaign")
	}
	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool {
		return cam.Status == "done" || cam.Status == "blocked"
	}, 120*time.Second)
	if cam.Status != "done" {
		t.Fatalf("relaunched campaign did not deliver: status=%q reason=%q", cam.Status, cam.PauseReason)
	}
	if _, err := os.Stat(leadTrip); err == nil {
		t.Error("intent lead was re-spawned on relaunch — the settled brief must be reused, not re-derived")
	}
	if _, err := os.Stat(techTrip); err == nil {
		t.Error("tech manager was re-spawned on relaunch — the settled partition must be reused, not re-derived")
	}
	if len(cam.QuestIDs) != 1 {
		t.Fatalf("relaunch must launch the one persisted child quest, got %v", cam.QuestIDs)
	}
	child, _ := c.GetQuest(cam.QuestIDs[0])
	if child.Title != "csv export" {
		t.Errorf("child quest title must come from the reused partition, got %q", child.Title)
	}
	if len(cam.PRs) != 1 || cam.PRs[0].URL == "" {
		t.Fatalf("reused-gate campaign must deliver one PR, got %+v", cam.PRs)
	}
}

func TestReuseChildQuestAction(t *testing.T) {
	q := func(status string) run.Quest { return run.Quest{Status: status} }
	// remediation (reuse=false) never reuses, whatever exists.
	for _, s := range []string{"done", "paused", "running", "blocked", "stopped", "delivery-failed"} {
		if got := reuseChildQuestAction(false, q(s), true); got != "" {
			t.Errorf("reuse=false must never reuse (status %q), got %q", s, got)
		}
	}
	// nothing found → fresh.
	if got := reuseChildQuestAction(true, run.Quest{}, false); got != "" {
		t.Errorf("no existing quest → fresh, got %q", got)
	}
	cases := map[string]string{
		"done": "reuse-terminal", "reviewed": "reuse-terminal", "surfaced-only": "reuse-terminal",
		"paused": "resume", "running": "wait",
		"blocked": "", "stopped": "", "delivery-failed": "",
	}
	for status, want := range cases {
		if got := reuseChildQuestAction(true, q(status), true); got != want {
			t.Errorf("reuse=true status %q → %q, want %q", status, got, want)
		}
	}
}

func TestLatestChildQuestForItem(t *testing.T) {
	c, repo := deliveryConductor(t, stubClaude())
	camID := c.CreateCampaign(run.CampaignSpec{Input: "x", Folders: []string{repo}})
	// Two generations for item "a" (later id wins) and one for item "b".
	first := c.CreateQuest(run.QuestSpec{CampaignID: camID, PartitionItemID: "a", Objective: "a1", Folders: []string{repo}})
	_ = c.CreateQuest(run.QuestSpec{CampaignID: camID, PartitionItemID: "b", Objective: "b1", Folders: []string{repo}})
	second := c.CreateQuest(run.QuestSpec{CampaignID: camID, PartitionItemID: "a", Objective: "a2", Folders: []string{repo}})

	got, ok := c.latestChildQuestForItem(camID, "a")
	if !ok || got.ID != second {
		t.Fatalf("latest for item a must be the higher-id quest %q, got %q (ok=%v)", second, got.ID, ok)
	}
	if got.ID == first {
		t.Errorf("must not return the older generation %q", first)
	}
	if _, ok := c.latestChildQuestForItem(camID, "zzz"); ok {
		t.Error("unknown item must return found=false")
	}
	if _, ok := c.latestChildQuestForItem(camID, ""); ok {
		t.Error("empty item id must return found=false")
	}
}

// Keystone: the 2026-07-07 incident shape end-to-end. A campaign RELAUNCH must not
// repeat work a prior drive already delivered. Seeds the campaign as a blocked prior
// drive left it — settled gates, persisted partition, and one child quest already
// delivered on the campaign branch — then BeginCampaign must deliver while (i) NOT
// re-spawning the intent lead / tech manager (tripwires stay absent — change A),
// (ii) NOT minting a second-generation quest for the delivered item (change B), and
// (iii) launching ZERO new child runs (the delivered work is reused, not re-executed
// — changes B/C). This is the failure #46 describes: re-paid brief+partition and the
// same objective re-run across relaunched quests.
func TestCampaignRelaunchDoesNotRepeatDeliveredWork(t *testing.T) {
	c, repo := deliveryConductor(t, relaunchReuseClaude)
	setCampaignFixtures(t, "satisfied")
	leadTrip := filepath.Join(t.TempDir(), "lead")
	techTrip := filepath.Join(t.TempDir(), "tech")
	t.Setenv("CANDYLAND_LEAD_TRIP", leadTrip)
	t.Setenv("CANDYLAND_TECH_TRIP", techTrip)

	id := c.CreateCampaign(run.CampaignSpec{Input: "add CSV export", Folders: []string{repo}})
	if _, err := git(context.Background(), repo, "branch", "campaign/"+id); err != nil {
		t.Fatalf("seed campaign branch: %v", err)
	}
	childID := c.CreateQuest(run.QuestSpec{CampaignID: id, PartitionItemID: "q1", Title: "csv export", Objective: "implement csv export end to end", Folders: []string{repo}})
	c.UpdateQuest(childID, func(q *run.Quest) {
		q.Status = "done"
		q.TokensUsed = 100000 // the prior drive's spend on this quest
		q.WorkItems = append(q.WorkItems, run.WorkItem{ID: "t1-w0", SourceTick: "t1", Disposition: "completed"})
		recomputeQuestRollups(q)
	})
	now := time.Now().UTC().Format(time.RFC3339)
	c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.IntentBrief = run.IntentBrief{RestatedGoal: "add csv export", Commitments: []run.Commitment{{ID: "c1", Statement: "endpoint"}, {ID: "c2", Statement: "totals"}}}
		cam.BriefGate = run.GateResult{Passed: true, DecidedAt: now}
		cam.PlanGate = run.GateResult{Passed: true, DecidedAt: now}
		cam.Partition = []run.QuestPartitionItem{{ID: "q1", Title: "csv export", Objective: "implement csv export end to end"}}
		cam.Status = "blocked"
		cam.TokensUsed = 100000 // as the prior drive persisted it (the child's spend, already counted)
	})

	if !c.BeginCampaign(id) {
		t.Fatal("BeginCampaign returned false")
	}
	cam := waitForCampaign(t, c, id, func(cam run.Campaign) bool { return cam.Status == "done" || cam.Status == "blocked" }, 120*time.Second)
	if cam.Status != "done" {
		t.Fatalf("relaunch did not deliver: status=%q reason=%q", cam.Status, cam.PauseReason)
	}
	// (i) the pre-launch managers were NOT re-spawned — brief + partition were reused.
	if _, err := os.Stat(leadTrip); err == nil {
		t.Error("intent lead was re-spawned on relaunch — the settled brief must be reused")
	}
	if _, err := os.Stat(techTrip); err == nil {
		t.Error("tech manager was re-spawned on relaunch — the settled partition must be reused")
	}
	// (ii) no second-generation quest for the already-delivered partition item.
	n := 0
	for _, q := range c.CampaignChildQuests(id) {
		if q.PartitionItemID == "q1" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("relaunch must reuse the delivered child quest for item q1, not duplicate it — got %d quests for q1", n)
	}
	// (iii) ZERO new child runs — the delivered work was reused, not re-executed.
	if runs := c.CampaignChildRuns(id); len(runs) != 0 {
		t.Fatalf("relaunch must launch no new child runs for already-delivered work, got %d", len(runs))
	}
	// (iv) the reused quest's 100k tokens are NOT re-charged to the campaign. cam
	// started this drive at 100k; a relaunch adds only the (tiny) gate-2 spend, never
	// a second 100k — a double-count would land near 200k and could trip the cap.
	if cam.TokensUsed >= 150000 {
		t.Fatalf("relaunch double-counted the reused quest's tokens: cam.TokensUsed=%d (expected ~100k + small gate-2 delta)", cam.TokensUsed)
	}
}
