package conductor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// === Objective-met dedup on quest relaunch ===================================
//
// A relaunch (a fresh BeginQuest drive after a pause/tick-bound) starts with an
// empty itemAttempts map, so discovery re-surfacing an item a PRIOR drive already
// delivered on the shared branch would re-launch it — redoing work already on the
// branch. deliveredTitles reads the durable completed ledger as the dedup evidence,
// and runQuestTick closes a re-surfaced delivered item directly from that evidence
// (no re-spawn), per core/completion objective-met dedup.

// --- deliveredTitles (pure) --------------------------------------------------

// A shared-branch quest's completed ledger yields its delivered titles (recovered
// from the tick's triage-decision lines), keyed case/space-insensitively. Skipped
// and blocked items are NOT delivered, so they never dedup.
func TestDeliveredTitlesFromLedger(t *testing.T) {
	q := run.Quest{
		Ticks: []run.Tick{{
			ID: "t0",
			TriageDecisions: []string{
				"Tidy The Lint: do now", // completed → delivered
				"fix the docs: do now",  // blocked → NOT delivered
				"risky rename: skip",    // skipped → NOT delivered
			},
		}},
		WorkItems: []run.WorkItem{
			{ID: "t0-w0", SourceTick: "t0", Disposition: "completed"},
			{ID: "t0-w1", SourceTick: "t0", Disposition: "blocked"},
			{ID: "t0-s0", SourceTick: "t0", Disposition: "skipped"},
		},
	}
	got := deliveredTitles(q)
	if len(got) != 1 || !got[dedupKey(" TIDY THE LINT ")] {
		t.Fatalf("delivered set = %v, want only the completed title (case/space-insensitive)", got)
	}
}

// A quest with NO shared branch (a perFinding adventure, or a feedback/review quest)
// accumulates nothing across drives — each finding is its own PR and re-surfacing is
// guarded by dropOwnArtifacts — so ledger dedup does not apply: deliveredTitles is
// empty even with completed items on the ledger.
func TestDeliveredTitlesEmptyWithoutSharedBranch(t *testing.T) {
	q := run.Quest{
		Convergence: run.ConvergePerFinding,
		Ticks:       []run.Tick{{ID: "t0", TriageDecisions: []string{"tidy the lint: do now"}}},
		WorkItems:   []run.WorkItem{{ID: "t0-w0", SourceTick: "t0", Disposition: "completed"}},
	}
	if got := deliveredTitles(q); len(got) != 0 {
		t.Fatalf("a perFinding quest has no shared branch — delivered set must be empty, got %v", got)
	}
}

// --- end to end: relaunch dedups the already-delivered item -------------------

// dedupQuestClaude drives a tick whose quest lead surfaces TWO items: one a prior
// drive already delivered ("tidy the lint", pre-seeded onto the ledger) and one
// genuinely new ("fix the docs"). The loop must DEDUP the delivered one (no child
// run) and still LAUNCH the new one, then finish cleanly with one terminal PR.
var dedupQuestClaude = stubClaude(
	role("quest lead", `if [[ -f "$CANDYLAND_QUEST_FIXTURE" ]]; then
  `+emitText("WORKITEMS_NONE")+`  `+emitResult("WORKITEMS_NONE", 1)+`else
  touch "$CANDYLAND_QUEST_FIXTURE"
  `+emitText(`WORKITEMS [{\"title\":\"tidy the lint\",\"evidence\":\"already delivered\",\"classification\":\"cleanup\",\"decision\":\"do\"},{\"title\":\"fix the docs\",\"evidence\":\"a new gap\",\"classification\":\"docs\",\"decision\":\"do\"}]`)+`  `+emitResult("done", 2)+`fi
`),
	roleCleanReviewer,
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

func TestQuestRelaunchDedupsAlreadyDeliveredItem(t *testing.T) {
	c, repo := deliveryConductor(t, dedupQuestClaude)
	t.Setenv("CANDYLAND_QUEST_FIXTURE", filepath.Join(t.TempDir(), "first-tick"))

	id := c.CreateQuest(run.QuestSpec{
		Objective: "keep it tidy",
		Folders:   []string{repo},
	})
	// Pre-seed the durable ledger as a PRIOR drive would: "tidy the lint" was already
	// delivered onto the shared branch (a completed WorkItem whose title is recoverable
	// from its tick's triage-decision line).
	c.UpdateQuest(id, func(q *run.Quest) {
		q.Ticks = append(q.Ticks, run.Tick{ID: "t0", TriageDecisions: []string{"tidy the lint: do now"}})
		q.WorkItems = append(q.WorkItems, run.WorkItem{ID: "t0-w0", SourceTick: "t0", Decision: "do", Disposition: "completed"})
		recomputeQuestRollups(q)
	})

	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false for a seeded quest")
	}
	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "done" }, 60*time.Second)
	if q.Status != "done" {
		t.Fatalf("quest did not finish: status=%q reason=%q ticks=%d", q.Status, q.PauseReason, len(q.Ticks))
	}

	// The drive's first tick is t1 (t0 was the pre-seed). It surfaced two items:
	// the delivered one was deduped (no launch), the new one launched exactly one child.
	drive := q.Ticks[1]
	if len(drive.LaunchedRunIDs) != 1 {
		t.Fatalf("tick must launch exactly one child run (the new item only), got %d: %v", len(drive.LaunchedRunIDs), drive.LaunchedRunIDs)
	}
	if !strings.Contains(drive.NextAction, "deduped") {
		t.Errorf("tick NextAction must record the dedup, got %q", drive.NextAction)
	}

	// Both surfaced items are completed on the ledger: t1-w0 deduped (no child run),
	// t1-w1 launched. The deduped one carries NO ChildRunID.
	var deduped, launched *run.WorkItem
	for i := range q.WorkItems {
		switch q.WorkItems[i].ID {
		case "t1-w0":
			deduped = &q.WorkItems[i]
		case "t1-w1":
			launched = &q.WorkItems[i]
		}
	}
	if deduped == nil || deduped.Disposition != "completed" || deduped.ChildRunID != "" {
		t.Fatalf("deduped item must be completed with no child run, got %+v", deduped)
	}
	if launched == nil || launched.Disposition != "completed" || launched.ChildRunID == "" {
		t.Fatalf("new item must be completed via a launched child run, got %+v", launched)
	}

	// One terminal PR opened for the shared branch (the new item's commit landed there).
	if len(q.PRs) != 1 || q.PRs[0].URL == "" {
		t.Fatalf("converge quest must open one terminal PR, got %+v", q.PRs)
	}
}
