package conductor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// decideWatch is pure — it turns a PR's review state into exactly one action. These
// cases pin the /babysit exit condition (approval on the LATEST commit merges; an
// approval on a stale commit does NOT) and the changes-requested → feedback branch.
func TestDecideWatch(t *testing.T) {
	cases := []struct {
		name string
		pr   run.PRReview
		want run.WatchDecision
	}{
		{"approved on latest commit → merge",
			run.PRReview{State: "OPEN", ReviewDecision: "APPROVED", HeadSHA: "abc", ApprovedSHA: "abc"}, run.WatchMerge},
		{"approved on stale commit → wait",
			run.PRReview{State: "OPEN", ReviewDecision: "APPROVED", HeadSHA: "def", ApprovedSHA: "abc"}, run.WatchWait},
		{"approved but no approved commit resolved → wait",
			run.PRReview{State: "OPEN", ReviewDecision: "APPROVED", HeadSHA: "abc"}, run.WatchWait},
		{"changes requested → feedback",
			run.PRReview{State: "OPEN", ReviewDecision: "CHANGES_REQUESTED", HeadSHA: "abc"}, run.WatchFeedback},
		{"review pending → wait",
			run.PRReview{State: "OPEN", ReviewDecision: "REVIEW_REQUIRED", HeadSHA: "abc"}, run.WatchWait},
		{"no review yet → wait",
			run.PRReview{State: "OPEN", HeadSHA: "abc"}, run.WatchWait},
		{"merged upstream → done",
			run.PRReview{State: "MERGED", HeadSHA: "abc"}, run.WatchDone},
		{"closed upstream → done",
			run.PRReview{State: "CLOSED", HeadSHA: "abc"}, run.WatchDone},
		{"lowercase state still classified",
			run.PRReview{State: "open", ReviewDecision: "approved", HeadSHA: "abc", ApprovedSHA: "abc"}, run.WatchMerge},
	}
	for _, tc := range cases {
		if got := decideWatch(tc.pr); got != tc.want {
			t.Errorf("%s: decideWatch = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// babysitRun creates a tracked babysit run on a serverless conductor and initializes
// its watch state (as watchPR would), so watchOnce can be exercised directly.
func babysitRun(t *testing.T, c *Conductor) string {
	t.Helper()
	id := c.Create(run.Spec{Prompt: "ship it", Folders: []string{"/repo"}, Deliver: run.DeliverBabysit})
	c.Update(id, func(r *run.Run) {
		r.Watch = &run.WatchState{PR: 7, Repo: "repo", PRUrl: "https://x/pull/7", State: "watching"}
	})
	return id
}

// A watchOnce that sees an approval on the latest commit merges the PR, records a
// terminal tick, flips the watch state to "merged", and reports terminal=true.
func TestWatchOnceMerges(t *testing.T) {
	c := New(nil)
	var merged bool
	c.prReview = func(context.Context, string, int) (run.PRReview, error) {
		return run.PRReview{State: "OPEN", ReviewDecision: "APPROVED", HeadSHA: "h1", ApprovedSHA: "h1"}, nil
	}
	c.mergePR = func(context.Context, string, int) error { merged = true; return nil }

	id := babysitRun(t, c)
	if terminal := c.watchOnce(context.Background(), id, "repo", 7); !terminal {
		t.Fatal("watchOnce should be terminal after a merge")
	}
	if !merged {
		t.Fatal("mergePR was not called")
	}
	r, _ := c.Get(id)
	if r.Watch.State != "merged" {
		t.Fatalf("watch state = %q, want merged", r.Watch.State)
	}
	if n := len(r.Watch.Ticks); n != 1 || r.Watch.Ticks[0].Decision != run.WatchMerge {
		t.Fatalf("ticks = %+v, want one merge tick", r.Watch.Ticks)
	}
}

// A merge failure is transient: watchOnce records it as a non-terminal wait so a
// later tick retries once branch protection / conflicts clear — it must NOT abandon
// a healthy, approved PR.
func TestWatchOnceMergeFailureIsNotTerminal(t *testing.T) {
	c := New(nil)
	c.prReview = func(context.Context, string, int) (run.PRReview, error) {
		return run.PRReview{State: "OPEN", ReviewDecision: "APPROVED", HeadSHA: "h1", ApprovedSHA: "h1"}, nil
	}
	c.mergePR = func(context.Context, string, int) error { return context.DeadlineExceeded }

	id := babysitRun(t, c)
	if terminal := c.watchOnce(context.Background(), id, "repo", 7); terminal {
		t.Fatal("a failed merge must not be terminal")
	}
	r, _ := c.Get(id)
	if r.Watch.State != "watching" {
		t.Fatalf("watch state = %q, want still watching", r.Watch.State)
	}
	if r.Watch.Ticks[0].Decision != run.WatchWait {
		t.Fatalf("merge-failure tick decision = %q, want wait", r.Watch.Ticks[0].Decision)
	}
}

// Changes-requested dispatches a feedback fix ONCE per head commit: a second tick on
// the same head just waits for the in-flight fix, and a new head re-dispatches.
func TestWatchOnceFeedbackDedupPerCommit(t *testing.T) {
	c := New(nil)
	head := "h1"
	c.prReview = func(context.Context, string, int) (run.PRReview, error) {
		return run.PRReview{State: "OPEN", ReviewDecision: "CHANGES_REQUESTED", HeadSHA: head}, nil
	}
	dispatched := 0
	c.dispatchFeedback = func(string, string, int) string { dispatched++; return "child" }

	id := babysitRun(t, c)
	c.watchOnce(context.Background(), id, "repo", 7) // dispatch for h1
	c.watchOnce(context.Background(), id, "repo", 7) // same head → no re-dispatch
	if dispatched != 1 {
		t.Fatalf("dispatched = %d after two same-head ticks, want 1", dispatched)
	}
	head = "h2" // the fix pushed → head advanced → a fresh review requesting changes re-dispatches
	c.watchOnce(context.Background(), id, "repo", 7)
	if dispatched != 2 {
		t.Fatalf("dispatched = %d after head advanced, want 2", dispatched)
	}
	r, _ := c.Get(id)
	if r.Watch.Ticks[0].ChildRunID != "child" {
		t.Fatalf("first feedback tick missing child run id: %+v", r.Watch.Ticks[0])
	}
}

// A gh read error is a transient wait tick, never terminal.
func TestWatchOnceReadErrorIsTransient(t *testing.T) {
	c := New(nil)
	c.prReview = func(context.Context, string, int) (run.PRReview, error) {
		return run.PRReview{}, context.DeadlineExceeded
	}
	id := babysitRun(t, c)
	if terminal := c.watchOnce(context.Background(), id, "repo", 7); terminal {
		t.Fatal("a read error must not be terminal")
	}
	r, _ := c.Get(id)
	if r.Watch.Ticks[0].Decision != run.WatchWait {
		t.Fatalf("read-error tick = %q, want wait", r.Watch.Ticks[0].Decision)
	}
}

// The full loop: watchPR ticks through a review that first requests changes, then
// approves the latest commit, and exits by merging. It initializes the watch state
// itself and drives to the terminal merged outcome.
func TestWatchPRLoopMergesAfterFeedback(t *testing.T) {
	c := New(nil)
	c.watchInterval = time.Millisecond

	var mu sync.Mutex
	step := 0
	c.prReview = func(context.Context, string, int) (run.PRReview, error) {
		mu.Lock()
		defer mu.Unlock()
		step++
		switch step {
		case 1:
			return run.PRReview{State: "OPEN", ReviewDecision: "CHANGES_REQUESTED", HeadSHA: "h1"}, nil
		case 2:
			return run.PRReview{State: "OPEN", ReviewDecision: "REVIEW_REQUIRED", HeadSHA: "h2"}, nil
		default:
			return run.PRReview{State: "OPEN", ReviewDecision: "APPROVED", HeadSHA: "h2", ApprovedSHA: "h2"}, nil
		}
	}
	var merged bool
	c.mergePR = func(context.Context, string, int) error { merged = true; return nil }
	c.dispatchFeedback = func(string, string, int) string { return "child" }

	id := c.Create(run.Spec{Prompt: "ship it", Folders: []string{"/repo"}, Deliver: run.DeliverBabysit})

	done := make(chan struct{})
	go func() { c.watchPR(context.Background(), id, "repo", "https://x/pull/7", 7); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchPR did not terminate")
	}

	if !merged {
		t.Fatal("PR was never merged")
	}
	r, _ := c.Get(id)
	if r.Watch == nil || r.Watch.State != "merged" {
		t.Fatalf("watch state = %v, want merged", r.Watch)
	}
	if r.Watch.PR != 7 || r.Watch.PRUrl == "" {
		t.Fatalf("watch state not initialized with the PR: %+v", r.Watch)
	}
}

// A stop (ctx cancel) ends the watch phase with a recorded "stopped" outcome rather
// than merging or hanging.
func TestWatchPRStopsOnCancel(t *testing.T) {
	c := New(nil)
	c.watchInterval = 10 * time.Millisecond
	c.prReview = func(context.Context, string, int) (run.PRReview, error) {
		return run.PRReview{State: "OPEN", ReviewDecision: "REVIEW_REQUIRED", HeadSHA: "h1"}, nil // never mergeable
	}
	c.mergePR = func(context.Context, string, int) error { t.Fatal("must not merge"); return nil }

	id := c.Create(run.Spec{Prompt: "ship it", Folders: []string{"/repo"}, Deliver: run.DeliverBabysit})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.watchPR(ctx, id, "repo", "https://x/pull/7", 7); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchPR did not stop on cancel")
	}
	r, _ := c.Get(id)
	if r.Watch.State != "stopped" {
		t.Fatalf("watch state = %q, want stopped", r.Watch.State)
	}
}

// prNumberFromURL parses the trailing PR number from a gh PR URL and is robust to a
// trailing slash / unparseable tail (0).
func TestPRNumberFromURL(t *testing.T) {
	cases := map[string]int{
		"https://github.com/o/r/pull/42":  42,
		"https://github.com/o/r/pull/42/": 42,
		"https://github.com/o/r/pull/x":   0,
		"nonsense":                        0,
	}
	for url, want := range cases {
		if got := prNumberFromURL(url); got != want {
			t.Errorf("prNumberFromURL(%q) = %d, want %d", url, got, want)
		}
	}
}
