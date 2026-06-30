package conductor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// A run still in the planning Q&A has no executor goroutine — a stop just sits
// buffered on the control channel and never takes effect. Cancel must abandon it
// outright; that's the state the user gets stuck in. After cancel the run leaves
// active tracking (Get → !ok) — it's kept as "cancelled" in ooo for the Tasks
// history, which a serverless conductor can't observe and which check-history.mjs
// covers against the real binary.
func TestCancelDuringPlanningRemovesRun(t *testing.T) {
	c := New(nil)
	id := c.Create(run.Spec{Prompt: "do the thing"})

	if _, ok := c.Get(id); !ok {
		t.Fatal("run should exist after Create")
	}
	if !c.Cancel(id) {
		t.Fatal("Cancel should succeed for a planning run")
	}
	if _, ok := c.Get(id); ok {
		t.Error("run should be gone after Cancel")
	}
	// Idempotent: cancelling an unknown/already-cancelled run reports false.
	if c.Cancel(id) {
		t.Error("Cancel of an already-cancelled run should report false")
	}
	// A cancelled run can't be resurrected by a late Begin.
	c.Begin(id, nil)
	if _, ok := c.Get(id); ok {
		t.Error("Begin must not recreate a cancelled run")
	}
}

// Edit on a genuinely-paused run must terminate the parked executor and then,
// on the next Begin, spawn exactly ONE healthy executor — a stale "quit" from the
// old generation must not reach (and kill) the new one. Uses a real executor
// goroutine (not a synthetic status), which is what makes the concurrency claim
// real rather than tautological.
func TestEditPausedThenBeginRunsCleanly(t *testing.T) {
	c, _ := deliveryConductor(t, slowCoder) // tech lead partitions fast; coder then sleeps
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000")

	id := c.Create(run.Spec{Prompt: "original"})
	c.Begin(id, nil)
	working := func(r run.Run) bool {
		for _, a := range r.Agents {
			if a.ID == "a" && a.State == "working" {
				return true
			}
		}
		return false
	}
	if r := waitFor(t, c, id, working, 20*time.Second); !working(r) {
		t.Fatal("first run never reached a working coder")
	}
	// Stop → paused (executor parks on its control channel).
	c.Command(id, "stop")
	if r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "paused" }, 15*time.Second); r.Status != "paused" {
		t.Fatalf("run did not pause: %q", r.Status)
	}

	// Edit the stopped run → quits the parked executor and re-plans.
	if !c.Edit(id, run.Spec{Prompt: "edited request"}) {
		t.Fatal("Edit should succeed for a paused run")
	}
	if r, _ := c.Get(id); r.Status != "planning" || r.Prompt != "edited request" {
		t.Fatalf("edit did not re-plan: status=%q prompt=%q", r.Status, r.Prompt)
	}

	// Begin again → a fresh executor on a fresh channel runs the new task; if a
	// stale quit had reached it, it would die instead of reaching a working coder.
	c.Begin(id, nil)
	if r := waitFor(t, c, id, working, 20*time.Second); !working(r) {
		st := []string{}
		for _, a := range r.Agents {
			st = append(st, a.ID+":"+a.State)
		}
		t.Fatalf("re-run after edit never reached a working coder (status=%q phase=%d err=%q agents=%v) — the new executor may have caught a stale command or the re-run stalled", r.Status, r.Phase, r.Error, st)
	}
	if r, _ := c.Get(id); r.Status != "running" {
		t.Errorf("re-run should be running, got %q", r.Status)
	}

	c.Cancel(id) // stop the sleeping coder + clean up worktrees
	deadline := time.Now().Add(8 * time.Second)
	wtRoot := filepath.Join(os.TempDir(), "candyland-wt", id)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(wtRoot); os.IsNotExist(err) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Cancelling the re-run must also tear down its worktrees (the run is now
	// untracked, so the deliveryConductor teardown drain won't cover it).
	if _, err := os.Stat(wtRoot); !os.IsNotExist(err) {
		t.Errorf("cancel after edit-rerun did not clean up worktrees at %s", wtRoot)
	}
}

// The progress bar must actually MOVE during a run, not sit at 0 until the end.
// recompute derives it from phase (+ coder completion during Build); the Go zero
// value would otherwise pin it at 0 for the whole run (the "stale, no feedback"
// a real run showed).
func TestProgressMovesWithPhaseAndTasks(t *testing.T) {
	last := len(run.Phases) - 1

	// Build (phase 0) with no coder green yet: the bar sits at the Build start.
	r := run.Run{Phase: run.PhaseBuild}
	recompute(&r)
	buildStart := r.Progress
	if buildStart < 0 || buildStart >= 1 {
		t.Errorf("Build start progress should be in [0,1), got %v", buildStart)
	}

	// Build, half the coders green: advances past the Build start, short of done.
	r = run.Run{Phase: run.PhaseBuild, Tasks: []run.Task{{State: "green"}, {State: "working"}}}
	recompute(&r)
	if !(r.Progress > buildStart && r.Progress < 1) {
		t.Errorf("half-green Build progress should advance past the Build start, got %v", r.Progress)
	}

	// A later phase (Integrate) moves the bar strictly forward from the Build start.
	r = run.Run{Phase: run.PhaseIntegrate}
	recompute(&r)
	if !(r.Progress > buildStart && r.Progress < 1) {
		t.Errorf("Integrate progress should be past Build start and below 1, got %v", r.Progress)
	}

	// PR phase (a clean finish) → fully complete.
	r = run.Run{Phase: last}
	recompute(&r)
	if r.Progress != 1 {
		t.Errorf("PR-phase progress should be 1, got %v", r.Progress)
	}

	// An errored run NEVER reads as 100% — even if it stalled at the last phase
	// (e.g. a push/PR-open failure). The bar must not imply a finish it never made.
	r = run.Run{Phase: last, Error: "Couldn't push the run branch"}
	recompute(&r)
	if r.Progress >= 1 {
		t.Errorf("an errored run must never show 100%%, got %v", r.Progress)
	}
}

// Archiving a tracked run sets Archived and it must STICK — a later executor
// Update (which republishes the whole run) must not clear it. (The untracked /
// terminal-run storage path is covered live by check-history.mjs.)
func TestArchiveTrackedRunSticks(t *testing.T) {
	c := New(nil)
	id := c.Create(run.Spec{Prompt: "x"})

	if !c.Archive(id) {
		t.Fatal("Archive should succeed for a tracked run")
	}
	if r, _ := c.Get(id); !r.Archived {
		t.Fatal("run should be archived after Archive")
	}
	// A later executor update must not un-archive the run.
	c.Update(id, func(r *run.Run) { r.StatusLine = "still working" })
	if r, _ := c.Get(id); !r.Archived {
		t.Error("a later Update cleared Archived — the flag must survive republishes")
	}
	if c.Archive("nope") {
		t.Error("archiving an unknown run should report false")
	}
}

// Cancelling an in-flight run stops it and drops it from active tracking (kept as
// "cancelled" in history — terminal, unlike stop which pauses for restart).
func TestCancelRunningRunStopsAndDropsFromTracking(t *testing.T) {
	c, _ := deliveryConductor(t, slowCoder)
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000")
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "2")

	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id, nil)

	// Wait until the coder is actually in flight.
	r := waitFor(t, c, id, func(r run.Run) bool {
		for _, a := range r.Agents {
			if a.ID == "a" && a.State == "working" {
				return true
			}
		}
		return false
	}, 20*time.Second)
	working := false
	for _, a := range r.Agents {
		if a.ID == "a" && a.State == "working" {
			working = true
		}
	}
	if !working {
		t.Fatal("coder never reached working state")
	}

	if !c.Cancel(id) {
		t.Fatal("Cancel should succeed for a running run")
	}
	if _, ok := c.Get(id); ok {
		t.Error("running run should be removed after Cancel")
	}

	// Cancel returns immediately, but the executor goroutine is still winding down
	// (killing its process tree, then cleaning its worktrees). Wait for that
	// cleanup to finish — both so we assert the worktrees are actually removed and
	// so the test's TempDir teardown doesn't race the goroutine's git calls.
	wtRoot := filepath.Join(os.TempDir(), "candyland-wt", id)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(wtRoot); os.IsNotExist(err) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(wtRoot); !os.IsNotExist(err) {
		t.Errorf("cancel did not clean up the run's worktrees at %s", wtRoot)
	}
}
