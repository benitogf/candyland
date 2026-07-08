package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/candyland/internal/run"
)

// ClaudeExecutor runs REAL headless claude processes and turns their work into a
// pull request. It resolves the run's folders (supplied at launch) to a git repo,
// runs the tech lead (which emits a structured PARTITION per the detritus
// roles/tech-lead convention) in an isolated worktree, then spawns ONE coder per
// fork-safe task — each in its OWN git worktree so they run in parallel without
// colliding. Their commits are merged into the run branch in an integration
// worktree, which is pushed and turned into a single PR. Because every agent and
// the integration run in throwaway worktrees off the primary repo, the user's
// existing checkout of that repo is never switched or dirtied — the run's work
// lands on a dedicated branch. (The run's other folders are passed as --add-dir
// context the agent may also read and edit.) Stop kills every process; Restart
// re-runs from a clean slate.
type ClaudeExecutor struct{}

func (e *ClaudeExecutor) Name() string { return "claude" }

// claudeBin is the binary spawned; overridable for tests via CANDYLAND_CLAUDE.
func claudeBin() string {
	if b := os.Getenv("CANDYLAND_CLAUDE"); b != "" {
		return b
	}
	return "claude"
}

// streamLine is the subset of Claude Code's --output-format stream-json we map.
type streamLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Message   struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	Result string `json:"result"`
	Usage  struct {
		OutputTokens             int `json:"output_tokens"`
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// partitionTask is the shape the tech lead emits on a `PARTITION <json>` line.
type partitionTask struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Role  string   `json:"role"`
	Emoji string   `json:"emoji"`
	Files []string `json:"files"`
	Test  string   `json:"test"`
	Deps  []string `json:"deps"`
	Repo  string   `json:"repo"` // target repo (folder name); empty → the run's primary repo (folders[0])
}

func (e *ClaudeExecutor) Execute(c *Conductor, id string, control <-chan string) {
	run1 := func(ctx context.Context) chan struct{} {
		done := make(chan struct{})
		go func() { fanOut(ctx, c, id); close(done) }()
		return done
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := run1(ctx)
	// stopped tracks whether THIS executor was stopped, so the <-done branch
	// decides park-vs-finish from its own state — not by re-reading the shared
	// run status. Stop cancels ctx (which closes done); if we instead read
	// c.Get(id).Status there, a concurrent Edit→Begin re-plan that flips the
	// status away from "paused" before we read it makes us wrongly take the
	// finish branch and mark the run "done" — and a racing Begin then sees
	// "done" (not "planning") and never spawns the re-run's executor.
	stopped := false
	for {
		select {
		case cmd := <-control:
			switch cmd {
			case "stop":
				cancel()
				stopped = true
				c.Update(id, func(r *run.Run) { r.Status = "paused" })
			}
		case <-done:
			if stopped {
				// Stopped — park on the control channel only. Setting done to nil
				// stops this select from spinning on the now-closed done channel
				// (a busy loop). fanOut has fully unwound by the time done fires, so
				// no coder goroutine can re-write an agent after this — stamp every
				// still-in-flight agent terminal so the dashboard doesn't render a
				// killed agent as "Working" beside the run's stopped status.
				c.Update(id, func(r *run.Run) { stopInFlightAgents(r.Agents) })
				done = nil
				continue
			}
			c.Update(id, func(r *run.Run) {
				r.Status = "done"
				if r.Error == "" { // a clean finish reaches PR; an errored run stays where it stopped
					r.Phase = run.PhasePR
					r.Progress = 1
				}
			})
			if fin, _ := c.Get(id); fin.Error == "" {
				log.Printf("candyland: run %s done — %s", id, orDefault(fin.PrURL, "(no PR opened)"))
			}
			// Record the queryable audit now that the run's status is terminal
			// ("done", with Error set on a failure). A paused/stopped run took the
			// continue above and is not audited — it isn't a completed run.
			c.writeAudit(id)
			c.cleanupBusConfigs(id) // no more coder spawns — drop the --mcp-config files
			cancel()
			return
		}
	}
}

// fanOut runs the whole delivery: partition → code → integrate per impacted repo
// (reassessing the split when the tech lead's own plan fails), then push and open
// ONE PR PER IMPACTED REPO. A feature may span N repos (N≥1, no cap); the
// cross-repo half is in-scope, never a blocker. It never claims success it didn't
// achieve, and never fails the run for a problem of the tech lead's own making (a
// bad split, an unresolvable conflict) without first letting it reassess.
func fanOut(ctx context.Context, c *Conductor, id string) {
	r, ok := c.Get(id)
	if !ok {
		return
	}

	// Resolve the run's working folders (supplied at launch). Every folder is a
	// CANDIDATE repo: a task targets one via its `repo` field (default folders[0],
	// the primary). A missing/invalid primary is ENVIRONMENTAL — re-splitting can't
	// fix it, so it's honest and terminal, not a reason to re-plan.
	folders, err := c.folders(r)
	if err != nil {
		fail(ctx, c, id, "tl", "Couldn't resolve the run's folders: "+err.Error()+". Launch with at least one folder whose first entry is a git repository.")
		return
	}
	if len(folders) == 0 {
		fail(ctx, c, id, "tl", "The run has no folders. Launch it with at least one (the first is the git repository the run works in).")
		return
	}
	// Copy before expanding — c.folders may return the run's stored slice by
	// reference, and fanOut must not mutate shared run state outside c.Update.
	folders = append([]string(nil), folders...)
	for i, f := range folders {
		folders[i] = expandHome(f)
	}
	if !isGitRepo(ctx, folders[0]) {
		fail(ctx, c, id, "tl", "The run's first folder isn't a git repository: "+folders[0]+". A run branches and opens its PR there.")
		return
	}

	// Everything happens in throwaway worktrees under wtRoot/<repoBase>/…, so the
	// user's own checkouts are never switched or dirtied. Clean leftovers from a
	// prior attempt of THIS run (e.g. a restart), and again on the way out.
	wtRoot := filepath.Join(os.TempDir(), "candyland-wt", id)
	cleanup(c, id, folders, wtRoot)
	defer cleanup(c, id, folders, wtRoot)

	// ── Plan → code → integrate per repo, REASSESSING the split on a
	//    plan-attributable failure (a coder can't finish, or slices conflict). ──
	replans := maxReplans()
	feedback := ""
	var delivered map[string]string // repo path → integration worktree, ready to push
	for attempt := 1; attempt <= replans; attempt++ {
		if ctx.Err() != nil {
			return
		}
		if attempt > 1 {
			cleanup(c, id, folders, wtRoot)
			log.Printf("candyland: run %s re-planning (attempt %d/%d) after: %s", id, attempt, replans, feedback)
		}
		res := attemptDelivery(ctx, c, id, folders, r.Prompt, r.Branch, wtRoot, feedback, attempt)
		if res.ok {
			delivered = res.integDirs
			break
		}
		if res.replan == "" {
			return // terminal failure already recorded, or stopped — nothing to reassess
		}
		feedback = res.replan
		if attempt == replans {
			// K=3 escalation cap reached: give up rather than thrash quota, and
			// escalate the still-open task-graph nodes to blocked.
			// E3: the run's tech-lead escalates this DECISION one tier up (a quest child
			// → its quest-lead; a standalone run → the tech-lead itself, the top tier),
			// decided autonomously and recorded on the run (audit), never a human. A run
			// is atomic delivery with no sub-unit to drop-and-continue, so a resolved
			// decision is folded into the failure reason (you cannot fabricate a PR from
			// nothing) — the recorded escalation is the audit trail and fail() attaches
			// the postmortem. Unresolved hits the same wall at the decider → genuine block.
			esc, _ := c.escalateRunDecision(ctx, r,
				fmt.Sprintf("the tech-lead could not find a working task split after %d attempts (%s) — decide whether to accept a narrower split, re-scope the item, or block it", replans, feedback),
				folders[0], extraDirsFor(folders[0], folders))
			fail(ctx, c, id, "tl", fmt.Sprintf("Couldn't find a working task split after %d attempts. Last problem: %s. Escalated to %s, decided: %s", replans, feedback, esc.Decider, esc.Answer))
			c.escalateOpenNodes(fmt.Sprintf("no working task split after %d attempts: %s", replans, feedback))
			return
		}
	}
	if len(delivered) == 0 {
		return // defensive — a successful loop always delivers at least one repo
	}

	// ── Review: a SEPARATE reviewer agent hard-reviews each integrated diff before
	//    any PR opens. Blockers drive a bounded fix→re-review loop; only a clean
	//    review across every repo lets delivery proceed. ──
	// The reviewer judges intent fidelity, so its brief carries the run's driving
	// intent — OriginalIntent is the launch prompt captured once at creation; Prompt
	// is the fallback for records that predate it. Task titles name what the
	// partition set out to build. (Moved here from reviewUntilClean, which is now the
	// level-agnostic primitive.)
	taskIntent := r.OriginalIntent
	if taskIntent == "" {
		taskIntent = r.Prompt
	}
	var taskTitles []string
	if cur, ok := c.Get(id); ok {
		for _, t := range cur.Tasks {
			taskTitles = append(taskTitles, t.Title)
		}
	}
	if len(taskTitles) > 0 {
		lead := ""
		if taskIntent != "" {
			lead = "\n\n"
		}
		taskIntent += lead + "Partitioned tasks (what the loop set out to build):\n- " + strings.Join(taskTitles, "\n- ")
	}
	fixModel, fixThinking := c.agentConfig(RoleFix)
	// A standalone run has no higher layer to contradict, so its root-intent channel
	// stays DISARMED (RootIntent == "") — the render rule would otherwise arm it because
	// taskIntent carries the "Partitioned tasks…" suffix and would differ from the bare
	// OriginalIntent. Only a quest/campaign child carries a genuine root layer.
	rootIntent := ""
	if r.QuestID != "" {
		rootIntent = c.rootIntentFor(id)
	}
	if clean, _ := c.reviewUntilClean(ctx, id, delivered, r.Branch, taskIntent, rootIntent, fixModel, fixThinking); !clean {
		return // findings unresolved (or stopped) — never open a PR on un-reviewed work
	}

	// ── Branch delivery (campaign/quest-owned child): the run commits its work onto
	//    the shared parent branch (quest/<id> or campaign/<id> — the same name in each
	//    impacted repo) and opens NO PR — the parent opens one PR per repo at the end
	//    (Delivery & PR Policy: children never open PRs). Push the branch so the
	//    parent can collect the commits; record no PR. ──
	if r.Deliver == run.DeliverBranch {
		c.deliverToBranch(ctx, id, folders, delivered, r.Branch)
		return
	}

	// ── Feedback delivery: update an EXISTING PR in place. Integration was based on
	//    each repo's target-PR head; push the accumulated branch back onto that head
	//    (the PR updates) and record the existing PR's URL — open NO new PR. ──
	if r.Deliver == run.DeliverFeedback {
		c.deliverToFeedback(ctx, id, folders, delivered, r.Branch, r.TargetPR)
		return
	}

	// ── Review delivery: produce findings, open NO PR. When findings were applied
	//    it updates the target PR (effectively feedback); otherwise it ends as a
	//    review-only no-op with an empty prUrl by design. ──
	if r.Deliver == run.DeliverReview {
		c.deliverToReview(ctx, id, folders, delivered, r.Branch, r.TargetPR)
		return
	}

	// ── Deliver: push + open one PR PER IMPACTED REPO, in folder order. These are
	//    ENVIRONMENTAL (a missing 'origin' or an unauthenticated gh can't be fixed
	//    by re-splitting). PARTIAL-FAILURE ISOLATION: one repo's push/PR failure is
	//    surfaced on that repo's PR record but does NOT abort the others. The run
	//    reaches the PR phase if at least one PR opened. ──
	c.Update(id, func(r *run.Run) {
		r.StatusLine = "Pushing branches and opening pull requests…"
		setAgentState(r, "tl", "working", "pushing branches and opening PRs")
	})
	prs := make([]run.PR, 0, len(delivered))
	for _, repo := range orderedRepos(folders, delivered) {
		integDir := delivered[repo]
		base := prBase(ctx, repo) // the repo's DEFAULT branch, never the checkout (q4 fix)
		pr := run.PR{Repo: repoBase(repo)}
		if err := pushBranch(ctx, integDir, r.Branch); err != nil {
			pr.Err = "push failed: " + err.Error()
		} else if url, err := openPR(ctx, integDir, base, r.Branch, prTitle(r), prBody(r)); err != nil {
			pr.Err = "PR failed: " + err.Error()
		} else {
			pr.URL = url
		}
		prs = append(prs, pr)
	}
	crossLinkPRs(ctx, delivered, prs)

	opened := 0
	for _, pr := range prs {
		if pr.URL != "" {
			opened++
		}
	}
	c.Update(id, func(r *run.Run) {
		r.PRs = prs
		for _, pr := range prs {
			if pr.URL != "" {
				r.PrURL = pr.URL // primary/first opened — back-compat for the single-PR UI
				break
			}
		}
		if opened == 0 {
			r.Error = "No pull request could be opened. " + firstPRErr(prs) +
				" Check each repo has an 'origin' remote you can push to and that gh is authenticated."
			setAgentState(r, "tl", "blocked", "no PR opened")
			return
		}
		r.Phase = run.PhasePR // reached only now that a PR is open
		r.StatusLine = prStatusLine(prs)
		setAgentState(r, "tl", "done", prStatusLine(prs))
	})
	// E2: a delivery-stage block (no PR could be opened) is a capability/delivery
	// failure — it must carry a schema-valid postmortem too (not only the resilience
	// capability paths).
	if opened == 0 {
		c.attachRunPostmortem(id, c.blockerPostmortemFor("tl", "", "delivery: no pull request could be opened", firstPRErr(prs), 1, "branch "+r.Branch))
		return
	}

	// ── Babysit: a "babysit"-delivery run doesn't end when its PR opens — it hands
	//    the primary PR to the post-delivery watch loop, which merges on approval and
	//    dispatches feedback fixes until then. watchPR blocks (in this executor
	//    goroutine) until the PR merges or the run is stopped (ctx cancel). ──
	if r.Deliver == run.DeliverBabysit {
		primary, num := primaryPR(prs)
		if num > 0 {
			c.watchPR(ctx, id, primaryRepoDir(folders, delivered, primary), primary.URL, num)
		}
	}
}

// primaryPR returns the first successfully opened PR and its parsed number (0 when
// the number can't be parsed from the URL). It's the PR a babysit run watches.
func primaryPR(prs []run.PR) (run.PR, int) {
	for _, pr := range prs {
		if pr.URL == "" {
			continue
		}
		return pr, prNumberFromURL(pr.URL)
	}
	return run.PR{}, 0
}

// prNumberFromURL extracts the trailing PR number from a gh PR URL
// (…/pull/<n>). Returns 0 when there's no parseable trailing number.
func prNumberFromURL(url string) int {
	url = strings.TrimRight(url, "/")
	i := strings.LastIndex(url, "/")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(url[i+1:])
	if err != nil {
		return 0
	}
	return n
}

// primaryRepoDir resolves the integration worktree dir the primary PR was opened
// from, so the watch loop runs its gh commands in a repo that has the right remote.
// Falls back to the run's primary folder when the delivered map doesn't key it.
func primaryRepoDir(folders []string, delivered map[string]string, primary run.PR) string {
	for repo, dir := range delivered {
		if repoBase(repo) == primary.Repo {
			return dir
		}
	}
	if len(folders) > 0 {
		return folders[0]
	}
	return primary.Repo
}

// deliverToBranch is the delivery step for a campaign/quest-owned child run: it
// pushes each impacted repo's reviewed work onto the shared parent branch (quest/<id>
// for a standalone quest's children, campaign/<id> for a campaign's — the same name in
// each impacted repo) and opens NO pull request (children never open PRs — the parent
// opens one PR per repo at the end).
// The branch is r.Branch, set by the parent at launch. Pushing it makes the commits
// collectable by the parent; a push failure is recorded per repo (partial-failure
// isolation) but never opens a PR. When at least one repo's push lands the run reaches
// the PR phase as its terminal state — its "delivery" is the pushed branch, not a PR.
// If EVERY repo's push fails (pushed==0) the child delivered nothing: it records the
// error and blocks the agent instead of claiming the terminal PR phase.
func (c *Conductor) deliverToBranch(ctx context.Context, id string, folders []string, delivered map[string]string, branch string) {
	c.Update(id, func(r *run.Run) {
		r.StatusLine = "Pushing work onto the campaign branch (no PR — the campaign delivers)…"
		setAgentState(r, "tl", "working", "pushing onto "+branch)
	})
	prs := make([]run.PR, 0, len(delivered))
	for _, repo := range orderedRepos(folders, delivered) {
		integDir := delivered[repo]
		pr := run.PR{Repo: repoBase(repo)} // no URL: a branch-delivered child opens no PR
		if err := pushBranch(ctx, integDir, branch); err != nil {
			pr.Err = "push failed: " + err.Error()
		}
		prs = append(prs, pr)
	}
	// A branch-delivered child's delivery IS the pushed branch (no PR opens), so a
	// PR record with an empty Err is a landed commit. If EVERY repo's push failed the
	// child delivered nothing — that's a capability/delivery failure, not a silent
	// success: record the error, block the agent, and never claim the terminal PR
	// phase (the parent would otherwise expect commits that were never pushed).
	// Partial-failure isolation: at least one landed branch is a real (partial) delivery.
	pushed := branchDeliveryPushed(prs)
	c.Update(id, func(r *run.Run) {
		r.PRs = prs
		if pushed == 0 {
			r.Error = "Couldn't push onto the campaign branch " + branch + ". " + firstPRErr(prs) +
				" Check the repo has an 'origin' remote you can push to."
			setAgentState(r, "tl", "blocked", "no branch pushed")
			return
		}
		r.Phase = run.PhasePR
		r.StatusLine = "Committed onto " + branch + " — the campaign will open the PR after intent review."
		setAgentState(r, "tl", "done", "committed onto "+branch)
	})
	// E2: a delivery-stage block (nothing pushed) carries a schema-valid postmortem,
	// like the no-PR-opened and feedback-update blocks.
	if pushed == 0 {
		c.attachRunPostmortem(id, c.blockerPostmortemFor("tl", "", "delivery: could not push onto "+branch, firstPRErr(prs), 1, "branch "+branch))
	}
}

// branchDeliveryPushed counts the repos whose branch-delivery push landed (an empty
// Err on a PR-less branch record). Zero means the child delivered nothing.
func branchDeliveryPushed(prs []run.PR) int {
	pushed := 0
	for _, pr := range prs {
		if pr.Err == "" {
			pushed++
		}
	}
	return pushed
}

// feedbackBaseRef resolves the target PR's head branch to a local SHA (fetching it
// from origin), the base a feedback/review run's coders and integration build on so
// the work accumulates on top of the existing PR. An actionable error is returned
// on a resolution/fetch failure (environmental — re-splitting can't fix it).
func feedbackBaseRef(ctx context.Context, repo string, targetPR int) (string, error) {
	head, err := prHeadBranch(ctx, repo, targetPR)
	if err != nil {
		return "", fmt.Errorf("Couldn't resolve PR #%d's head branch in %s: %v", targetPR, repoBase(repo), err)
	}
	if _, err := git(ctx, repo, "fetch", "origin", head); err != nil {
		return "", fmt.Errorf("Couldn't fetch PR #%d's head branch %q in %s: %v", targetPR, head, repoBase(repo), err)
	}
	sha, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "FETCH_HEAD")
	if err != nil || sha == "" {
		return "", fmt.Errorf("Couldn't resolve PR #%d's head %q to a commit in %s", targetPR, head, repoBase(repo))
	}
	return sha, nil
}

// deliverToFeedback updates EXISTING PRs in place (D1): per impacted repo it
// pushes the integration worktree's accumulated commits (based on that repo's
// target-PR head) back onto the PR's head branch, so the PR updates — opening NO
// new PR. The run's PR result records the existing/updated PR's URL. A push or
// resolution failure is isolated per repo (partial-failure isolation).
func (c *Conductor) deliverToFeedback(ctx context.Context, id string, folders []string, delivered map[string]string, branch string, targetPR int) {
	c.Update(id, func(r *run.Run) {
		r.StatusLine = fmt.Sprintf("Pushing findings onto PR #%d (no new PR)…", targetPR)
		setAgentState(r, "tl", "working", fmt.Sprintf("updating PR #%d", targetPR))
	})
	prs := make([]run.PR, 0, len(delivered))
	for _, repo := range orderedRepos(folders, delivered) {
		integDir := delivered[repo]
		pr := run.PR{Repo: repoBase(repo)}
		head, err := prHeadBranch(ctx, repo, targetPR)
		if err != nil {
			pr.Err = fmt.Sprintf("resolve PR #%d head failed: %v", targetPR, err)
			prs = append(prs, pr)
			continue
		}
		// Push the integrated branch's tip onto the PR's head branch in place.
		if _, err := git(ctx, integDir, "push", "origin", branch+":"+head); err != nil {
			pr.Err = "push to PR head failed: " + err.Error()
		} else if url, err := prURL(ctx, repo, targetPR); err != nil {
			pr.Err = fmt.Sprintf("PR #%d URL lookup failed: %v", targetPR, err)
		} else {
			pr.URL = url
		}
		prs = append(prs, pr)
	}
	opened := 0
	for _, pr := range prs {
		if pr.URL != "" {
			opened++
		}
	}
	c.Update(id, func(r *run.Run) {
		r.PRs = prs
		for _, pr := range prs {
			if pr.URL != "" {
				r.PrURL = pr.URL
				break
			}
		}
		if opened == 0 {
			r.Error = fmt.Sprintf("Couldn't update PR #%d. %s", targetPR, firstPRErr(prs))
			setAgentState(r, "tl", "blocked", "no PR updated")
			return
		}
		r.Phase = run.PhasePR
		r.StatusLine = fmt.Sprintf("Updated PR #%d in place (no new PR).", targetPR)
		setAgentState(r, "tl", "done", fmt.Sprintf("updated PR #%d", targetPR))
	})
	// E2: a feedback delivery block (no PR could be updated) carries a postmortem too.
	if opened == 0 {
		c.attachRunPostmortem(id, c.blockerPostmortemFor("tl", "", fmt.Sprintf("delivery: could not update PR #%d", targetPR), firstPRErr(prs), 1, "branch "+branch))
	}
}

// deliverToReview is the review-delivery terminal step (D2): a review run opens NO
// PR. When the review/fix loop applied findings (the integration tip diverges from
// the target-PR head), it updates that PR in place — terminal "reviewed (findings
// applied to PR #N)". When nothing was applied it is a review-only no-op —
// "reviewed (no actionable findings)" with an empty prUrl by design.
func (c *Conductor) deliverToReview(ctx context.Context, id string, folders []string, delivered map[string]string, branch string, targetPR int) {
	applied := false
	if targetPR > 0 {
		for _, repo := range orderedRepos(folders, delivered) {
			if head, err := prHeadBranch(ctx, repo, targetPR); err == nil {
				if _, ferr := git(ctx, repo, "fetch", "origin", head); ferr == nil {
					// A non-empty DIFF between the PR head and the integrated branch means
					// a finding turned into a real change (a commit alone, with no content
					// delta, is not an applied finding).
					if out, err := git(ctx, delivered[repo], "diff", "--name-only", "FETCH_HEAD.."+branch); err == nil && strings.TrimSpace(out) != "" {
						applied = true
						break
					}
				}
			}
		}
	}
	if applied {
		// Findings were applied: update the PR in place (effectively feedback) and
		// record the honest terminal state.
		c.deliverToFeedback(ctx, id, folders, delivered, branch, targetPR)
		c.Update(id, func(r *run.Run) {
			if r.Error == "" {
				r.StatusLine = fmt.Sprintf("reviewed (findings applied to PR #%d)", targetPR)
				setAgentState(r, "tl", "done", r.StatusLine)
			}
		})
		return
	}
	c.Update(id, func(r *run.Run) {
		r.PRs = nil
		r.PrURL = "" // review-only: no PR by design
		r.Phase = run.PhasePR
		r.StatusLine = "reviewed (no actionable findings)"
		setAgentState(r, "tl", "done", "reviewed (no actionable findings)")
	})
}

// attemptDeliveryResult is the outcome of ONE partition → code → integrate pass.
type attemptDeliveryResult struct {
	integDirs map[string]string // repo path → integration worktree, ready to push (success only)
	ok        bool              // every impacted repo integrated cleanly
	// replan, when non-empty, is feedback for the tech lead: a failure of its OWN
	// plan (a coder couldn't finish, slices conflicted unresolvably) that warrants
	// re-partitioning. ok==false && replan=="" means a terminal failure was already
	// recorded (claude missing, tech lead can't partition at all) or the run stopped.
	replan string
}

// attemptDelivery runs the tech lead → coders → integrate flow once, ACROSS every
// impacted repo. The tech lead partitions all the work in one pass (its worktree
// is in the primary repo, with the other folders as --add-dir context); tasks are
// grouped by their target repo, and each repo's slice is coded + integrated into
// that repo's own run branch. On a re-plan the prior failure is woven into the
// brief so the tech lead produces a DIFFERENT breakdown.
func attemptDelivery(ctx context.Context, c *Conductor, id string, folders []string, prompt, branch, wtRoot, feedback string, attempt int) attemptDeliveryResult {
	primary := folders[0]
	tlModel, tlThinking := c.agentConfig(RoleTechLead)
	// ── Tech lead: partition the work (in its own worktree in the primary repo). ──
	c.Update(id, func(r *run.Run) {
		r.Status = "running"
		r.Phase = run.PhaseBuild
		tl := run.Agent{ID: "tl", Role: "Tech lead", Emoji: "🧭", Task: "partition · integrate · deliver",
			State: "working", Activity: "planning the partition", Budget: 800, Worktree: "wt/tl", Model: tlModel, Thinking: tlThinking,
			Events: []run.Event{{T: "system", Text: "tech-lead · claude -p --output-format stream-json"}}}
		if attempt > 1 {
			r.StatusLine = "Reassessing the task split and trying a different breakdown…"
			tl.Events = append(tl.Events, run.Event{T: "system", Text: fmt.Sprintf("re-planning (attempt %d): %s", attempt, feedback)})
		} else {
			r.StatusLine = "Tech lead is breaking the request into tasks…"
		}
		r.Agents = []run.Agent{tl}
		r.Tasks = []run.Task{}
	})
	base0, err := currentBranch(ctx, primary)
	if err != nil {
		fail(ctx, c, id, "tl", "Couldn't read the repo's current branch: "+err.Error())
		return attemptDeliveryResult{}
	}
	tlDir := filepath.Join(wtRoot, repoBase(primary), "tl")
	if err := addWorktree(ctx, primary, tlDir, branchName(id, "tl"), base0); err != nil {
		fail(ctx, c, id, "tl", "Couldn't create the tech lead's worktree: "+err.Error())
		return attemptDeliveryResult{}
	}
	c.putBrief("tl", bus.Brief{Role: "tech-lead", Prompt: prompt, Feedback: feedback, Attempt: attempt})
	// Fork the tech-lead doctrine template when one resolves — the template only
	// ADDS pre-loaded doctrine to the context; the prompt is unchanged. ok=false
	// degrades silently to today's cold start (no fork args, same bootstrap).
	tlOpts := spawnOpts{model: tlModel, thinking: tlThinking}
	if tpl, ok := c.templateForWorkdir(RoleTechLead, primary, tlDir); ok {
		tlOpts.forkFrom, tlOpts.fallbackPrompt = tpl, techLeadBootstrap
		tlOpts.onForkUnresolved = func() { c.invalidateTemplate(RoleTechLead, primary) }
		defer cleanupTemplateCopy(tpl, tlDir)
	}
	tasks := runAgentResilient(ctx, c, id, "tl", techLeadBootstrap, true, tlDir, extraDirsFor(primary, folders), tlOpts)
	if ctx.Err() != nil {
		return attemptDeliveryResult{} // stopped
	}
	if len(tasks) == 0 {
		// The tech lead couldn't produce a partition at all after its own retries
		// (recorded by runAgentResilient). Re-running the identical call wouldn't
		// help, so this is terminal — not a re-plan.
		return attemptDeliveryResult{}
	}

	// Write the partition DAG and the coder agents.
	coderModel, coderThinking := c.agentConfig(RoleCoder)
	c.Update(id, func(r *run.Run) {
		r.HasDag = true
		r.StatusLine = fmt.Sprintf("Coders are implementing %d %s…", len(tasks), plural(len(tasks), "task", "tasks"))
		r.Tasks = make([]run.Task, 0, len(tasks))
		for _, t := range tasks {
			r.Tasks = append(r.Tasks, run.Task{ID: t.ID, Title: t.Title, Files: t.Files, Test: t.Test, Owner: t.ID, State: "working", Deps: t.Deps})
			r.Agents = append(r.Agents, run.Agent{ID: t.ID, Role: orDefault(t.Role, "Coder"), Emoji: orDefault(t.Emoji, "⚙️"), Task: t.Title,
				State: "working", Activity: "implementing " + t.Title, Budget: 200, Worktree: "wt/" + t.ID, Model: coderModel, Thinking: coderThinking})
		}
		setAgentState(r, "tl", "integrating", "coordinating coders")
	})
	// Publish the partition into the coordination task-graph (bus) so coders can
	// graph_read the open work and the conductor can auto-unblock / escalate.
	c.publishGraphNodes(tasks)

	// ── Per impacted repo: code its slice, then integrate it into that repo's run
	//    branch. A coder/integration failure in ANY repo re-plans the whole split. ──
	order, byRepo := groupTasksByRepo(tasks, folders)
	c.Update(id, func(r *run.Run) {
		r.Phase = run.PhaseIntegrate
	})
	integDirs := make(map[string]string, len(order))
	for _, repo := range order {
		base, err := currentBranch(ctx, repo)
		if err != nil {
			fail(ctx, c, id, "tl", "Couldn't read "+repoBase(repo)+"'s current branch: "+err.Error())
			return attemptDeliveryResult{}
		}
		// Feedback/review base their coders AND integration on the target PR's head
		// (fetched), so the work accumulates on top of the PR — not off the default
		// branch (which would add/add-conflict against files the PR already changed).
		if cr, _ := c.Get(id); (cr.Deliver == run.DeliverFeedback || cr.Deliver == run.DeliverReview) && cr.TargetPR > 0 {
			head, herr := feedbackBaseRef(ctx, repo, cr.TargetPR)
			if herr != nil {
				fail(ctx, c, id, "tl", herr.Error())
				return attemptDeliveryResult{}
			}
			base = head
		}
		repoWt := filepath.Join(wtRoot, repoBase(repo))
		extra := extraDirsFor(repo, folders)
		rtasks := byRepo[repo]

		// Coders for this repo's tasks, each in its own worktree off the repo's base.
		runCoders(ctx, c, id, repo, base, repoWt, rtasks, extra)
		if ctx.Err() != nil {
			return attemptDeliveryResult{} // stopped
		}
		if cr, _ := c.Get(id); cr.Error != "" {
			reason := cr.Error
			if strings.HasPrefix(reason, startFailurePrefix) {
				return attemptDeliveryResult{} // claude couldn't start — environmental, terminal
			}
			c.Update(id, func(r *run.Run) { r.Error = "" })
			return attemptDeliveryResult{replan: "A coder couldn't complete its task: " + reason +
				" Re-split into smaller, clearer, fully self-contained tasks (or sequence dependent ones with deps)."}
		}

		// Integrate this repo's slice into its run branch.
		integDir, replan, ok := integrateRepo(ctx, c, id, repo, branch, base, repoWt, rtasks, extra)
		if !ok {
			return attemptDeliveryResult{replan: replan} // replan=="" when stopped/terminal
		}
		integDirs[repo] = integDir
	}
	return attemptDeliveryResult{integDirs: integDirs, ok: true}
}

// integrateRepo merges one repo's task branches into its run branch, in an
// integration worktree off that repo (the user's checkout stays untouched). It
// returns the worktree ready to push, or a re-plan reason. ok=false with an empty
// replan means the run was stopped or a terminal failure was already recorded.
func integrateRepo(ctx context.Context, c *Conductor, id, repo, branch, base, repoWt string, tasks []partitionTask, extra []string) (string, string, bool) {
	c.Update(id, func(r *run.Run) {
		r.StatusLine = "Integrating " + repoBase(repo) + " into one branch…"
		setAgentState(r, "tl", "integrating", "merging the slices")
	})
	integDir := filepath.Join(repoWt, "integrate")
	// A branch-delivered child shares ONE branch with its siblings (quest/<id> or
	// campaign/<id>, per the parent),
	// who run sequentially. If the shared branch already carries an earlier child's
	// commits, base this integration off that tip (resolved to a SHA so a later
	// branch move doesn't strand the base) so the work ACCUMULATES rather than resets.
	if cr, _ := c.Get(id); cr.Deliver == run.DeliverBranch {
		if sha, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", branch); err == nil && sha != "" {
			base = sha
		}
	}
	// Integrate in a DETACHED worktree at the base SHA — never checking out `branch`,
	// which a sibling/stale worktree may still hold. That sidesteps the "already used
	// by worktree" collision that used to hard-block every child run after the first;
	// the branch is (re)pointed at the integrated tip below via syncBranchRef.
	//
	// Feedback/review base resolution (the target-PR head SHA) is done by the caller
	// (attemptDelivery) and passed in as `base`, so coders and this integration share
	// the same base — no add/add conflict against files the PR already changed.
	if err := addDetachedWorktree(ctx, repo, integDir, base); err != nil {
		// A branch-checkout collision is RETRYABLE — re-plan (re-derive the base off
		// the branch tip) rather than hard-block; only a genuine failure is terminal.
		if isWorktreeCollision(err) {
			return "", "Couldn't create the integration worktree for " + repoBase(repo) + " (" + err.Error() +
				"). A worktree still holds the run branch — retry the integration.", false
		}
		fail(ctx, c, id, "tl", "Couldn't create the integration worktree for "+repoBase(repo)+": "+err.Error())
		return "", "", false
	}
	for _, t := range tasks {
		conflicted, files, err := mergeBranch(ctx, integDir, branchName(id, t.ID))
		if err != nil {
			abortMerge(integDir)
			return "", "Merging " + t.ID + " failed: " + err.Error() + " Re-partition so tasks own disjoint files.", false
		}
		if conflicted {
			if err := resolveConflict(ctx, c, id, repo, integDir, t, files, extra); err != nil {
				if ctx.Err() != nil {
					return "", "", false // stopped mid-resolution
				}
				abortMerge(integDir)
				return "", "Task " + t.ID + " conflicted with an earlier slice in " + strings.Join(files, ", ") +
					" and couldn't be reconciled (" + err.Error() + "). Re-partition so NO two tasks edit the same file, or sequence the dependent one with deps.", false
			}
			c.Update(id, func(r *run.Run) {
				r.StatusLine = "Resolved a merge conflict — integrating…"
				appendToAgent(r, "tl", run.Event{T: "system", Text: "resolved conflict in " + strings.Join(files, ", ")}, 0)
				setAgentState(r, "tl", "integrating", "merging the slices")
			})
		}
		if ctx.Err() != nil {
			return "", "", false
		}
	}
	// Publish the integrated tip under the run branch: the integration worktree is
	// detached, so update the local branch ref (push/PR resolve it) to HEAD.
	if err := syncBranchRef(ctx, integDir, branch); err != nil {
		fail(ctx, c, id, "tl", "Couldn't update the "+branch+" ref after integrating "+repoBase(repo)+": "+err.Error())
		return "", "", false
	}
	return integDir, "", true
}

// --- multi-repo helpers ---

// repoBase is the folder basename used to match a task's `repo` field and to name
// the per-repo worktree subdirectory. (Two folders sharing a basename is an
// unsupported edge case — runs pass distinct folders.)
func repoBase(path string) string { return filepath.Base(strings.TrimRight(path, "/")) }

// resolveRepo maps a task to its target repo path. The task's Repo names a folder
// (by path or basename); empty or unmatched → folders[0], the primary repo.
func resolveRepo(t partitionTask, folders []string) string {
	if t.Repo != "" {
		for _, f := range folders {
			if f == t.Repo || repoBase(f) == t.Repo || repoBase(f) == repoBase(t.Repo) {
				return f
			}
		}
	}
	return folders[0]
}

// groupTasksByRepo buckets tasks by their resolved repo, returning the impacted
// repos in folder order plus the per-repo task slices.
func groupTasksByRepo(tasks []partitionTask, folders []string) ([]string, map[string][]partitionTask) {
	byRepo := make(map[string][]partitionTask)
	for _, t := range tasks {
		repo := resolveRepo(t, folders)
		byRepo[repo] = append(byRepo[repo], t)
	}
	order := make([]string, 0, len(byRepo))
	for _, f := range folders {
		if len(byRepo[f]) > 0 {
			order = append(order, f)
		}
	}
	return order, byRepo
}

// orderedRepos returns the delivered repos in folder order (stable PR ordering).
func orderedRepos(folders []string, delivered map[string]string) []string {
	order := make([]string, 0, len(delivered))
	for _, f := range folders {
		if _, ok := delivered[f]; ok {
			order = append(order, f)
		}
	}
	return order
}

// extraDirsFor returns the --add-dir context for an agent working in `repo`:
// every OTHER folder, so it can read across repos without editing its own twice.
func extraDirsFor(repo string, folders []string) []string {
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		if f != repo {
			out = append(out, f)
		}
	}
	return out
}

// firstPRErr returns the first per-repo failure reason (for the run-level error
// when no PR opened at all).
func firstPRErr(prs []run.PR) string {
	for _, pr := range prs {
		if pr.Err != "" {
			return pr.Repo + ": " + pr.Err + "."
		}
	}
	return ""
}

// prStatusLine summarizes the delivery: "Opened 2 pull requests" / "Opened 1 of 2
// (other: …)" so a partial failure is visible, never papered over as full success.
func prStatusLine(prs []run.PR) string {
	opened, failed := 0, 0
	for _, pr := range prs {
		if pr.URL != "" {
			opened++
		} else {
			failed++
		}
	}
	if failed == 0 {
		return fmt.Sprintf("Opened %d pull %s.", opened, plural(opened, "request", "requests"))
	}
	return fmt.Sprintf("Opened %d of %d pull requests — %d repo(s) failed: %s", opened, opened+failed, failed, firstPRErr(prs))
}

// runCoders implements every task in parallel, each in its own worktree off base.
// A coder that fails (process error, or commit error) records the run error and
// blocks its agent; the caller decides whether that warrants a re-plan.
func runCoders(ctx context.Context, c *Conductor, id, repo, base, wtRoot string, tasks []partitionTask, extra []string) {
	coderModel, coderThinking := c.agentConfig(RoleCoder)
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func(t partitionTask) {
			defer wg.Done()
			wtDir := filepath.Join(wtRoot, t.ID)
			if err := addWorktree(ctx, repo, wtDir, branchName(id, t.ID), base); err != nil {
				fail(ctx, c, id, t.ID, "Couldn't create the worktree for "+t.ID+": "+err.Error())
				return
			}
			c.putBrief(t.ID, bus.Brief{Role: t.Role, Title: t.Title, Files: t.Files, Test: t.Test, Deps: t.Deps, Repo: repoBase(repo)})
			// Fork the coder doctrine template when one resolves (concurrent coders
			// coalesce into a single creation); ok=false is a silent cold start.
			opts := spawnOpts{model: coderModel, thinking: coderThinking}
			if tpl, ok := c.templateForWorkdir(RoleCoder, repo, wtDir); ok {
				opts.forkFrom, opts.fallbackPrompt = tpl, coderBootstrap
				opts.onForkUnresolved = func() { c.invalidateTemplate(RoleCoder, repo) }
				defer cleanupTemplateCopy(tpl, wtDir)
			}
			runAgentResilient(ctx, c, id, t.ID, coderBootstrap, false, wtDir, extra, opts)
			// Don't commit or claim success for a coder that failed (r.Error) or was
			// killed mid-flight by Stop/Restart (ctx cancelled).
			cr, _ := c.Get(id)
			if cr.Error != "" || ctx.Err() != nil {
				return
			}
			if _, err := commitAll(ctx, wtDir, "candyland("+t.ID+"): "+t.Title); err != nil {
				fail(ctx, c, id, t.ID, "Couldn't commit "+t.ID+"'s changes: "+err.Error())
				return
			}
			c.Update(id, func(r *run.Run) {
				setAgentState(r, t.ID, "green", "done")
				setTaskState(r, t.ID, "green")
			})
		}(t)
	}
	wg.Wait()
}

// plural picks the singular or plural noun for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// branchName is the throwaway branch a per-agent worktree commits to.
func branchName(runID, agentID string) string {
	return "candyland/" + runID + "/" + agentID
}

// cleanup removes this run's worktrees and throwaway per-agent branches across
// every impacted repo (best-effort). Worktrees live under wtRoot/<repoBase>/…, so
// each repo's are pruned against that repo. The run branch is left intact — it's
// the PR.
func cleanup(c *Conductor, id string, folders []string, wtRoot string) {
	ctx := context.Background()
	for _, repo := range folders {
		sub := filepath.Join(wtRoot, repoBase(repo))
		entries, _ := os.ReadDir(sub)
		for _, e := range entries {
			removeWorktree(ctx, repo, filepath.Join(sub, e.Name()))
			_, _ = git(ctx, repo, "branch", "-D", branchName(id, e.Name()))
		}
		_, _ = git(ctx, repo, "worktree", "prune")
	}
	_ = os.RemoveAll(wtRoot)
}

// crossLinkPRs adds a "Companion PRs" comment to each opened PR so the per-repo
// PRs of one multi-repo feature review together. Best-effort — a failed edit is
// logged, never fatal (the PRs are already open).
func crossLinkPRs(ctx context.Context, delivered map[string]string, prs []run.PR) {
	urls := make([]string, 0, len(prs))
	for _, pr := range prs {
		if pr.URL != "" {
			urls = append(urls, pr.URL)
		}
	}
	if len(urls) < 2 {
		return // a single PR has no siblings to link
	}
	dirByRepo := make(map[string]string, len(delivered))
	for repo, dir := range delivered {
		dirByRepo[repoBase(repo)] = dir
	}
	for _, pr := range prs {
		if pr.URL == "" {
			continue
		}
		others := make([]string, 0, len(urls)-1)
		for _, u := range urls {
			if u != pr.URL {
				others = append(others, u)
			}
		}
		note := "🍬 Companion PRs (same feature, other repos): " + strings.Join(others, ", ")
		if err := commentPR(ctx, dirByRepo[pr.Repo], pr.URL, note); err != nil {
			log.Printf("candyland: cross-link PR %s: %v", pr.URL, err)
		}
	}
}

// fail records an actionable run error and blocks the named agent. It never
// reports success — a failed delivery must be visible, not papered over. A
// cancelled ctx means OUR OWN stop killed the in-flight git/claude process, so
// that's not a failure: skip it and let the Execute loop settle into paused.
func fail(ctx context.Context, c *Conductor, id, agentID, msg string) {
	if ctx.Err() != nil {
		return
	}
	log.Printf("candyland: run %s failed at %s: %s", id, agentID, msg)
	c.Update(id, func(r *run.Run) {
		appendToAgent(r, agentID, run.Event{T: "text", Text: msg}, 0)
		r.Error = msg
		setAgentState(r, agentID, "blocked", "blocked")
	})
	// E2 invariant: no terminal `blocked` run without a schema-valid postmortem. This
	// is the single choke point for every run block (folders/worktree/integration/
	// delivery/reviewer failures) — a failed step IS a capability/delivery failure.
	// Synthesise one from the reason unless a richer one (the resilience capability
	// path) is already attached.
	if cur, ok := c.Get(id); ok && cur.Postmortem == nil {
		c.attachRunPostmortem(id, c.blockerPostmortemFor(agentID, "", msg, msg, 1, "branch "+cur.Branch))
	}
}

// openBranchPRs pushes `branch` and opens ONE PR per impacted repo the branch
// exists on, in folder order — the shared TERMINAL-delivery shape a campaign
// (deliverCampaign) and a converge quest (deliverQuest) both use. The PR base is
// each repo's DEFAULT branch (q4 fix), never the checkout. Partial-failure
// isolation: one repo's push/PR failure is recorded on that repo's PR record and
// never aborts the rest. A repo the branch does not exist on carries no work and is
// skipped.
func openBranchPRs(ctx context.Context, folders []string, branch, title, body string) []run.PR {
	prs := make([]run.PR, 0, len(folders))
	for _, repo := range folders {
		repo = expandHome(repo)
		if !isGitRepo(ctx, repo) {
			continue
		}
		if sha, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", branch); err != nil || sha == "" {
			continue
		}
		pr := run.PR{Repo: repoBase(repo)}
		base := prBase(ctx, repo) // the repo's DEFAULT branch, never the checkout (q4 fix)
		if err := pushBranch(ctx, repo, branch); err != nil {
			pr.Err = "push failed: " + err.Error()
		} else if url, ok := existingOpenPR(ctx, repo, branch); ok {
			pr.URL = url // idempotent re-finish: an open PR for this branch already exists — reuse it
		} else if url, err := openPR(ctx, repo, base, branch, title, body); err != nil {
			pr.Err = "PR failed: " + err.Error()
		} else {
			pr.URL = url
		}
		prs = append(prs, pr)
	}
	return prs
}

func prTitle(r run.Run) string {
	if t := strings.TrimSpace(r.Title); t != "" {
		return t
	}
	if p := strings.TrimSpace(r.Prompt); p != "" {
		return truncate(strings.SplitN(p, "\n", 2)[0], 72)
	}
	return "candyland run " + r.ID
}

func prBody(r run.Run) string {
	return "Delivered by a candyland run.\n\n## Request\n\n" + strings.TrimSpace(r.Prompt) +
		"\n\n" + provenanceFooter("run", r.ID)
}

// The spawn prompts below are CONSTANT bootstraps. The request, task spec, and
// any prior-attempt feedback ride in the agent's brief (brief/<agentID>, fetched
// via the brief_get MCP tool) — never on argv, which Windows caps at ~32k. Each
// keeps the role discriminators ("tech lead", "git merge conflict", the TEST /
// PARTITION lines) the resilience layer and the stub tests rely on.

// incidentDoctrine is the shared self-report clause appended to EVERY agent
// bootstrap (run/quest/campaign, full and fork-slim). It covers TWO classes of
// non-terminal event a future run should learn from: (1) a self-acknowledged
// mistake or doctrine violation — the agent did something wrong, skipped a step,
// or ignored a rule it was given; and (2) a worked-around problem — a flaky
// dependency, a stale lockfile, a substitutable missing env var. Neither stops
// the run, so the agent voluntarily emits an `INCIDENT <json>` line (severity
// info|warn|error) and keeps going; captureIncidents funnels every such
// self-report onto the host record's audit trail (run.Incidents / quest /
// campaign), which /learn later mines.
const incidentDoctrine = " Separate from and additional to any protocol/verdict line you must emit: if at any point you catch yourself in a mistake or doctrine violation (you did something wrong, skipped a step, ignored a rule you were given), OR you work around a non-terminal problem (a flaky dependency, a stale lockfile, a missing env var you can substitute) — anything a future run should learn from but that does NOT stop you — self-report it as one line beginning with `INCIDENT ` followed by JSON " +
	`{"summary":<one line what happened>,"detail":<optional: what you did about it>,"severity":"info"|"warn"|"error"}` +
	" and keep working. Never stop for an incident; a genuine blocker is a different thing."

const techLeadBootstrap = "You are the tech lead. Call the brief_get tool FIRST to read the request you must partition — it carries the full plan (and any previous failed attempt to avoid), so it is no longer on your command line. " +
	"Then emit exactly one line beginning with `PARTITION ` followed by a JSON array of fork-safe tasks: " +
	`[{"id","title","role","emoji","files":[],"test","deps":[]}]. ` +
	"Tasks must own DISJOINT files so they can be implemented and merged in parallel. " +
	"A single atomic task is a valid partition — when the work doesn't decompose, emit exactly one task (never treat \"one task\" as a failure). " +
	"For small, tightly-coupled backend+frontend work, emit one task with role \"fullstack\"; split large cross-domain work into separate tasks sequenced with \"deps\". " +
	"If the work spans more than one of the run's folders/repos, set each task's \"repo\" to the target folder's name (omit it for the primary repo); each impacted repo gets its own pull request. " +
	"Then stop." + incidentDoctrine

const coderBootstrap = "You are a coder. Call the brief_get tool FIRST to read your task — its title, the files you may touch, the defining test, and your role. " +
	"Implement the task until its defining test is green: make the changes with your tools — do not just describe them. " +
	"If your role is \"fullstack\", implement BOTH the server side and the client side of the slice and keep the API contract consistent between them. " +
	"When you run the defining test, report the result as one line beginning with `TEST ` " +
	`followed by JSON {"pass":<count>,"fail":<count>} (e.g. ` + "`TEST {\"pass\":3,\"fail\":0}`" +
	"), so the run records real verification counts." + incidentDoctrine

// resolveConflict has the tech lead reconcile a merge git couldn't auto-merge.
// The conflict markers are left in the integration worktree; the tech lead edits
// the conflicted files to combine both sides, we verify no markers remain, then
// complete the merge. Retries with a firmer prompt; if it genuinely can't resolve
// the conflict in place, the caller aborts the merge and REASSESSES the split (a
// re-plan) — only an exhausted re-plan budget fails the run. A real tech lead
// resolves conflicts, or re-thinks the breakdown — it doesn't abandon the run.
func resolveConflict(ctx context.Context, c *Conductor, id, repo, integDir string, t partitionTask, files, extra []string) error {
	attempts := maxAttempts()
	var lastErr error
	tlModel, tlThinking := c.agentConfig(RoleTechLead)
	// Conflict resolution is tech-lead work, so it forks the TECH-LEAD doctrine
	// template (computed once, before any spawn); ok=false is a silent cold start.
	tlOpts := spawnOpts{model: tlModel, thinking: tlThinking}
	if tpl, ok := c.templateForWorkdir(RoleTechLead, repo, integDir); ok {
		tlOpts.forkFrom = tpl
		tlOpts.onForkUnresolved = func() { c.invalidateTemplate(RoleTechLead, repo) }
		defer cleanupTemplateCopy(tpl, integDir)
	}
	c.putBrief("tl", bus.Brief{Role: "tech-lead", Title: "resolve merge conflict in " + t.Title, Files: files})
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.Update(id, func(r *run.Run) {
			setAgentState(r, "tl", "working", "resolving merge conflicts in "+strings.Join(files, ", "))
			if attempt == 1 {
				appendToAgent(r, "tl", run.Event{T: "system", Text: "merge conflict in " + strings.Join(files, ", ") + " — tech lead reconciling"}, 0)
			}
		})
		prompt := reinforce(conflictBootstrap, attempt, false)
		o := tlOpts
		if o.forkFrom != "" {
			o.fallbackPrompt = prompt // a failed fork reruns cold with this attempt's prompt
		}
		out := streamOnce(ctx, c, id, "tl", prompt, integDir, extra, o)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if out.startErr != nil {
			return fmt.Errorf("claude failed to start: %w", out.startErr)
		}
		if bad := unresolvedMarkers(integDir, files); len(bad) == 0 {
			if err := completeMerge(ctx, integDir, "candyland(integrate): resolve conflict from "+t.ID); err != nil {
				return err
			}
			log.Printf("candyland: run %s tl resolved merge conflict in %s", id, strings.Join(files, ", "))
			return nil
		} else if out.stalled {
			lastErr = fmt.Errorf("the integrator stalled before resolving %s", strings.Join(files, ", "))
		} else if out.runErr != nil {
			lastErr = fmt.Errorf("the integrator process exited: %s", firstLine(out.stderr))
		} else {
			lastErr = fmt.Errorf("conflict markers still present in %s", strings.Join(bad, ", "))
		}
	}
	return lastErr
}

// reviewFinding is one blocker a reviewer cites on a `REVIEW_FINDINGS <json>` line.
type reviewFinding struct {
	File  string `json:"file"`
	Line  int    `json:"line,omitempty"`
	Issue string `json:"issue"`
	// synthesized marks a blocker the CONDUCTOR fabricated (a narration-bounce of a
	// self-contradicting REVIEW_CLEAN), not one the reviewer cited. It carries no real
	// file, so splitFindings must keep it in the fix loop at every level rather than
	// class it structural and route it to a child run / remediation quest. Unexported
	// + json:"-" so a reviewer's parsed output can never set it.
	synthesized bool `json:"-"`
}

// reviewVerdict is the structured outcome a reviewer emits — either a single line
// `REVIEW_CLEAN` (no blockers) or `REVIEW_FINDINGS {"blockers":[…]}`.
type reviewVerdict struct {
	Blockers []reviewFinding `json:"blockers"`
}

// parseReview extracts the reviewer's structured verdict from its output, mirroring
// parsePartition/parseTest. A `REVIEW_CLEAN` line → (verdict, true) with no
// blockers; a `REVIEW_FINDINGS <json>` line → the parsed blockers. ok is false when
// neither line is present (the reviewer produced no verdict — treated as a failure
// by the caller, never as a silent pass). The last verdict line wins.
func parseReview(text string) (verdict reviewVerdict, ok bool) {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case ln == "REVIEW_CLEAN":
			verdict, ok = reviewVerdict{}, true
		case strings.HasPrefix(ln, "REVIEW_FINDINGS "):
			var v reviewVerdict
			if json.Unmarshal([]byte(strings.TrimPrefix(ln, "REVIEW_FINDINGS ")), &v) == nil {
				verdict, ok = v, true
			}
		}
	}
	return verdict, ok
}

// blockerAdmissions are blocker-class phrases that, if present in a verdict's
// narration, mean the reviewer DESCRIBED a real defect — so a REVIEW_CLEAN
// alongside them is self-contradicting and must not be accepted.
var blockerAdmissions = []string{
	"not wired", "not yet wired", "dead code", "no consumer", "unreachable", "regression",
}

// hedgeWords are confidence-laundering phrases: when a REVIEW_CLEAN leans on
// them it never PROVED the change works (it guessed), which the doctrine treats
// as an unproven pass — bounce it back demanding cited evidence.
var hedgeWords = []string{
	"plausibly", "presumably", "likely", "should be", "probably", "seems",
	"i assume", "sibling branch", "other branch", "not a genuine blocker",
}

// narrationProse strips quoted code from a reviewer's output — fenced ``` blocks
// and the git-diff hunks it pastes after running `git diff` — leaving only its own
// prose. The verdict-integrity detectors scan this prose, so a blocker-class
// KEYWORD that merely appears in the DIFF UNDER REVIEW (this file, for one, lists
// "unreachable" and "regression" as admission phrases) cannot masquerade as the
// reviewer's own admission and bounce an otherwise-clean verdict.
func narrationProse(text string) string {
	var b strings.Builder
	inFence, inDiff := false, false
	for _, ln := range strings.Split(text, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(t, "diff --git") || strings.HasPrefix(t, "@@") {
			inDiff = true
			continue
		}
		if inDiff {
			// A hunk's body/context/header lines start with +, -, a space, or \;
			// index/---/+++ headers too. Anything else ends the hunk (back to prose).
			if ln == "" || strings.HasPrefix(ln, "+") || strings.HasPrefix(ln, "-") ||
				strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\\") ||
				strings.HasPrefix(t, "index ") {
				continue
			}
			inDiff = false
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return b.String()
}

// negators are the words that, immediately before a blocker-class phrase, flip it
// from an admission into MITIGATING evidence: "this is not dead code", "there is no
// dead code", "it isn't unreachable". A reviewer citing that the change is wired and
// works — exactly what a bounced verdict is told to do — must not be re-bounced by
// the very phrase it is refuting.
var negators = []string{"not", "no", "isn't", "aren't", "wasn't", "never", "nor", "without"}

// quotedAt reports whether the phrase spanning [i, i+n) in lower is wrapped in
// matching backticks or double quotes — the reviewer QUOTING a phrase/identifier
// (e.g. naming the "dead code" admission string this file itself lists) rather than
// admitting the defect. narrationProse strips fenced/diff blocks but not inline
// quotations, so without this a reviewer discussing a change that is ABOUT
// blocker-class phrases would bounce its own clean verdict.
func quotedAt(lower string, i, n int) bool {
	if i == 0 || i+n >= len(lower) {
		return false
	}
	open, close := lower[i-1], lower[i+n]
	return (open == '`' && close == '`') || (open == '"' && close == '"')
}

// negatedAt reports whether the phrase found at index i in lower is preceded (within
// a few words) by a negator, making it mitigating rather than an admission.
func negatedAt(lower string, i int) bool {
	prefix := lower[:i]
	fields := strings.Fields(prefix)
	for j := len(fields) - 1; j >= 0 && j >= len(fields)-4; j-- {
		w := strings.Trim(fields[j], ".,;:!?\"'()")
		if strings.HasSuffix(w, "n't") {
			return true
		}
		for _, n := range negators {
			if w == n {
				return true
			}
		}
	}
	return false
}

// verdictBearingBlock returns the block of prose that carries the reviewer's verdict:
// the last verdict line (REVIEW_CLEAN / REVIEW_FINDINGS …) together with the nearest
// non-empty paragraph before it — the rationale the reviewer offers FOR that verdict.
// The hedge-word scan is confined here (see cleanVerdictContradictsNarration): a
// reviewer may legitimately hedge while EXPLORING ("this should be fine, let me
// check…") and then prove the change and stamp CLEAN. Only a hedge in the block that
// states the verdict's rationale means the CLEAN itself was guessed rather than
// proved. When no verdict line is present the whole prose is returned (the caller's
// scan then behaves as before).
func verdictBearingBlock(prose string) string {
	lines := strings.Split(prose, "\n")
	last := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "REVIEW_CLEAN" || strings.HasPrefix(t, "REVIEW_FINDINGS ") {
			last = i
		}
	}
	if last < 0 {
		return prose
	}
	start := last - 1
	for start >= 0 && strings.TrimSpace(lines[start]) == "" {
		start--
	}
	for start >= 0 && strings.TrimSpace(lines[start]) != "" {
		start--
	}
	return strings.Join(lines[start+1:last+1], "\n")
}

// cleanVerdictContradictsNarration reports whether a REVIEW_CLEAN's own narration
// undermines it: it contains a blocker-class admission (the reviewer described a
// real defect) OR a hedge word (the reviewer guessed rather than proved). It is a
// pure, separately-testable detector — the structural backstop behind the parsed
// verdict, so a model that narrates a problem then stamps CLEAN can't slip a PR
// through. reason names the first offending phrase for the bounced-back finding.
// It scans only the reviewer's PROSE (via narrationProse), never quoted diff/code,
// so a keyword present in the change under review isn't mistaken for an admission.
// A blocker phrase in a NEGATED/mitigating context ("not dead code", "isn't
// unreachable") is the reviewer refuting the defect, not admitting it, so it is not
// flagged — otherwise the cited-mitigating-evidence path a bounce demands could
// never clear. An inline-QUOTED occurrence (`dead code` / "dead code") is the
// reviewer naming the phrase (e.g. reviewing a change that is itself about these
// admission strings), not admitting the defect, so it is not flagged either.
// Blocker-class admissions are scanned across the whole prose (they describe a
// concrete defect wherever they appear), but the hedge-word scan is confined to the
// verdict-bearing block (see verdictBearingBlock) so exploratory hedging that the
// reviewer later resolved before stamping CLEAN doesn't bounce a proven verdict.
func cleanVerdictContradictsNarration(text string) (bad bool, reason string) {
	prose := narrationProse(text)
	lower := strings.ToLower(prose)
	for _, p := range blockerAdmissions {
		for from := 0; ; {
			idx := strings.Index(lower[from:], p)
			if idx < 0 {
				break
			}
			at := from + idx
			if !negatedAt(lower, at) && !quotedAt(lower, at, len(p)) {
				return true, "blocker-class admission in narration: " + strconv.Quote(p)
			}
			from = at + len(p)
		}
	}
	verdictLower := strings.ToLower(verdictBearingBlock(prose))
	for _, h := range hedgeWords {
		if strings.Contains(verdictLower, h) {
			return true, "hedged narration (no proof the change works): " + strconv.Quote(h)
		}
	}
	return false, ""
}

// reviewUntilClean runs a REAL review phase after integration and before any PR:
// a SEPARATE reviewer agent hard-reviews each repo's integrated diff (forking the
// reviewer doctrine template — core/review-rigor + truthseeker pre-loaded — or
// loading that doctrine via kb_get when no template resolves), and any blockers
// drive a bounded fix→re-review loop in that same integration worktree. It returns
// true only when every repo reviews clean (so the executor opens a PR), false when
// the round budget is exhausted with blockers still open (recording an honest run
// error and opening no PR) or the run was stopped.
func (c *Conductor) reviewUntilClean(ctx context.Context, hostID string,
	delivered map[string]string, branch string,
	taskIntent, rootIntent string,
	fixModel, fixThinking string) (clean bool, structural []reviewFinding) {
	rounds := maxReviewRounds()
	reviewerModel, reviewerThinking := c.agentConfig(RoleReviewer)
	// isRunHost: a run's own review folds structural findings back into the fix set
	// (there is no lower tier to decompose them), preserving today's behavior byte-
	// for-byte. A quest/campaign gate splits them out and returns them for the caller
	// to route to its decomposition owner.
	isRunHost := !strings.HasPrefix(hostID, "q") && !strings.HasPrefix(hostID, "c")
	totalRounds := 0
	// A run records ReviewRounds only on the all-clean path (byte-equivalence);
	// a quest/campaign gate accumulates GateRounds on every return (cumulative across
	// re-entries). `clean` is the named result read at defer time.
	defer func() { c.recordReviewRounds(hostID, totalRounds, clean) }()
	// The gate review loop's reviewer/repair/fix spawns consume tokens that must count
	// against a quest/campaign budget (the retired gate-2 spawn did); fold the total
	// into TokensUsed on the way out (a no-op at run level, whose tokens are already
	// tracked on the agent rollup).
	gateTokens := 0
	defer func() { c.addGateReviewTokens(hostID, gateTokens) }()
	c.updateAgentHost(hostID, func(agents *[]run.Agent) {
		a := ensureAgent(agents, reviewerID)
		a.Role, a.Emoji, a.Task = "Reviewer", "🔎", "review the integrated diff"
		a.State, a.Activity = "working", "loading review doctrine"
		a.Budget, a.Worktree, a.Model, a.Thinking = clampReviewBudget(400), "wt/review", reviewerModel, reviewerThinking
	})
	if isRunHost {
		c.Update(hostID, func(r *run.Run) {
			r.Phase = run.PhaseReview
			r.StatusLine = "Reviewing the integrated changes before opening a pull request…"
		})
	}
	folders := orderedDelivered(delivered)
	for _, repo := range folders {
		integDir := delivered[repo]
		base, _ := currentBranch(ctx, repo)
		// EVERY round-1 (and every fallback) forks the reviewer doctrine template
		// (computed once per repo) with the slim bootstrap — the template already
		// carries core/review-rigor + truthseeker. When no template resolves the round
		// is byte-for-byte today's cold spawn: the full kb_get bootstrap, no fork args.
		tpl, tplOK := c.templateForWorkdir(RoleReviewer, repo, integDir)
		if tplOK {
			defer cleanupTemplateCopy(tpl, integDir)
		}
		doctrineEvent := "reviewer · kb_get core/review-rigor + truthseeker — " + repoBase(repo)
		if tplOK {
			doctrineEvent = "reviewer · doctrine template fork (core/review-rigor + truthseeker) — " + repoBase(repo)
		}
		c.updateAgentHost(hostID, func(agents *[]run.Agent) {
			appendToAgentIn(agents, reviewerID, run.Event{T: "system", Text: doctrineEvent}, 0)
		})
		// The reviewer brief carries the diff command + both intent layers. It is
		// re-put before every FORK spawn (spawnReviewer) so a prior fix pass's brief on
		// the shared brief/<reviewerID> key can never shadow it; a resume round reads
		// nothing (it continues the session that already holds this brief).
		reviewerBrief := bus.Brief{Role: "reviewer", Title: "review " + repoBase(repo),
			Prompt: "git diff " + base + ".." + branch, Intent: taskIntent, RootIntent: rootIntent}

		// Reviewer session continuity: round 1 forks the template and captures its
		// session id; round ≥ 2 RESUMES it with a fresh-evidence reverify prompt so the
		// reviewer does not re-derive the brief/diff/tests it already holds.
		reviewerSession := ""
		// Remove EVERY reviewer transcript this repo's rounds minted (each fork and each
		// fallback fork opens a distinct session), on every exit path — alongside the
		// template-copy cleanup defer. The closure reads the final session set.
		var reviewerSessions []string
		defer func() {
			for _, s := range reviewerSessions {
				cleanupTemplateCopy(s, integDir)
			}
		}()
		var pendingReverify []reviewFinding // blockers the fix pass just addressed, to re-verify
		repoClean := false
		for round := 1; round <= rounds; round++ {
			if ctx.Err() != nil {
				return false, nil // stopped mid-review
			}
			totalRounds++
			if isRunHost {
				c.Update(hostID, func(r *run.Run) {
					r.StatusLine = fmt.Sprintf("Reviewing %s (round %d/%d)…", repoBase(repo), round, rounds)
				})
			}
			c.updateAgentHost(hostID, func(agents *[]run.Agent) {
				setAgentStateIn(agents, reviewerID, "working", fmt.Sprintf("reviewing %s (round %d/%d)", repoBase(repo), round, rounds))
			})
			out := c.spawnReviewer(ctx, hostID, repo, integDir, extraDirsForDelivered(repo, folders),
				round, tpl, tplOK, reviewerModel, reviewerThinking, pendingReverify, &reviewerSession, reviewerBrief)
			gateTokens += out.tokens
			if reviewerSession != "" && !slices.Contains(reviewerSessions, reviewerSession) {
				reviewerSessions = append(reviewerSessions, reviewerSession)
			}
			if ctx.Err() != nil {
				return false, nil
			}
			if out.startErr != nil {
				c.failReview(ctx, hostID, reviewerID, startFailurePrefix+out.startErr.Error()+". The reviewer couldn't start; no PR is opened on an un-reviewed change.")
				return false, nil
			}
			// collect and persist any INTENT_CONFLICT lines this reviewer flagged
			// against the root intent (context-only contradiction, not a local blocker).
			c.captureIntentConflicts(hostID, reviewerID, out.allText)
			// narration is the text the verdict-integrity gate scans — the ORIGINAL pass
			// by default, but the REPAIRED transcript when a repair produced the verdict.
			narration := out.allText
			verdict, ok := parseReview(out.allText)
			if !ok {
				// One bounded resume-and-re-ask before blocking on a missing verdict line.
				if rep, did := c.repairVerdict(ctx, hostID, reviewerID, reviewerSession, "REVIEW_CLEAN/REVIEW_FINDINGS", integDir, extraDirsForDelivered(repo, folders), reviewerModel, reviewerThinking); did {
					gateTokens += rep.tokens
					// The repair transcript is authoritative for both the narration check
					// and any conflict the reviewer flagged while re-asking.
					c.captureIntentConflicts(hostID, reviewerID, rep.allText)
					if v, ok2 := parseReview(rep.allText); ok2 {
						verdict, ok, narration = v, true, rep.allText
					} else {
						c.failReview(ctx, hostID, reviewerID, "The reviewer "+noVerdictReason(rep, reviewerID, "REVIEW")+" for "+repoBase(repo)+" — refusing to open a PR on an un-reviewed change.")
						return false, nil
					}
				} else {
					c.failReview(ctx, hostID, reviewerID, "The reviewer "+noVerdictReason(out, reviewerID, "REVIEW")+" for "+repoBase(repo)+" — refusing to open a PR on an un-reviewed change.")
					return false, nil
				}
			}
			// V3 verdict-integrity gate (unchanged): a REVIEW_CLEAN that contradicts its
			// own narration is bounced back as a synthesized blocker.
			if len(verdict.Blockers) == 0 {
				if bad, reason := cleanVerdictContradictsNarration(narration); bad {
					c.updateAgentHost(hostID, func(agents *[]run.Agent) {
						appendToAgentIn(agents, reviewerID, run.Event{T: "system", Text: "rejected REVIEW_CLEAN for " + repoBase(repo) + ": " + reason}, 0)
					})
					verdict.Blockers = []reviewFinding{{Issue: "REVIEW_CLEAN contradicts its own narration (" + reason + ") — cite mitigating evidence the change is wired and works, or emit an explicit blocker", synthesized: true}}
				}
			}
			if len(verdict.Blockers) == 0 {
				c.updateAgentHost(hostID, func(agents *[]run.Agent) {
					setAgentStateIn(agents, reviewerID, "green", "review clean: "+repoBase(repo))
					appendToAgentIn(agents, reviewerID, run.Event{T: "system", Text: "review clean: " + repoBase(repo)}, 0)
				})
				repoClean = true
				break // this repo is clean — review the next
			}
			// Split blockers into CITABLE (the fix agent can act on them directly) and
			// STRUCTURAL (empty file, or a file not present on the branch — no citable
			// decision to fix). At run level structural is folded back into citable.
			citable, structuralRound := splitFindings(ctx, integDir, branch, verdict.Blockers)
			if isRunHost {
				citable = append(citable, structuralRound...)
				structuralRound = nil
			}
			if round == rounds && len(citable) > 0 {
				c.failReview(ctx, hostID, reviewerID, fmt.Sprintf("Review of %s still has %d unresolved %s after %d rounds: %s. No PR is opened until review is clean.",
					repoBase(repo), len(citable), plural(len(citable), "blocker", "blockers"), rounds, firstFinding(citable)))
				return false, nil
			}
			if len(citable) > 0 {
				// Re-engage a fix agent on the citable subset, commit onto the branch.
				fixOK, fixTokens := c.fixReviewFindings(ctx, hostID, repo, integDir, branch, citable, extraDirsForDelivered(repo, folders), round, fixModel, fixThinking, taskIntent)
				gateTokens += fixTokens
				if !fixOK {
					return false, nil
				}
				pendingReverify = citable
			}
			if len(structuralRound) > 0 {
				// Non-citable findings need decomposition by the level above — return them
				// so the caller (quest/campaign gate) can route them into work items and
				// re-enter reviewUntilClean.
				return false, structuralRound
			}
		}
		if !repoClean {
			// Rounds exhausted with no clean verdict and nothing left to escalate.
			c.failReview(ctx, hostID, reviewerID, fmt.Sprintf("Review of %s did not converge in %d rounds. No PR is opened until review is clean.", repoBase(repo), rounds))
			return false, nil
		}
	}
	if isRunHost {
		c.Update(hostID, func(r *run.Run) {
			r.StatusLine = "Review clean — opening pull requests…"
		})
	}
	c.updateAgentHost(hostID, func(agents *[]run.Agent) {
		setAgentStateIn(agents, reviewerID, "done", "review clean")
	})
	return true, nil
}

// reviewContinuityEnabled is the Task-3 kill switch: CANDYLAND_REVIEW_CONTINUITY=0
// disables reviewer resume rounds (every round forks the template — today's behavior).
func reviewContinuityEnabled() bool { return os.Getenv("CANDYLAND_REVIEW_CONTINUITY") != "0" }

// spawnReviewer runs one reviewer round. Round 1 (and any fallback) forks the
// doctrine template with the slim/full bootstrap and captures the session id into
// *reviewerSession. Round ≥ 2, when continuity is enabled and a session was
// captured, RESUMES that session with the fresh-evidence reverify prompt so the
// reviewer does not re-derive context it already holds; a resume that terminal-fails
// clears the session and retries the same round via the fork path.
func (c *Conductor) spawnReviewer(ctx context.Context, hostID, repo, integDir string, extra []string,
	round int, tpl string, tplOK bool, reviewerModel, reviewerThinking string,
	reverify []reviewFinding, reviewerSession *string, reviewerBrief bus.Brief) attemptOutcome {
	forkSpawn := func() attemptOutcome {
		// Re-put the reviewer brief immediately before every FORK spawn: the reviewer
		// and the fix identity share brief/<reviewerID>, so a prior fix pass leaves the
		// FIX brief (no diff command) on that key. A cold/fork reviewer round would
		// otherwise brief_get the fix brief and review blind. (A resume round reads
		// nothing — it continues the session that already holds the reviewer brief.)
		c.putBrief(reviewerID, reviewerBrief)
		prompt := reviewBootstrap
		o := spawnOpts{maxTurns: reviewFixTurns(), model: reviewerModel, thinking: reviewerThinking}
		if tplOK {
			prompt = reviewBootstrapSlim
			o.forkFrom, o.fallbackPrompt = tpl, reviewBootstrap
			o.onForkUnresolved = func() { c.invalidateTemplate(RoleReviewer, repo) }
		}
		out := streamOnce(ctx, c, hostID, reviewerID, prompt, integDir, extra, o)
		if out.sessionID != "" {
			*reviewerSession = out.sessionID
		}
		return out
	}
	if round >= 2 && reviewContinuityEnabled() && *reviewerSession != "" {
		out := streamOnce(ctx, c, hostID, reviewerID, reviewReverifyPrompt(reverify), integDir, extra,
			spawnOpts{resumeFrom: *reviewerSession, maxTurns: reviewFixTurns(), model: reviewerModel, thinking: reviewerThinking})
		if !out.terminalFailed() && out.startErr == nil {
			return out
		}
		// A resume that terminal-failed: clear the session and retry via the fork path.
		c.updateAgentHost(hostID, func(agents *[]run.Agent) {
			appendToAgentIn(agents, reviewerID, run.Event{T: "system", Text: "reviewer resume failed — falling back to a fresh review round"}, 0)
		})
		*reviewerSession = ""
		return forkSpawn()
	}
	return forkSpawn()
}

// splitFindings partitions blockers into CITABLE (a fix agent can act on the cited
// file directly) and STRUCTURAL (empty file, or a file not present on the branch —
// no local citable decision). The predicate is `git cat-file -e <branch>:<file>`.
func splitFindings(ctx context.Context, dir, branch string, blockers []reviewFinding) (citable, structural []reviewFinding) {
	for _, b := range blockers {
		// A conductor-synthesized narration-bounce blocker has no real cited file but is
		// NOT structural — it must be resolved in the review/fix loop at every level, never
		// routed out to a child run / remediation quest with the reviewer's meta-text as
		// the objective.
		if b.synthesized {
			citable = append(citable, b)
			continue
		}
		if strings.TrimSpace(b.File) == "" || !fileOnBranch(ctx, dir, branch, b.File) {
			structural = append(structural, b)
			continue
		}
		citable = append(citable, b)
	}
	return citable, structural
}

// gateCleanup tears down a gate's detached review worktrees the proper way — a
// `git worktree remove` per repo (so the user's real repos carry no prunable
// registration in .git/worktrees), then removes the parent scratch dir.
func gateCleanup(ctx context.Context, delivered map[string]string, wtRoot string) {
	for repo, wt := range delivered {
		removeWorktree(ctx, repo, wt)
	}
	os.RemoveAll(wtRoot)
}

// branchHasDiff reports whether branch diverges from base in the repo at dir (so a
// gate has something to review). `git diff --quiet` exits non-zero when a diff exists.
func branchHasDiff(ctx context.Context, dir, base, branch string) bool {
	_, err := git(ctx, dir, "diff", "--quiet", base+".."+branch)
	return err != nil
}

// fileOnBranch reports whether file exists on branch in the repo at dir.
func fileOnBranch(ctx context.Context, dir, branch, file string) bool {
	if strings.TrimSpace(file) == "" {
		return false
	}
	_, err := git(ctx, dir, "cat-file", "-e", branch+":"+file)
	return err == nil
}

// failReview records a review-phase failure on the host that owns hostID: a run
// records the run error (fail), a quest blocks with a postmortem, a campaign blocks
// via blockCampaign — so the level-agnostic review primitive records honestly at
// every level rather than assuming a run.
func (c *Conductor) failReview(ctx context.Context, hostID, agentID, msg string) {
	switch {
	case strings.HasPrefix(hostID, "q"):
		c.attachQuestPostmortem(hostID, agentID, msg, msg)
		c.UpdateQuest(hostID, func(q *run.Quest) {
			if q.Status == "stopped" || q.Status == "done" {
				return
			}
			q.Status = "blocked"
			q.PauseReason = msg
		})
	case strings.HasPrefix(hostID, "c"):
		c.blockCampaign(hostID, msg)
	default:
		fail(ctx, c, hostID, agentID, msg)
	}
}

// recordReviewRounds folds the rounds a review loop consumed onto the host: a run
// records ReviewRounds (set), a quest/campaign gate accumulates GateRounds (cumulative
// across re-entries).
func (c *Conductor) recordReviewRounds(hostID string, n int, clean bool) {
	if n == 0 {
		return
	}
	switch {
	case strings.HasPrefix(hostID, "q"):
		c.UpdateQuest(hostID, func(q *run.Quest) { q.GateRounds += n })
	case strings.HasPrefix(hostID, "c"):
		c.UpdateCampaign(hostID, func(cam *run.Campaign) { cam.GateRounds += n })
	default:
		// Run-level byte-equivalence: ReviewRounds is set only when the run's
		// review reached the all-clean path, exactly as before the primitive extraction.
		if clean {
			c.Update(hostID, func(r *run.Run) { r.ReviewRounds = n })
		}
	}
}

// addGateReviewTokens folds the gate review loop's token usage into the host budget:
// a quest/campaign gate charges Quest/Campaign.TokensUsed (which drives the budget
// pause and effectiveTokenCap), matching the retired gate-2 spawn's accounting. A run
// is a no-op — its review tokens are already tracked on the agent rollup.
func (c *Conductor) addGateReviewTokens(hostID string, n int) {
	if n == 0 {
		return
	}
	switch {
	case strings.HasPrefix(hostID, "q"):
		c.UpdateQuest(hostID, func(q *run.Quest) { q.TokensUsed += n })
	case strings.HasPrefix(hostID, "c"):
		c.addCampaignTokens(hostID, n)
	}
}

// fixReviewFindings re-engages a fix agent in the integration worktree to address
// the reviewer's cited blockers and commits the fixes onto the run branch. It
// returns ok=true when the fixes were made and committed, false when the agent failed
// to act (error recorded) or the run was stopped, plus the fix spawn's token usage
// (for the gate budget accounting).
func (c *Conductor) fixReviewFindings(ctx context.Context, id, repo, integDir, branch string, blockers []reviewFinding, extra []string, round int, fixModel, fixThinking, intent string) (bool, int) {
	// C2 fail-fast: a fix pass with NO findings has nothing to act on. Never let it
	// run and silently re-derive its own task list (a context-blind agent then
	// explores the whole tree); record an explicit error and abort the pass.
	if len(blockers) == 0 {
		c.failReview(ctx, id, reviewerID, "A fix pass was requested for "+repoBase(repo)+" with no review findings — aborting (no PR is opened, and the pass does not silently re-derive work).")
		return false, 0
	}
	c.Update(id, func(r *run.Run) {
		r.StatusLine = fmt.Sprintf("Addressing %d review %s in %s…", len(blockers), plural(len(blockers), "finding", "findings"), repoBase(repo))
		setAgentState(r, reviewerID, "working", "fixing review findings in "+repoBase(repo))
		appendToAgent(r, reviewerID, run.Event{T: "system", Text: fmt.Sprintf("round %d: %d blocker(s) — fixing: %s", round, len(blockers), firstFinding(blockers))}, 0)
	})
	c.putBrief(reviewerID, bus.Brief{Role: "fix", Title: "address review findings in " + repoBase(repo), Findings: findingLines(blockers), Intent: intent})
	// C2 belt-and-suspenders: also carry the cited findings in the prompt text
	// itself, so they're impossible to miss even if the brief render drops them.
	// The fix identity forks its doctrine template when one resolves; the fallback
	// keeps the findings-carrying prompt so C2 survives a failed fork too.
	prompt := reviewFixPrompt(blockers)
	opts := spawnOpts{maxTurns: reviewFixTurns(), model: fixModel, thinking: fixThinking}
	if tpl, ok := c.templateForWorkdir(RoleFix, repo, integDir); ok {
		opts.forkFrom, opts.fallbackPrompt = tpl, prompt
		opts.onForkUnresolved = func() { c.invalidateTemplate(RoleFix, repo) }
		defer cleanupTemplateCopy(tpl, integDir)
	}
	out := streamOnce(ctx, c, id, reviewerID, prompt, integDir, extra, opts)
	if ctx.Err() != nil {
		return false, out.tokens // stopped mid-fix
	}
	if out.startErr != nil {
		c.failReview(ctx, id, reviewerID, startFailurePrefix+out.startErr.Error()+". The fix pass couldn't start.")
		return false, out.tokens
	}
	if !out.sawTool {
		c.failReview(ctx, id, reviewerID, "The fix pass made no changes for the review findings in "+repoBase(repo)+" — refusing to open a PR with open blockers.")
		return false, out.tokens
	}
	if _, err := commitAll(ctx, integDir, "candyland(review): address findings in "+repoBase(repo)); err != nil {
		c.failReview(ctx, id, reviewerID, "Couldn't commit the review fixes for "+repoBase(repo)+": "+err.Error())
		return false, out.tokens
	}
	// The integration worktree is detached; keep the run branch ref (what push/PR
	// resolve) tracking the review-fix commits that just landed on HEAD.
	if err := syncBranchRef(ctx, integDir, branch); err != nil {
		c.failReview(ctx, id, reviewerID, "Couldn't update the "+branch+" ref after review fixes for "+repoBase(repo)+": "+err.Error())
		return false, out.tokens
	}
	return true, out.tokens
}

// orderedDelivered returns the delivered repos in a stable order (map iteration is
// random; the review loop and its UI must be deterministic).
func orderedDelivered(delivered map[string]string) []string {
	out := make([]string, 0, len(delivered))
	for repo := range delivered {
		out = append(out, repo)
	}
	sort.Strings(out)
	return out
}

// extraDirsForDelivered exposes the OTHER delivered repos to a reviewer/fix agent
// as --add-dir context (so a cross-repo change reviews together), mirroring
// extraDirsFor over the delivered set.
func extraDirsForDelivered(repo string, repos []string) []string {
	out := make([]string, 0, len(repos))
	for _, f := range repos {
		if f != repo {
			out = append(out, f)
		}
	}
	return out
}

// firstFinding renders the first blocker for a one-line run error/status.
func firstFinding(blockers []reviewFinding) string {
	if len(blockers) == 0 {
		return ""
	}
	b := blockers[0]
	if b.Line > 0 {
		return fmt.Sprintf("%s:%d %s", b.File, b.Line, b.Issue)
	}
	if b.File != "" {
		return b.File + " " + b.Issue
	}
	return b.Issue
}

// findingLines renders the blockers as lines for the fix agent's brief.
func findingLines(blockers []reviewFinding) []string {
	out := make([]string, 0, len(blockers))
	for _, b := range blockers {
		out = append(out, firstFinding([]reviewFinding{b}))
	}
	return out
}

// reviewerID is the single reviewer agent that runs the review phase (and any fix
// passes) across every delivered repo, in sequence.
const reviewerID = "review"

const reviewBootstrap = "You are a code reviewer. Call the brief_get tool FIRST — it names the repo and the exact diff command to review." + briefGetToolHint + ". " +
	rootIntentReviewClause +
	"Load the detritus review doctrine via the kb_get tool: kb_get name=\"roles/reviewer\" AND kb_get name=\"core/review-rigor\" AND kb_get name=\"flows/principles/truthseeker\"; apply that rubric, do NOT improvise your own. " +
	"Review the integrated diff with the doctrine's rigor (run the diff command in the brief, read the changed files, hunt for blockers). " +
	"For any wiring- or assembly-dependent change, do NOT merely read the diff and trust `go test`: ASSEMBLE AND RUN THE BINARY, and TRACE reachability of the changed feature from the program entrypoint. " +
	"The brief also carries the run's driving INTENT — what was asked for. Verify the diff SATISFIES it: an intent commitment that is missing, only partially delivered, or contradicted is a BLOCKER (issue: \"intent unmet: <commitment>\"); REVIEW_CLEAN asserts intent fidelity as well as defect absence. " +
	"If you cannot PROVE the feature is actually wired in and reachable from the entrypoint, that is a BLOCKER — emit it as a finding (issue: \"wiring unproven: <feature> not shown reachable from entrypoint\"); do NOT emit REVIEW_CLEAN on unproven wiring. " +
	"Then emit EXACTLY ONE verdict line and stop: either `REVIEW_CLEAN` (no blockers) " +
	"OR `REVIEW_FINDINGS ` followed by JSON " + `{"blockers":[{"file":"path","line":12,"issue":"…"}]}` +
	" listing only genuine blockers (cite file and line per the doctrine). Do not ask questions." + incidentDoctrine

// reviewBootstrapSlim is the bootstrap for a reviewer forked from the doctrine
// template: the forked session already carries core/review-rigor + truthseeker
// in context, so the kb_get load instruction is dropped — everything else is
// reviewBootstrap verbatim. A spawn with no template (and a fork that fails to
// resolve) uses the full reviewBootstrap, whose kb_get load stands on its own.
const reviewBootstrapSlim = "You are a code reviewer. Call the brief_get tool FIRST — it names the repo and the exact diff command to review." + briefGetToolHint + ". " +
	rootIntentReviewClause +
	"Review the integrated diff with the doctrine's rigor (run the diff command in the brief, read the changed files, hunt for blockers). " +
	"For any wiring- or assembly-dependent change, do NOT merely read the diff and trust `go test`: ASSEMBLE AND RUN THE BINARY, and TRACE reachability of the changed feature from the program entrypoint. " +
	"The brief also carries the run's driving INTENT — what was asked for. Verify the diff SATISFIES it: an intent commitment that is missing, only partially delivered, or contradicted is a BLOCKER (issue: \"intent unmet: <commitment>\"); REVIEW_CLEAN asserts intent fidelity as well as defect absence. " +
	"If you cannot PROVE the feature is actually wired in and reachable from the entrypoint, that is a BLOCKER — emit it as a finding (issue: \"wiring unproven: <feature> not shown reachable from entrypoint\"); do NOT emit REVIEW_CLEAN on unproven wiring. " +
	"Then emit EXACTLY ONE verdict line and stop: either `REVIEW_CLEAN` (no blockers) " +
	"OR `REVIEW_FINDINGS ` followed by JSON " + `{"blockers":[{"file":"path","line":12,"issue":"…"}]}` +
	" listing only genuine blockers (cite file and line per the doctrine). Do not ask questions." + incidentDoctrine

const reviewFixBootstrap = "You are addressing review findings on an integrated branch before it opens as a pull request. " +
	"Call the brief_get tool FIRST to read the cited findings (file, line, issue)." + briefGetToolHint + ". " +
	"Fix every cited blocker with your editing tools — make the changes, do not just describe them — and keep the existing tests green. " +
	"The brief also carries the run's driving intent — address the findings in service of it, not just to the letter. " +
	"Do not ask questions and do not defer; resolve all the findings in this run." + incidentDoctrine

// reviewFixCeiling is the hard per-pass turn ceiling for the review/fix identity
// (C3). A context-blind fix agent left uncapped can blow up — the incident saw
// ~84 tool calls plus a sub-agent escalation. This ceiling is enforced two ways
// that AGREE: the review and fix spawns pass it as claude's --max-turns (a real,
// process-level hard cap that aborts the run after this many agentic turns — see
// reviewFixTurns / streamOnce's spawnOpts), and the agent's displayed Budget is
// clamped to it. maxReviewRounds() separately bounds the TOTAL number of passes.
const reviewFixCeiling = 40

// reviewFixTurns is the hard --max-turns cap threaded into every review and fix
// spawn — the SAME value as the displayed budget clamp (clampReviewBudget), so the
// real enforcement and the shown ceiling never diverge. Tunable downward via
// CANDYLAND_REVIEW_BUDGET (clampReviewBudget honors it), never above the ceiling.
func reviewFixTurns() int { return clampReviewBudget(reviewFixCeiling) }

// clampReviewBudget caps a proposed reviewer/fix budget at reviewFixCeiling so a
// single pass has an explicit, testable per-pass ceiling. Tunable downward via
// CANDYLAND_REVIEW_BUDGET, but never above the hard ceiling.
func clampReviewBudget(proposed int) int {
	ceiling := reviewFixCeiling
	if v := envInt("CANDYLAND_REVIEW_BUDGET", 0); v > 0 && v < ceiling {
		ceiling = v
	}
	if proposed > ceiling {
		return ceiling
	}
	return proposed
}

// reviewFixPrompt builds the fix-pass prompt, carrying the cited findings inline
// (C2 belt-and-suspenders) and restating the per-pass ceiling (C3) so a
// context-blind agent self-limits its exploration. The prompt text is advisory;
// the HARD bound is the --max-turns cap streamOnce passes (reviewFixTurns) plus
// maxReviewRounds() on the pass count.
func reviewFixPrompt(blockers []reviewFinding) string {
	var b strings.Builder
	b.WriteString(reviewFixBootstrap)
	b.WriteString("\n\n--- CITED FINDINGS (also in your brief) ---\n")
	for _, ln := range findingLines(blockers) {
		b.WriteString("- ")
		b.WriteString(ln)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nStay focused: address ONLY these findings. Do not explore the wider tree or spawn sub-agents; this pass is budget-capped at %d tool calls.", reviewFixCeiling)
	return b.String()
}

// briefGetToolHint is the exact deferred-tool naming appended to every
// "Call the brief_get tool FIRST" sentence, so an agent loads the tool by a single
// ToolSearch selection rather than an expensive keyword search that repeats per round.
const briefGetToolHint = " (load it with a single ToolSearch call: query \"select:mcp__candyland-comms__brief_get\", then call the tool; do not search by keywords)"

// rootIntentReviewClause is the two-layer-intent instruction appended to the
// reviewer bootstraps: it authorizes flagging a CONTRADICTION with the root intent as a
// one-line INTENT_CONFLICT (routed to a ruling one tier up), while forbidding a
// completeness demand — sibling work the reviewer cannot see owns the rest.
const rootIntentReviewClause = "If the brief carries a ROOT INTENT section: you can judge whether this diff CONTRADICTS the root intent; you cannot judge whether the root intent is fully delivered — sibling work you cannot see owns the rest. Absence of a root-intent commitment is NEVER a finding. A genuine contradiction is NOT a blocker either: report it as one line `INTENT_CONFLICT ` followed by JSON {\"issue\":\"…\"} in addition to your verdict line, and keep your verdict scoped to the task brief. "

// reviewReverifyBootstrap is the round-≥2 prompt for a RESUMED reviewer session: the
// fix identity has addressed the cited blockers, so the reviewer re-verifies with
// fresh evidence WITHOUT re-deriving context it already holds this session.
const reviewReverifyBootstrap = "The fix identity has addressed your cited blockers (listed below); the fixes are committed on the branch you are reviewing. Re-verify with FRESH evidence: diff the new commits, re-run the acceptance checks you already established in this session, and confirm each cited blocker is resolved and nothing regressed. Do not re-derive context you already hold. Then emit EXACTLY ONE verdict line and stop: REVIEW_CLEAN or REVIEW_FINDINGS <json>. " + rootIntentReviewClause + incidentDoctrine

// reviewReverifyPrompt builds the round-≥2 reverify prompt, carrying the just-fixed
// blockers rendered exactly as reviewFixPrompt renders them (reuse findingLines).
func reviewReverifyPrompt(blockers []reviewFinding) string {
	var b strings.Builder
	b.WriteString(reviewReverifyBootstrap)
	b.WriteString("\n\n--- CITED BLOCKERS (now addressed on the branch) ---\n")
	for _, ln := range findingLines(blockers) {
		b.WriteString("- ")
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return b.String()
}

const conflictBootstrap = "You are the tech lead resolving a git merge conflict while integrating parallel work into one branch. " +
	"Call the brief_get tool FIRST to read which task conflicted and the conflicted files. " +
	"Open each conflicted file and reconcile the two sides so BOTH changes are preserved and the result is correct — " +
	"remove every git conflict marker (<<<<<<<, =======, >>>>>>>). Use your editing tools to write the resolved files. " +
	"Do not ask questions and do not leave any conflict unresolved." + incidentDoctrine

// mapAgentLine streams one stream-json line into the agent's live ooo state and
// returns the signals the resilience layer uses to judge compliance: any parsed
// partition, whether a tool was used (real work), and the latest text (checked
// for deferral / a question to the user).
func mapAgentLine(c *Conductor, id, agentID string, line streamLine) (partition []partitionTask, sawTool bool, text string) {
	switch line.Type {
	case "assistant":
		for _, blk := range line.Message.Content {
			b := blk
			if b.Type == "text" && b.Text != "" {
				if p := parsePartition(b.Text); p != nil {
					partition = p
				}
				if pass, fail, ok := parseTest(b.Text); ok {
					c.recordAgentEvent(id, func(agents *[]run.Agent) {
						appendToAgentIn(agents, agentID, run.Event{T: "test", Pass: pass, Fail: fail}, 0)
					})
				}
				text = b.Text
				c.recordAgentEvent(id, func(agents *[]run.Agent) {
					appendToAgentIn(agents, agentID, run.Event{T: "text", Text: b.Text}, 0)
				})
			}
			if b.Type == "tool_use" {
				sawTool = true
				in := string(b.Input)
				summary := truncate(in, 200)
				c.recordAgentEvent(id, func(agents *[]run.Agent) {
					appendToAgentIn(agents, agentID, run.Event{T: "tool", Name: b.Name, Input: summary, InputFull: fullWhenTruncated(summary, in)}, 0)
					ensureAgent(agents, agentID).ToolCalls++ // L2 telemetry: tool-call count
				})
			}
		}
	case "result":
		l := line
		if l.Result != "" {
			text = l.Result
		}
		summary := truncate(l.Result, 300)
		c.recordAgentEvent(id, func(agents *[]run.Agent) {
			appendToAgentIn(agents, agentID, run.Event{T: "result", Text: summary, TextFull: fullWhenTruncated(summary, l.Result)}, l.Usage.OutputTokens/1000)
			// Raw input-side usage accumulates unscaled alongside the /1000 output
			// display count above (Tokens keeps its display semantics untouched).
			a := ensureAgent(agents, agentID)
			a.InputTokens += l.Usage.InputTokens
			a.CacheReadTokens += l.Usage.CacheReadInputTokens
			a.CacheCreationTokens += l.Usage.CacheCreationInputTokens
		})
	}
	return partition, sawTool, text
}

// parsePartition extracts the task array from a `PARTITION <json>` line.
func parsePartition(text string) []partitionTask {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "PARTITION ") {
			continue
		}
		var tasks []partitionTask
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "PARTITION ")), &tasks) == nil && len(tasks) > 0 {
			// A task id becomes a worktree path component and a git branch ref, and
			// dep references are matched against task ids by the bus auto-unblock.
			// The id comes from the (local) tech-lead model, so normalize every id
			// and dep through the same slug: a malformed id can't escape the
			// worktree root or break ref creation, and ids stay consistent with the
			// deps that reference them. Realistic ids (a, backend, csv-export) are
			// unchanged by slug.
			seen := make(map[string]bool, len(tasks))
			for i := range tasks {
				tasks[i].ID = slug(tasks[i].ID)
				for j := range tasks[i].Deps {
					tasks[i].Deps[j] = slug(tasks[i].Deps[j])
				}
				// Ensure ids are UNIQUE: a task id keys the brief, the bus agent, the
				// worktree dir, and the git branch — a collision (more likely now that
				// one partition spans multiple repos) would silently overwrite the
				// first. Suffix duplicates; deps still resolve to the first occurrence
				// (the bus auto-unblock is a best-effort hint, not a hard dependency).
				base, uid := tasks[i].ID, tasks[i].ID
				for k := 2; seen[uid]; k++ {
					uid = fmt.Sprintf("%s-%d", base, k)
				}
				tasks[i].ID = uid
				seen[uid] = true
			}
			return tasks
		}
	}
	return nil
}

// parseTest extracts a verification result from a `TEST <json>` line emitted by
// an agent (e.g. `TEST {"pass":12,"fail":0}`), mirroring parsePartition. The
// last such line on the agent's stream wins. ok is false when no TEST line is
// present, so a plain text block is left untouched.
func parseTest(text string) (pass, fail int, ok bool) {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "TEST ") {
			continue
		}
		var res struct {
			Pass int `json:"pass"`
			Fail int `json:"fail"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "TEST ")), &res) == nil {
			pass, fail, ok = res.Pass, res.Fail, true
		}
	}
	return pass, fail, ok
}

func setAgentState(r *run.Run, agentID, state, activity string) {
	setAgentStateIn(&r.Agents, agentID, state, activity)
}

// setAgentStateIn sets an agent's state/activity on any host's agent slice (a
// run's, a quest's, or a campaign's), seeding a minimal agent entry when the id is
// not yet present. A run's agents are pre-seeded by the executor, so seeding only
// ever fires for a campaign/quest's coordinating agent (intent-lead/reviewer,
// quest-lead), which the supervisor spawns without a pre-seeded entry.
func setAgentStateIn(agents *[]run.Agent, agentID, state, activity string) {
	a := ensureAgent(agents, agentID)
	a.State = state
	a.Activity = activity
}

// ensureAgent returns a pointer to the agent with agentID in the slice, appending
// a minimal entry when it is absent (so a campaign/quest coordinating agent the
// supervisor never pre-seeded is recorded rather than dropped). The pointer is
// valid only until the next append to the slice — callers mutate and return.
func ensureAgent(agents *[]run.Agent, agentID string) *run.Agent {
	for i := range *agents {
		if (*agents)[i].ID == agentID {
			return &(*agents)[i]
		}
	}
	*agents = append(*agents, run.Agent{ID: agentID, Events: []run.Event{}})
	return &(*agents)[len(*agents)-1]
}

func setTaskState(r *run.Run, taskID, state string) {
	for i := range r.Tasks {
		if r.Tasks[i].ID == taskID {
			r.Tasks[i].State = state
			return
		}
	}
}

func appendToAgent(r *run.Run, agentID string, e run.Event, addTokens int) {
	appendToAgentIn(&r.Agents, agentID, e, addTokens)
}

// appendToAgentIn appends an event (and adds tokens) to an agent on any host's
// agent slice, seeding the agent when absent (same self-seeding rationale as
// setAgentStateIn). It stamps the append time centrally so every event carries an
// ordering aid without threading a timestamp through each call site; TaskID is
// left best-effort (empty) — the per-agent slice order already gives sequence.
func appendToAgentIn(agents *[]run.Agent, agentID string, e run.Event, addTokens int) {
	if e.Ts == "" {
		e.Ts = time.Now().UTC().Format(time.RFC3339)
	}
	a := ensureAgent(agents, agentID)
	a.Events = append(a.Events, e)
	a.Tokens += addTokens
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// fullWhenTruncated returns the complete payload when the compact summary dropped
// content, and "" when the summary already holds it in full. Storing it only in the
// truncated case keeps the common (short) event from carrying a redundant copy — the
// full fields are omitempty, so a short payload lives solely in Input/Text.
func fullWhenTruncated(summary, full string) string {
	if summary == full {
		return ""
	}
	return full
}

// deriveTitle produces a short display label from a multi-paragraph objective/input:
// the first non-empty line with a leading markdown heading marker or an
// "Objective:"/"Goal:" prefix stripped, truncated to 72 chars — the same primitive
// PR titles use (prTitle/campaignPRTitle). Used to stamp Quest/Campaign.Title when
// the launcher supplied none, so the UI never renders the whole objective in a
// title slot.
func deriveTitle(text string) string {
	line := strings.TrimSpace(firstLine(text))
	line = strings.TrimSpace(strings.TrimLeft(line, "#")) // drop a leading markdown heading marker
	for _, p := range []string{"Objective:", "objective:", "Goal:", "goal:", "OBJECTIVE:", "GOAL:"} {
		if rest, ok := strings.CutPrefix(line, p); ok {
			line = strings.TrimSpace(rest)
			break
		}
	}
	return truncate(line, 72)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary so a multi-byte character isn't split into invalid
	// UTF-8 (which would corrupt the JSON the UI renders).
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
