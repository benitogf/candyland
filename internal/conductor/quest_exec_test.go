package conductor

import (
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
// no real model. It branches on the spawn's role (the prompt) and the bus brief:
//   - the QUEST LEAD emits ONE work item on the first tick (recorded via a marker
//     file in CANDYLAND_QUEST_FIXTURE), then WORKITEMS_NONE on every later tick — so
//     the loop launches one child run, then naturally finishes (no safe work left).
//   - the child run's TECH LEAD emits a one-task PARTITION; its CODER writes a file
//     and reports a green TEST; its REVIEWER returns REVIEW_CLEAN — the existing run
//     executor then opens a PR. This exercises discover→triage→run→review→PR for one
//     work item end to end, deterministically.
const questTickClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"quest lead"* ]]; then
  if [[ -f "$CANDYLAND_QUEST_FIXTURE" ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"WORKITEMS_NONE"}]}}'
    echo '{"type":"result","subtype":"success","result":"WORKITEMS_NONE","usage":{"output_tokens":1}}'
  else
    touch "$CANDYLAND_QUEST_FIXTURE"
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"WORKITEMS [{\"title\":\"tidy the lint\",\"evidence\":\"a stale import\",\"classification\":\"cleanup\",\"decision\":\"do\"}]"}]}}'
    echo '{"type":"result","subtype":"success","result":"done","usage":{"output_tokens":2}}'
  fi
elif [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"do the item\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "done by $$" > "a.txt"
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"TEST {\"pass\":1,\"fail\":0}"}]}}'
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

// The ORACLE for the quest execution layer: a scripted-stub tick asserting the full
// discover → triage → run → review → PR transition for ONE work item. An L3
// (unattended) quest discovers one item, launches a child run via the EXISTING run
// executor (QuestID set, deliver=pr → its own PR), records the tick + work item, and
// finishes once the quest lead reports no more work.
func TestQuestTickLaunchesChildRunToPR(t *testing.T) {
	c, repo := deliveryConductor(t, questTickClaude)
	t.Setenv("CANDYLAND_QUEST_FIXTURE", filepath.Join(t.TempDir(), "first-tick"))

	id := c.CreateQuest(run.QuestSpec{
		Objective:     "keep it tidy",
		Folders:       []string{repo}, // the quest lead runs here; child runs use the conductor's folders override
		AutonomyLevel: run.AutonomyUnattended,
	})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false for a fresh quest")
	}

	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "done" }, 60*time.Second)
	if q.Status != "done" {
		t.Fatalf("quest did not finish: status=%q reason=%q ticks=%d", q.Status, q.PauseReason, len(q.Ticks))
	}

	// A tick was recorded with the discovery + triage + launch trace.
	if len(q.Ticks) == 0 {
		t.Fatal("no ticks recorded")
	}
	first := q.Ticks[0]
	if len(first.LaunchedRunIDs) != 1 {
		t.Fatalf("first tick must launch exactly one child run, got %d (%+v)", len(first.LaunchedRunIDs), first.LaunchedRunIDs)
	}
	if len(first.TriageDecisions) == 0 {
		t.Error("the tick must record a triage decision for the surfaced item")
	}
	if first.DiscoverySummary == "" {
		t.Error("the tick must record a discovery summary")
	}

	// The work item ledger linked the item to its child run and marked it completed.
	if len(q.WorkItems) != 1 {
		t.Fatalf("expected one work item, got %d: %+v", len(q.WorkItems), q.WorkItems)
	}
	wi := q.WorkItems[0]
	if wi.ChildRunID == "" {
		t.Error("the work item must link the child run it launched")
	}
	if wi.Disposition != "completed" {
		t.Errorf("work item disposition = %q, want completed", wi.Disposition)
	}
	if q.ItemsCompleted != 1 {
		t.Errorf("ItemsCompleted rollup = %d, want 1", q.ItemsCompleted)
	}

	// The child run is the real run executor's output: QuestID set, reached PR.
	child, ok := c.Get(wi.ChildRunID)
	if !ok {
		t.Fatalf("child run %q not tracked", wi.ChildRunID)
	}
	if child.QuestID != id {
		t.Errorf("child run QuestID = %q, want %q", child.QuestID, id)
	}
	if child.Status != "done" || child.Error != "" {
		t.Fatalf("child run did not finish cleanly: status=%q error=%q", child.Status, child.Error)
	}
	if child.PrURL == "" {
		t.Error("a standalone (deliver=pr) child run must open its own PR")
	}
	if q.PRsOpened != 1 {
		t.Errorf("PRsOpened rollup = %d, want 1", q.PRsOpened)
	}

	// QuestChildRuns surfaces it by parent link.
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

// Pause/resume/stop state transitions: a paused quest stops ticking; resume
// continues; stop is terminal (a stopped quest never ticks again and can't begin).
func TestQuestPauseResumeStop(t *testing.T) {
	c, _ := newQuestServer(t)
	id := c.CreateQuest(run.QuestSpec{Objective: "x", Folders: []string{"/repo"}})

	// Pause an idle (never-begun) quest: status flips, no drive needed.
	if !c.PauseQuest(id, "operator hold") {
		t.Fatal("PauseQuest should succeed on a known quest")
	}
	q, _ := c.GetQuest(id)
	if q.Status != "paused" || q.PauseReason != "operator hold" {
		t.Fatalf("pause not recorded: status=%q reason=%q", q.Status, q.PauseReason)
	}

	// A paused quest does not tick: BeginQuest resumes it (status running), but with
	// no real claude here discovery fails fast and it blocks — the point is only that
	// resume is refused unless paused, and a non-paused quest can't be "resumed".
	if c.ResumeQuest("nope") {
		t.Error("ResumeQuest on an unknown quest must return false")
	}

	// Stop is terminal: status stopped, and a stopped quest refuses begin/resume.
	if !c.StopQuest(id, "done for the day") {
		t.Fatal("StopQuest should succeed")
	}
	q, _ = c.GetQuest(id)
	if q.Status != "stopped" || q.PauseReason != "done for the day" {
		t.Fatalf("stop not recorded: status=%q reason=%q", q.Status, q.PauseReason)
	}
	if c.BeginQuest(id) {
		t.Error("a stopped quest must not be begin-able")
	}
	if c.ResumeQuest(id) {
		t.Error("a stopped quest must not be resumable")
	}
}

// A running quest, paused mid-drive, stops ticking — no further ticks are recorded
// after the pause settles. Uses a live bus + repo so the loop genuinely runs.
func TestQuestPauseHaltsTicking(t *testing.T) {
	c, repo := deliveryConductor(t, pauseTickClaude)
	t.Setenv("CANDYLAND_QUEST_ITEM_ATTEMPTS", "100") // don't let the thrash cap stop it first

	id := c.CreateQuest(run.QuestSpec{
		Objective:     "loops forever until paused",
		Folders:       []string{repo},
		AutonomyLevel: run.AutonomyUnattended,
	})
	c.BeginQuest(id)

	// Let at least one tick land, then pause.
	waitForQuest(t, c, id, func(q run.Quest) bool { return len(q.Ticks) >= 1 }, 60*time.Second)
	if !c.PauseQuest(id, "halt") {
		t.Fatal("pause failed")
	}
	// Wait for the drive to settle paused.
	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "paused" }, 30*time.Second)
	if q.Status != "paused" {
		t.Fatalf("quest did not pause: status=%q", q.Status)
	}
	settled := len(q.Ticks)
	// No further ticks after the pause settles.
	time.Sleep(500 * time.Millisecond)
	q2, _ := c.GetQuest(id)
	if len(q2.Ticks) > settled {
		t.Errorf("paused quest kept ticking: %d ticks after pause, was %d", len(q2.Ticks), settled)
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
