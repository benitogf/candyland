package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
		OutputTokens int `json:"output_tokens"`
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
			case "restart":
				cancel()
				ctx, cancel = context.WithCancel(context.Background())
				stopped = false // a fresh re-run — the next <-done is a real completion
				// A restart is a fresh re-run — clear any prior error so the new run
				// can reach completion (the phase/green gates key off r.Error).
				c.Update(id, func(r *run.Run) { r.Status = "running"; r.Error = "" })
				done = run1(ctx)
			case "quit":
				// Terminate this executor entirely (used by Edit to re-plan a paused
				// run): kill the process tree and exit the goroutine. The conductor
				// has already reset the run, so we publish nothing here.
				cancel()
				c.cleanupBusConfigs(id) // a re-planned run regenerates these on its next spawn
				return
			}
		case <-done:
			if stopped {
				// Stopped — park on the control channel only. Setting done to nil
				// stops this select from spinning on the now-closed done channel
				// (a busy loop); a restart installs a fresh done below.
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
			fail(ctx, c, id, "tl", fmt.Sprintf("Couldn't find a working task split after %d attempts. Last problem: %s", replans, feedback))
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
	if !c.reviewUntilClean(ctx, id, delivered, r.Branch) {
		return // findings unresolved (or stopped) — never open a PR on un-reviewed work
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
		base, _ := currentBranch(ctx, repo)
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
	// ── Tech lead: partition the work (in its own worktree in the primary repo). ──
	c.Update(id, func(r *run.Run) {
		r.Status = "running"
		r.Phase = run.PhaseBuild
		tl := run.Agent{ID: "tl", Role: "Tech lead", Emoji: "🧭", Task: "partition · integrate · deliver",
			State: "working", Activity: "planning the partition", Budget: 800, Worktree: "wt/tl", Model: "opus-4-8",
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
	tasks := runAgentResilient(ctx, c, id, "tl", techLeadBootstrap, true, tlDir, extraDirsFor(primary, folders))
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
	c.Update(id, func(r *run.Run) {
		r.HasDag = true
		r.StatusLine = fmt.Sprintf("Coders are implementing %d %s…", len(tasks), plural(len(tasks), "task", "tasks"))
		r.Tasks = make([]run.Task, 0, len(tasks))
		for _, t := range tasks {
			r.Tasks = append(r.Tasks, run.Task{ID: t.ID, Title: t.Title, Files: t.Files, Test: t.Test, Owner: t.ID, State: "working", Deps: t.Deps})
			r.Agents = append(r.Agents, run.Agent{ID: t.ID, Role: orDefault(t.Role, "Coder"), Emoji: orDefault(t.Emoji, "⚙️"), Task: t.Title,
				State: "working", Activity: "implementing " + t.Title, Budget: 200, Worktree: "wt/" + t.ID, Model: "opus-4-8"})
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
	if err := addWorktree(ctx, repo, integDir, branch, base); err != nil {
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
			if err := resolveConflict(ctx, c, id, integDir, t, files, extra); err != nil {
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
			runAgentResilient(ctx, c, id, t.ID, coderBootstrap, false, wtDir, extra)
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
		"\n\n🍬 Opened by [candyland](https://github.com/benitogf/candyland)."
}

// The spawn prompts below are CONSTANT bootstraps. The request, task spec, and
// any prior-attempt feedback ride in the agent's brief (brief/<agentID>, fetched
// via the brief_get MCP tool) — never on argv, which Windows caps at ~32k. Each
// keeps the role discriminators ("tech lead", "git merge conflict", the TEST /
// PARTITION lines) the resilience layer and the stub tests rely on.

const techLeadBootstrap = "You are the tech lead. Call the brief_get tool FIRST to read the request you must partition — it carries the full plan (and any previous failed attempt to avoid), so it is no longer on your command line. " +
	"Then emit exactly one line beginning with `PARTITION ` followed by a JSON array of fork-safe tasks: " +
	`[{"id","title","role","emoji","files":[],"test","deps":[]}]. ` +
	"Tasks must own DISJOINT files so they can be implemented and merged in parallel. " +
	"A single atomic task is a valid partition — when the work doesn't decompose, emit exactly one task (never treat \"one task\" as a failure). " +
	"For small, tightly-coupled backend+frontend work, emit one task with role \"fullstack\"; split large cross-domain work into separate tasks sequenced with \"deps\". " +
	"If the work spans more than one of the run's folders/repos, set each task's \"repo\" to the target folder's name (omit it for the primary repo); each impacted repo gets its own pull request. " +
	"Then stop."

const coderBootstrap = "You are a coder. Call the brief_get tool FIRST to read your task — its title, the files you may touch, the defining test, and your role. " +
	"Implement the task until its defining test is green: make the changes with your tools — do not just describe them. " +
	"If your role is \"fullstack\", implement BOTH the server side and the client side of the slice and keep the API contract consistent between them. " +
	"When you run the defining test, report the result as one line beginning with `TEST ` " +
	`followed by JSON {"pass":<count>,"fail":<count>} (e.g. ` + "`TEST {\"pass\":3,\"fail\":0}`" +
	"), so the run records real verification counts."

// resolveConflict has the tech lead reconcile a merge git couldn't auto-merge.
// The conflict markers are left in the integration worktree; the tech lead edits
// the conflicted files to combine both sides, we verify no markers remain, then
// complete the merge. Retries with a firmer prompt; if it genuinely can't resolve
// the conflict in place, the caller aborts the merge and REASSESSES the split (a
// re-plan) — only an exhausted re-plan budget fails the run. A real tech lead
// resolves conflicts, or re-thinks the breakdown — it doesn't abandon the run.
func resolveConflict(ctx context.Context, c *Conductor, id, integDir string, t partitionTask, files, extra []string) error {
	attempts := maxAttempts()
	var lastErr error
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
		out := streamOnce(ctx, c, id, "tl", reinforce(conflictBootstrap, attempt, false), integDir, extra)
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

// reviewUntilClean runs a REAL review phase after integration and before any PR:
// a SEPARATE reviewer agent hard-reviews each repo's integrated diff (loading the
// detritus review doctrine via kb_get, not an inlined rubric), and any blockers
// drive a bounded fix→re-review loop in that same integration worktree. It returns
// true only when every repo reviews clean (so the executor opens a PR), false when
// the round budget is exhausted with blockers still open (recording an honest run
// error and opening no PR) or the run was stopped.
func (c *Conductor) reviewUntilClean(ctx context.Context, id string, delivered map[string]string, branch string) bool {
	rounds := maxReviewRounds()
	c.Update(id, func(r *run.Run) {
		r.Phase = run.PhaseReview
		r.StatusLine = "Reviewing the integrated changes before opening a pull request…"
		r.Agents = append(r.Agents, run.Agent{ID: reviewerID, Role: "Reviewer", Emoji: "🔎",
			Task: "review the integrated diff", State: "working", Activity: "loading review doctrine",
			Budget: 400, Worktree: "wt/review", Model: "opus-4-8",
			Events: []run.Event{{T: "system", Text: "reviewer · kb_get core/review-rigor + truthseeker"}}})
	})
	folders := orderedDelivered(delivered)
	for _, repo := range folders {
		integDir := delivered[repo]
		base, _ := currentBranch(ctx, repo)
		for round := 1; round <= rounds; round++ {
			if ctx.Err() != nil {
				return false // stopped mid-review
			}
			c.Update(id, func(r *run.Run) {
				r.StatusLine = fmt.Sprintf("Reviewing %s (round %d/%d)…", repoBase(repo), round, rounds)
				setAgentState(r, reviewerID, "working", fmt.Sprintf("reviewing %s (round %d/%d)", repoBase(repo), round, rounds))
			})
			c.putBrief(reviewerID, bus.Brief{Role: "reviewer", Title: "review " + repoBase(repo), Prompt: "git diff " + base + ".." + branch})
			out := streamOnce(ctx, c, id, reviewerID, reviewBootstrap, integDir, extraDirsForDelivered(repo, folders))
			if ctx.Err() != nil {
				return false // stopped mid-review
			}
			if out.startErr != nil {
				fail(ctx, c, id, reviewerID, startFailurePrefix+out.startErr.Error()+". The reviewer couldn't start; no PR is opened on an un-reviewed change.")
				return false
			}
			if out.review == nil {
				fail(ctx, c, id, reviewerID, "The reviewer produced no verdict for "+repoBase(repo)+" — refusing to open a PR on an un-reviewed change.")
				return false
			}
			verdict := *out.review
			if len(verdict.Blockers) == 0 {
				c.Update(id, func(r *run.Run) {
					setAgentState(r, reviewerID, "green", "review clean: "+repoBase(repo))
					appendToAgent(r, reviewerID, run.Event{T: "system", Text: "review clean: " + repoBase(repo)}, 0)
				})
				break // this repo is clean — review the next
			}
			if round == rounds {
				fail(ctx, c, id, reviewerID, fmt.Sprintf("Review of %s still has %d unresolved %s after %d rounds: %s. No PR is opened until review is clean.",
					repoBase(repo), len(verdict.Blockers), plural(len(verdict.Blockers), "blocker", "blockers"), rounds, firstFinding(verdict.Blockers)))
				return false
			}
			// Blockers remain and rounds are left: re-engage a fix agent in this repo's
			// integration worktree to address the cited findings, commit onto the run
			// branch, then re-review.
			if !c.fixReviewFindings(ctx, id, repo, integDir, branch, verdict.Blockers, extraDirsForDelivered(repo, folders), round) {
				return false // fix pass failed/stopped — error already recorded (or stopped)
			}
		}
	}
	c.Update(id, func(r *run.Run) {
		r.StatusLine = "Review clean — opening pull requests…"
		setAgentState(r, reviewerID, "done", "review clean")
	})
	return true
}

// fixReviewFindings re-engages a fix agent in the integration worktree to address
// the reviewer's cited blockers and commits the fixes onto the run branch. It
// returns true when the fixes were made and committed, false when the agent failed
// to act (error recorded) or the run was stopped.
func (c *Conductor) fixReviewFindings(ctx context.Context, id, repo, integDir, branch string, blockers []reviewFinding, extra []string, round int) bool {
	c.Update(id, func(r *run.Run) {
		r.StatusLine = fmt.Sprintf("Addressing %d review %s in %s…", len(blockers), plural(len(blockers), "finding", "findings"), repoBase(repo))
		setAgentState(r, reviewerID, "working", "fixing review findings in "+repoBase(repo))
		appendToAgent(r, reviewerID, run.Event{T: "system", Text: fmt.Sprintf("round %d: %d blocker(s) — fixing: %s", round, len(blockers), firstFinding(blockers))}, 0)
	})
	c.putBrief(reviewerID, bus.Brief{Role: "fix", Title: "address review findings in " + repoBase(repo), Findings: findingLines(blockers)})
	out := streamOnce(ctx, c, id, reviewerID, reviewFixBootstrap, integDir, extra)
	if ctx.Err() != nil {
		return false // stopped mid-fix
	}
	if out.startErr != nil {
		fail(ctx, c, id, reviewerID, startFailurePrefix+out.startErr.Error()+". The fix pass couldn't start.")
		return false
	}
	if !out.sawTool {
		fail(ctx, c, id, reviewerID, "The fix pass made no changes for the review findings in "+repoBase(repo)+" — refusing to open a PR with open blockers.")
		return false
	}
	if _, err := commitAll(ctx, integDir, "candyland(review): address findings in "+repoBase(repo)); err != nil {
		fail(ctx, c, id, reviewerID, "Couldn't commit the review fixes for "+repoBase(repo)+": "+err.Error())
		return false
	}
	return true
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

const reviewBootstrap = "You are a code reviewer. Call the brief_get tool FIRST — it names the repo and the exact diff command to review. " +
	"Load the detritus review doctrine via the kb_get tool: kb_get name=\"core/review-rigor\" AND kb_get name=\"flows/principles/truthseeker\"; apply that rubric, do NOT improvise your own. " +
	"Review the integrated diff with the doctrine's rigor (run the diff command in the brief, read the changed files, hunt for blockers). " +
	"Then emit EXACTLY ONE verdict line and stop: either `REVIEW_CLEAN` (no blockers) " +
	"OR `REVIEW_FINDINGS ` followed by JSON " + `{"blockers":[{"file":"path","line":12,"issue":"…"}]}` +
	" listing only genuine blockers (cite file and line per the doctrine). Do not ask questions."

const reviewFixBootstrap = "You are addressing review findings on an integrated branch before it opens as a pull request. " +
	"Call the brief_get tool FIRST to read the cited findings (file, line, issue). " +
	"Fix every cited blocker with your editing tools — make the changes, do not just describe them — and keep the existing tests green. " +
	"Do not ask questions and do not defer; resolve all the findings in this run."

const conflictBootstrap = "You are the tech lead resolving a git merge conflict while integrating parallel work into one branch. " +
	"Call the brief_get tool FIRST to read which task conflicted and the conflicted files. " +
	"Open each conflicted file and reconcile the two sides so BOTH changes are preserved and the result is correct — " +
	"remove every git conflict marker (<<<<<<<, =======, >>>>>>>). Use your editing tools to write the resolved files. " +
	"Do not ask questions and do not leave any conflict unresolved."

// mapAgentLine streams one stream-json line into the agent's live ooo state and
// returns the signals the resilience layer uses to judge compliance: any parsed
// partition, whether a tool was used (real work), and the latest text (checked
// for deferral / a question to the user).
func mapAgentLine(c *Conductor, id, agentID string, line streamLine) (partition []partitionTask, review *reviewVerdict, sawTool bool, text string) {
	switch line.Type {
	case "assistant":
		for _, blk := range line.Message.Content {
			b := blk
			if b.Type == "text" && b.Text != "" {
				if p := parsePartition(b.Text); p != nil {
					partition = p
				}
				if v, ok := parseReview(b.Text); ok {
					vv := v
					review = &vv
				}
				if pass, fail, ok := parseTest(b.Text); ok {
					c.Update(id, func(r *run.Run) {
						appendToAgent(r, agentID, run.Event{T: "test", Pass: pass, Fail: fail}, 0)
					})
				}
				text = b.Text
				c.Update(id, func(r *run.Run) { appendToAgent(r, agentID, run.Event{T: "text", Text: b.Text}, 0) })
			}
			if b.Type == "tool_use" {
				sawTool = true
				c.Update(id, func(r *run.Run) {
					appendToAgent(r, agentID, run.Event{T: "tool", Name: b.Name, Input: truncate(string(b.Input), 200)}, 0)
				})
			}
		}
	case "result":
		l := line
		if l.Result != "" {
			text = l.Result
		}
		c.Update(id, func(r *run.Run) {
			appendToAgent(r, agentID, run.Event{T: "result", Text: truncate(l.Result, 300)}, l.Usage.OutputTokens/1000)
		})
	}
	return partition, review, sawTool, text
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
	for i := range r.Agents {
		if r.Agents[i].ID == agentID {
			r.Agents[i].State = state
			r.Agents[i].Activity = activity
			return
		}
	}
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
	for i := range r.Agents {
		if r.Agents[i].ID == agentID {
			r.Agents[i].Events = append(r.Agents[i].Events, e)
			r.Agents[i].Tokens += addTokens
			return
		}
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
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
