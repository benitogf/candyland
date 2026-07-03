package conductor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// Babysit post-delivery watch phase. A run delivered with Deliver=="babysit" opens
// its PR exactly like a normal "pr" run and THEN, instead of ending, hands the PR to
// this watch loop: on an interval it reads the PR's review state, dispatches a
// feedback fix when changes are requested, and merges once an approval lands on the
// PR's latest commit. It is a terminating loop — the merge (or an upstream
// merge/close, or a stop) is the exit — mirroring the /babysit skill.
//
// The gh reads/merge and the feedback-run dispatch are Conductor seams (set in New,
// overridden in tests), so the whole loop is deterministic without GitHub or claude.

// decideWatch is the pure decision at the heart of a watch tick: given a PR's current
// review state, decide whether to merge, dispatch a feedback fix, keep waiting, or
// exit because the PR already left the OPEN state. Kept side-effect-free so it is
// exhaustively unit-testable.
//
//   - MERGED/CLOSED upstream        → done (someone else finished it; stop watching)
//   - APPROVED on the LATEST commit → merge (the skill's exit condition)
//   - CHANGES_REQUESTED             → feedback (fix, then re-review)
//   - anything else (review pending,
//     or an approval on a STALE commit) → wait
func decideWatch(pr run.PRReview) run.WatchDecision {
	switch strings.ToUpper(pr.State) {
	case "MERGED", "CLOSED":
		return run.WatchDone
	}
	if strings.EqualFold(pr.ReviewDecision, "APPROVED") && pr.ApprovedSHA != "" && pr.ApprovedSHA == pr.HeadSHA {
		return run.WatchMerge
	}
	if strings.EqualFold(pr.ReviewDecision, "CHANGES_REQUESTED") {
		return run.WatchFeedback
	}
	return run.WatchWait
}

// watchOnce runs a single watch tick: read the PR's review state, decide, act, and
// record the tick + any resulting state transition onto the run's WatchState. It
// returns true when the watch reached a terminal state (merged or upstream
// done/closed) so the caller's loop should exit. A gh read error is a transient tick
// (recorded as a wait with the error detail) — never terminal — so a flaky network
// doesn't abandon a healthy PR.
func (c *Conductor) watchOnce(ctx context.Context, id, repo string, prNum int) (terminal bool) {
	pr, err := c.prReview(ctx, repo, prNum)
	if err != nil {
		c.recordWatchTick(id, run.WatchTick{Decision: run.WatchWait, Detail: "couldn't read PR review state: " + err.Error()})
		return false
	}

	decision := decideWatch(pr)
	tick := run.WatchTick{HeadSHA: pr.HeadSHA, Decision: decision}

	switch decision {
	case run.WatchMerge:
		if err := c.mergePR(ctx, repo, prNum); err != nil {
			// A merge failure (branch protection unmet, conflicts) is not terminal —
			// record it and keep watching so a later tick retries once the block clears.
			tick.Decision = run.WatchWait
			tick.Detail = fmt.Sprintf("approved on the latest commit but merge failed: %v", err)
			c.recordWatchTick(id, tick)
			return false
		}
		tick.Detail = "approval on the latest commit — merged"
		c.recordWatchTick(id, tick)
		c.finishWatch(id, "merged", fmt.Sprintf("PR #%d merged on approval.", prNum))
		return true

	case run.WatchFeedback:
		// Dispatch a feedback fix at most once per head commit: while a fix for the
		// current head is in flight, further changes-requested ticks just wait for it
		// to push (which advances the head) rather than piling on duplicate runs.
		if w := c.watchState(id); w != nil && w.LastFeedbackSHA == pr.HeadSHA {
			tick.Decision = run.WatchWait
			tick.Detail = "changes requested — a feedback fix is already in flight for this commit"
			c.recordWatchTick(id, tick)
			return false
		}
		child := c.dispatchFeedback(id, repo, prNum)
		tick.ChildRunID = child
		tick.Detail = "changes requested — dispatched a feedback fix"
		c.recordWatchTick(id, tick)
		c.Update(id, func(r *run.Run) {
			if r.Watch != nil {
				r.Watch.LastFeedbackSHA = pr.HeadSHA
			}
		})
		return false

	case run.WatchDone:
		st := strings.ToLower(pr.State)
		tick.Detail = fmt.Sprintf("PR is %s upstream — nothing left to watch", st)
		c.recordWatchTick(id, tick)
		c.finishWatch(id, "merged", fmt.Sprintf("PR #%d %s upstream.", prNum, st))
		return true

	default: // WatchWait
		tick.Detail = "no actionable review yet — waiting"
		c.recordWatchTick(id, tick)
		return false
	}
}

// watchPR is the babysit watch loop. It initializes the run's WatchState, then ticks
// watchOnce on watchInterval until the watch reaches a terminal state or ctx is
// cancelled (a stop). On stop it marks the watch "stopped" so a paused babysit run
// records why its watch phase ended. It blocks its caller (the executor goroutine),
// so a run only reports "done" once its PR is merged (or the watch is stopped).
func (c *Conductor) watchPR(ctx context.Context, id, repo, prURL string, prNum int) {
	now := time.Now().UTC().Format(time.RFC3339)
	c.Update(id, func(r *run.Run) {
		r.Watch = &run.WatchState{
			PR: prNum, Repo: repo, PRUrl: prURL, State: "watching",
			StartedAt: now, UpdatedAt: now,
		}
		r.StatusLine = fmt.Sprintf("Watching PR #%d — merging on approval, fixing on request…", prNum)
	})

	for {
		if c.watchOnce(ctx, id, repo, prNum) {
			return
		}
		select {
		case <-ctx.Done():
			c.finishWatch(id, "stopped", fmt.Sprintf("Stopped watching PR #%d.", prNum))
			return
		case <-time.After(c.watchInterval):
		}
	}
}

// recordWatchTick appends a tick to the run's WatchState (assigning its id/timestamp)
// and bumps UpdatedAt. A no-op if the watch state isn't initialized yet.
func (c *Conductor) recordWatchTick(id string, tick run.WatchTick) {
	c.Update(id, func(r *run.Run) {
		if r.Watch == nil {
			return
		}
		tick.ID = fmt.Sprintf("w%d", len(r.Watch.Ticks)+1)
		tick.At = time.Now().UTC().Format(time.RFC3339)
		r.Watch.Ticks = append(r.Watch.Ticks, tick)
		r.Watch.UpdatedAt = tick.At
	})
}

// finishWatch stamps the terminal watch state + outcome and surfaces it on the run's
// status line. state is "merged" or "stopped".
func (c *Conductor) finishWatch(id, state, outcome string) {
	c.Update(id, func(r *run.Run) {
		if r.Watch == nil {
			return
		}
		r.Watch.State = state
		r.Watch.Outcome = outcome
		r.Watch.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		r.StatusLine = outcome
	})
}

// watchState returns a snapshot of the run's WatchState (nil when absent).
func (c *Conductor) watchState(id string) *run.WatchState {
	r, ok := c.Get(id)
	if !ok || r.Watch == nil {
		return nil
	}
	w := *r.Watch
	return &w
}

// launchFeedbackRun is the default dispatchFeedback seam: it creates and begins a
// standalone feedback run against the watched PR, so a changes-requested review is
// addressed by the same fix→push path a manual /gh-feedback-work would take. The
// child run bases its work on the PR's head and pushes back (opening no new PR).
// Returns the child run id.
func (c *Conductor) launchFeedbackRun(parentID, repo string, prNum int) string {
	parent, ok := c.Get(parentID)
	if !ok {
		return ""
	}
	child := c.Create(run.Spec{
		Folders:  parent.Folders,
		Prompt:   fmt.Sprintf("Address the review feedback on PR #%d and push the fixes.", prNum),
		Title:    fmt.Sprintf("babysit feedback: PR #%d", prNum),
		Deliver:  run.DeliverFeedback,
		TargetPR: prNum,
	})
	c.Begin(child)
	return child
}
