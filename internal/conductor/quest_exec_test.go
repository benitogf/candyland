package conductor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// waitForQuest polls a quest's persisted state until `until` holds or the deadline
// passes, mirroring waitFor for runs.
func waitForQuest(t *testing.T, c *Conductor, id string, until func(run.Quest) bool, d time.Duration) run.Quest {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		q, _ := c.GetQuest(id)
		if until(q) {
			return q
		}
		time.Sleep(20 * time.Millisecond)
	}
	q, _ := c.GetQuest(id)
	return q
}

// questTickClaude is a scripted stub `claude` that drives a whole quest tick with
// no real model. Composed from the stubClaude harness (see stubclaude_test.go); it
// branches on the spawn's role (the prompt) and a per-tick fixture file:
//   - the QUEST LEAD emits ONE work item on the first tick (recorded via a marker
//     file in CANDYLAND_QUEST_FIXTURE), then WORKITEMS_NONE on every later tick — so
//     the loop launches one child run, then naturally finishes (no safe work left).
//   - the child run's TECH LEAD emits a one-task PARTITION; its CODER writes a file
//     and reports a green TEST; its REVIEWER returns REVIEW_CLEAN — the existing run
//     executor then opens a PR. This exercises discover→triage→run→review→PR for one
//     work item end to end, deterministically.
var questTickClaude = stubClaude(
	role("quest lead", `if [[ -f "$CANDYLAND_QUEST_FIXTURE" ]]; then
  `+emitText("WORKITEMS_NONE")+`  `+emitResult("WORKITEMS_NONE", 1)+`else
  touch "$CANDYLAND_QUEST_FIXTURE"
  `+emitText(`WORKITEMS [{\"title\":\"tidy the lint\",\"evidence\":\"a stale import\",\"classification\":\"cleanup\",\"decision\":\"do\"}]`)+`  `+emitResult("done", 2)+`fi
`),
	roleCleanReviewer,
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

// The ORACLE for the CONVERGE policy (the default, a bounded quest): the child run
// accumulates its commits onto the quest's own branch (quest/<id>) with NO per-child
// PR, and the quest opens ONE PR per impacted repo at terminal (the campaign delivery
// shape). Discover → triage → run → review → branch, then one terminal PR.
func TestQuestConvergeOpensOnePRAtTerminal(t *testing.T) {
	c, repo := deliveryConductor(t, questTickClaude)
	t.Setenv("CANDYLAND_QUEST_FIXTURE", filepath.Join(t.TempDir(), "first-tick"))

	id := c.CreateQuest(run.QuestSpec{
		Objective: "keep it tidy",
		Folders:   []string{repo}, // the quest lead runs here; child runs use the conductor's folders override
	})
	// Default convergence is "converge".
	if q, _ := c.GetQuest(id); q.Convergence != run.ConvergeConverge {
		t.Fatalf("default convergence = %q, want converge", q.Convergence)
	}
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false for a fresh quest")
	}

	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "done" }, 60*time.Second)
	if q.Status != "done" {
		t.Fatalf("quest did not finish: status=%q reason=%q ticks=%d", q.Status, q.PauseReason, len(q.Ticks))
	}

	first := q.Ticks[0]
	if len(first.LaunchedRunIDs) != 1 {
		t.Fatalf("first tick must launch exactly one child run, got %d (%+v)", len(first.LaunchedRunIDs), first.LaunchedRunIDs)
	}
	// A converge child run delivers to the branch — it records NO PR on the tick.
	if len(first.PRs) != 0 {
		t.Errorf("a converge child run must not open a per-child PR, got tick PRs %+v", first.PRs)
	}

	wi := q.WorkItems[0]
	if wi.Disposition != "completed" || q.ItemsCompleted != 1 {
		t.Errorf("item disposition=%q ItemsCompleted=%d, want completed/1", wi.Disposition, q.ItemsCompleted)
	}

	// The child run committed onto quest/<id> and opened NO PR of its own.
	child, ok := c.Get(wi.ChildRunID)
	if !ok {
		t.Fatalf("child run %q not tracked", wi.ChildRunID)
	}
	if child.Status != "done" || child.Error != "" {
		t.Fatalf("child run did not finish cleanly: status=%q error=%q", child.Status, child.Error)
	}
	if child.Deliver != run.DeliverBranch {
		t.Errorf("converge child deliver = %q, want branch", child.Deliver)
	}
	if child.Branch != "quest/"+id {
		t.Errorf("converge child branch = %q, want quest/%s", child.Branch, id)
	}
	if child.PrURL != "" {
		t.Errorf("a converge child run must open NO PR, got %q", child.PrURL)
	}

	// The delivery gate ran before the PR opened (Task 7): a converge delivery is
	// preceded by a recorded review gate (GateRounds ≥ 1).
	if q.GateRounds < 1 {
		t.Errorf("converge delivery must record a delivery gate (GateRounds ≥ 1), got %d", q.GateRounds)
	}
	// The quest opened exactly ONE terminal PR per impacted repo (one repo here).
	if len(q.PRs) != 1 || q.PRs[0].URL == "" {
		t.Fatalf("converge quest must open one terminal PR, got %+v", q.PRs)
	}
	if q.PRsOpened != 1 {
		t.Errorf("PRsOpened rollup = %d, want 1", q.PRsOpened)
	}
}

// The ORACLE for the perFinding policy (an adventure): each accepted finding is its
// own child run with deliver=pr and opens its OWN PR — the pre-convergence behavior,
// now explicit via Convergence: perFinding.
func TestAdventurePerFindingOpensPRPerFinding(t *testing.T) {
	c, repo := deliveryConductor(t, questTickClaude)
	t.Setenv("CANDYLAND_QUEST_FIXTURE", filepath.Join(t.TempDir(), "first-tick"))

	id := c.CreateQuest(run.QuestSpec{
		Objective:   "keep it tidy",
		Folders:     []string{repo},
		Convergence: run.ConvergePerFinding,
	})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false for a fresh quest")
	}

	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "done" }, 60*time.Second)
	if q.Status != "done" {
		t.Fatalf("quest did not finish: status=%q reason=%q", q.Status, q.PauseReason)
	}
	wi := q.WorkItems[0]
	child, ok := c.Get(wi.ChildRunID)
	if !ok {
		t.Fatalf("child run %q not tracked", wi.ChildRunID)
	}
	if child.Deliver != run.DeliverPR {
		t.Errorf("perFinding child deliver = %q, want pr", child.Deliver)
	}
	if child.PrURL == "" {
		t.Error("a perFinding (adventure) child run must open its own PR")
	}
	// No terminal per-repo PR — the finding's own PR is the delivery.
	if len(q.PRs) != 0 {
		t.Errorf("perFinding quest must not open a terminal PR, got %+v", q.PRs)
	}
	if q.PRsOpened != 1 {
		t.Errorf("PRsOpened rollup = %d, want 1 (the child's own PR)", q.PRsOpened)
	}

	kids := c.QuestChildRuns(id)
	if len(kids) != 1 || kids[0].ID != wi.ChildRunID {
		t.Errorf("QuestChildRuns = %+v, want one run %q", kids, wi.ChildRunID)
	}
}

// pauseTickClaude keeps surfacing the SAME item every tick (no marker), so the loop
// would keep ticking — letting the test pause it mid-flight and assert it stops.
const pauseTickClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"quest lead"* ]]; then
  sleep 0.2
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"WORKITEMS [{\"title\":\"never-ending item\",\"evidence\":\"x\",\"classification\":\"cleanup\",\"decision\":\"do\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"done","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"do it\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "done by $$" > "a.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":1}}'
fi
`

// Stop is the only quest control and it is terminal: status flips to stopped with
// the reason, and a stopped quest never ticks again (BeginQuest refuses it).
func TestQuestStopIsTerminal(t *testing.T) {
	c, _ := newQuestServer(t)
	id := c.CreateQuest(run.QuestSpec{Objective: "x", Folders: []string{"/repo"}})

	if !c.StopQuest(id, "done for the day") {
		t.Fatal("StopQuest should succeed")
	}
	q, _ := c.GetQuest(id)
	if q.Status != "stopped" || q.PauseReason != "done for the day" {
		t.Fatalf("stop not recorded: status=%q reason=%q", q.Status, q.PauseReason)
	}
	if c.BeginQuest(id) {
		t.Error("a stopped quest must not be begin-able")
	}
}

// A running quest, stopped mid-drive, stops ticking — no further ticks are recorded
// after the stop settles. Uses a live bus + repo so the loop genuinely runs.
func TestQuestStopHaltsTicking(t *testing.T) {
	c, repo := deliveryConductor(t, pauseTickClaude)
	t.Setenv("CANDYLAND_QUEST_ITEM_ATTEMPTS", "100") // don't let the thrash cap stop it first

	id := c.CreateQuest(run.QuestSpec{
		Objective: "loops forever until stopped",
		Folders:   []string{repo},
	})
	c.BeginQuest(id)

	// Let at least one tick land, then stop.
	waitForQuest(t, c, id, func(q run.Quest) bool { return len(q.Ticks) >= 1 }, 60*time.Second)
	if !c.StopQuest(id, "halt") {
		t.Fatal("stop failed")
	}
	// Wait for the drive to settle stopped.
	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "stopped" }, 30*time.Second)
	if q.Status != "stopped" {
		t.Fatalf("quest did not stop: status=%q", q.Status)
	}
	settled := len(q.Ticks)
	// No further ticks after the stop settles.
	time.Sleep(500 * time.Millisecond)
	q2, _ := c.GetQuest(id)
	if len(q2.Ticks) > settled {
		t.Errorf("stopped quest kept ticking: %d ticks after stop, was %d", len(q2.Ticks), settled)
	}
}

// The token-budget cap pauses a quest (with a visible reason) once usage crosses the
// budget — CANDYLAND_QUEST_TOKEN_CAP honoring without a real model.
func TestQuestTokenBudgetPauses(t *testing.T) {
	c, _ := newQuestServer(t)
	id := c.CreateQuest(run.QuestSpec{Objective: "x", Folders: []string{"/repo"}, TokenBudget: 100})
	c.UpdateQuest(id, func(q *run.Quest) { q.TokensUsed = 150 }) // already over budget

	c.pauseQuestForBudget(id, 150, 100)
	q, _ := c.GetQuest(id)
	if q.Status != "paused" {
		t.Fatalf("over-budget quest must pause, got %q", q.Status)
	}
	if q.PauseReason == "" {
		t.Error("the pause must carry a visible reason")
	}
}

// resurfaceBlocked is the "re-surface" half of convergence: transiently-blocked
// items (a child run failed but attempts remain) are handed back for a retry when a
// tick discovers nothing new, so a transient block converges instead of terminating
// unresolved. Pin the contract the drive loop relies on.
func TestResurfaceBlocked(t *testing.T) {
	if got := resurfaceBlocked(nil); got != nil {
		t.Fatalf("empty map must re-surface nothing, got %+v", got)
	}
	blocked := map[string]questWorkItem{
		"flaky item":   {Title: "flaky item", Evidence: "e1", Classification: "c1", Decision: "do"},
		"another item": {Title: "another item", Evidence: "e2", Classification: "c2", Decision: "do"},
	}
	got := resurfaceBlocked(blocked)
	if len(got) != 2 {
		t.Fatalf("both blocked items must re-surface, got %d (%+v)", len(got), got)
	}
	seen := map[string]bool{}
	for _, it := range got {
		seen[it.Title] = true
	}
	if !seen["flaky item"] || !seen["another item"] {
		t.Errorf("re-surfaced titles = %v, want both blocked items", seen)
	}
}

// The convergence gate: a quest with unresolved blocked items converges to a
// terminal "blocked" (with a schema-valid postmortem, E2) — NEVER a clean "done".
// A quest with zero blocked items reaches "done". This is the "gates clean done on
// zero blocked" half of the task.
func TestQuestConvergenceGatesDoneOnBlocked(t *testing.T) {
	// zero blocked, delivered work → clean "done".
	clean := &run.Quest{ItemsCompleted: 1, PRsOpened: 1, WorkItems: []run.WorkItem{{Disposition: "completed"}}}
	if got := questTerminalStatus(clean); got != "done" {
		t.Errorf("zero-blocked delivered quest status = %q, want done", got)
	}
	// one blocked item → the gate forces "blocked", not "done".
	blocked := &run.Quest{ItemsCompleted: 1, ItemsBlocked: 1}
	if got := questTerminalStatus(blocked); got != "blocked" {
		t.Errorf("quest with a blocked item status = %q, want blocked", got)
	}

	// finishQuest end to end: a converge quest whose only item is durably blocked must
	// land "blocked" with a postmortem attached and a summary naming the block — not a
	// silent "done". ItemsCompleted is 0 so no terminal branch PR is attempted (no git).
	c, _ := newQuestServer(t)
	id := c.CreateQuest(run.QuestSpec{Objective: "converge me", Folders: []string{"/repo"}})
	c.UpdateQuest(id, func(q *run.Quest) {
		q.WorkItems = []run.WorkItem{{ID: "t1-w0", SourceTick: "t1", Classification: "cleanup", Evidence: "a stale import", Disposition: "blocked"}}
		recomputeQuestRollups(q)
	})
	c.finishQuest(context.Background(), id)

	q, _ := c.GetQuest(id)
	if q.Status != "blocked" {
		t.Fatalf("blocked-item quest terminal status = %q, want blocked", q.Status)
	}
	if q.Postmortem == nil {
		t.Error("a terminal blocked quest must carry a schema-valid postmortem (E2)")
	}
	if q.Summary == "" || !contains(q.Summary, "blocked") {
		t.Errorf("terminal summary must name the block, got %q", q.Summary)
	}
}

// questTwoItemClaude surfaces TWO accepted items on the first tick, then
// WORKITEMS_NONE — so the drive loop launches two child runs in one tick.
var questTwoItemClaude = stubClaude(
	role("quest lead", `if [[ -f "$CANDYLAND_QUEST_FIXTURE" ]]; then
  `+emitText("WORKITEMS_NONE")+`  `+emitResult("WORKITEMS_NONE", 1)+`else
  touch "$CANDYLAND_QUEST_FIXTURE"
  `+emitText(`WORKITEMS [{\"title\":\"item one\",\"evidence\":\"e1\",\"classification\":\"cleanup\",\"decision\":\"do\"},{\"title\":\"item two\",\"evidence\":\"e2\",\"classification\":\"cleanup\",\"decision\":\"do\"}]`)+`  `+emitResult("done", 2)+`fi
`),
	roleCleanReviewer,
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

// Child runs launch SERIALLY within a tick: each accepted item's child run is
// driven to a terminal state (launchChildRun blocks) before the next launches, so a
// two-item tick records two child runs and both complete. Serial launch is what
// keeps siblings off each other on the shared branch (paired with push rebase-retry).
func TestQuestLaunchesChildRunsSerially(t *testing.T) {
	c, repo := deliveryConductor(t, questTwoItemClaude)
	t.Setenv("CANDYLAND_QUEST_FIXTURE", filepath.Join(t.TempDir(), "first-tick"))

	id := c.CreateQuest(run.QuestSpec{
		Objective:   "keep it tidy",
		Folders:     []string{repo},
		Convergence: run.ConvergePerFinding, // each finding its own PR — clean, independent children
	})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false for a fresh quest")
	}

	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "done" }, 60*time.Second)
	if q.Status != "done" {
		t.Fatalf("quest did not finish: status=%q reason=%q", q.Status, q.PauseReason)
	}
	first := q.Ticks[0]
	if len(first.LaunchedRunIDs) != 2 {
		t.Fatalf("a two-item tick must launch two child runs serially, got %d (%+v)", len(first.LaunchedRunIDs), first.LaunchedRunIDs)
	}
	if q.ItemsCompleted != 2 {
		t.Errorf("both serially-launched items must complete, got ItemsCompleted=%d", q.ItemsCompleted)
	}
	for _, childID := range first.LaunchedRunIDs {
		child, ok := c.Get(childID)
		if !ok {
			t.Fatalf("child run %q not tracked", childID)
		}
		if child.Status != "done" || child.Error != "" {
			t.Errorf("child run %q did not finish cleanly: status=%q error=%q", childID, child.Status, child.Error)
		}
	}
}

// The triage blocker rule: a quest-lead triage decision of "block" is NOT benign
// like "skip" — it records a DURABLE blocked WorkItem that gates the clean terminal,
// while "skip" stays a benign "skipped". Pin the pure ledger builder both halves
// share.
func TestTriagedWorkItemsBlockerRule(t *testing.T) {
	ledger, decisions := triagedWorkItems([]questWorkItem{
		{Title: "out of scope", Evidence: "e1", Classification: "cleanup", Decision: "skip"},
		{Title: "needs human creds", Evidence: "e2", Classification: "blocked", Decision: "block"},
		{Title: "do this", Evidence: "e3", Decision: "do"}, // accepted — never in the triage ledger
	}, "t1")
	if len(ledger) != 2 {
		t.Fatalf("only skip+block enter the triage ledger, got %d (%+v)", len(ledger), ledger)
	}
	if len(decisions) != 2 {
		t.Errorf("triage decision lines = %+v, want 2", decisions)
	}
	byDecision := map[string]run.WorkItem{}
	for _, w := range ledger {
		byDecision[w.Decision] = w
	}
	if got := byDecision["skip"].Disposition; got != "skipped" {
		t.Errorf("a skip must record Disposition skipped, got %q", got)
	}
	if got := byDecision["block"].Disposition; got != "blocked" {
		t.Errorf("a block must record Disposition blocked (the blocker rule), got %q", got)
	}
}

// questTriageBlockClaude drives a quest whose lead surfaces exactly one item and
// TRIAGES it "block" (nothing accepted, no child run launched) on the first tick,
// then WORKITEMS_NONE. The loop must record it as a blocked WorkItem and converge
// to a terminal "blocked" with a postmortem — never a silent "done".
var questTriageBlockClaude = stubClaude(
	role("quest lead", `if [[ -f "$CANDYLAND_QUEST_FIXTURE" ]]; then
  `+emitText("WORKITEMS_NONE")+`  `+emitResult("WORKITEMS_NONE", 1)+`else
  touch "$CANDYLAND_QUEST_FIXTURE"
  `+emitText(`WORKITEMS [{\"title\":\"needs human creds\",\"evidence\":\"blocked on a secret only a human can supply\",\"classification\":\"blocked\",\"decision\":\"block\"}]`)+`  `+emitResult("done", 2)+`fi
`),
	roleCleanReviewer,
	coder(emitText("noop"), emitResult("noop", 1)), // no child launches; a non-empty default keeps the bash valid
)

// End to end: a triage "block" flows through the drive loop to a terminal "blocked"
// quest carrying an E2 postmortem — the triage blocker rule, not a looks-done no-op.
func TestQuestTriageBlockConvergesToBlocked(t *testing.T) {
	c, repo := deliveryConductor(t, questTriageBlockClaude)
	t.Setenv("CANDYLAND_QUEST_FIXTURE", filepath.Join(t.TempDir(), "first-tick"))

	id := c.CreateQuest(run.QuestSpec{Objective: "keep it tidy", Folders: []string{repo}})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false for a fresh quest")
	}

	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "blocked" }, 60*time.Second)
	if q.Status != "blocked" {
		t.Fatalf("a triage-blocked quest must converge to blocked, got status=%q reason=%q", q.Status, q.PauseReason)
	}
	if q.ItemsBlocked != 1 {
		t.Errorf("the triaged block must count as one blocked item, got ItemsBlocked=%d", q.ItemsBlocked)
	}
	if len(q.WorkItems) != 1 || q.WorkItems[0].Disposition != "blocked" {
		t.Errorf("the surfaced item must be a durable blocked WorkItem, got %+v", q.WorkItems)
	}
	if len(q.Ticks) == 0 || len(q.Ticks[0].LaunchedRunIDs) != 0 {
		t.Errorf("a triage block launches no child run, got %+v", q.Ticks)
	}
	if q.Postmortem == nil {
		t.Error("a terminal blocked quest must carry a schema-valid postmortem (E2)")
	}
}

// contains reports whether sub appears in s (a tiny local helper to keep the assert
// legible without pulling strings into the test's imports).
func contains(s, sub string) bool { return len(sub) == 0 || indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// parseWorkItems is the quest-lead verdict convention (the WORKITEMS / WORKITEMS_NONE
// fenced lines), mirroring parseReview. Pin it so the contract the stub and a real
// quest lead share can't drift silently.
func TestParseWorkItems(t *testing.T) {
	items, none, ok := parseWorkItems(`some preamble
WORKITEMS [{"title":"a","evidence":"e","classification":"c","decision":"do"},{"title":"b","decision":"skip"}]`)
	if !ok || none {
		t.Fatalf("a WORKITEMS line must parse (ok=%v none=%v)", ok, none)
	}
	if len(items) != 2 || items[0].Title != "a" || items[1].Decision != "skip" {
		t.Fatalf("items not parsed: %+v", items)
	}
	// acceptedItems drops skip/block.
	if acc := acceptedItems(items); len(acc) != 1 || acc[0].Title != "a" {
		t.Errorf("acceptedItems must drop skip/block, got %+v", acc)
	}

	if _, none, ok := parseWorkItems("WORKITEMS_NONE"); !ok || !none {
		t.Errorf("WORKITEMS_NONE must parse as none (ok=%v none=%v)", ok, none)
	}
	if _, _, ok := parseWorkItems("no verdict at all"); ok {
		t.Error("text with no verdict line must report ok=false (never a silent pass)")
	}
}
