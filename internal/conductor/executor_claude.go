package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
					r.Phase = len(run.Phases) - 1
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

// fanOut runs the whole delivery: resolve the repo, then partition → code →
// integrate (reassessing the split when the tech lead's own plan fails), then
// push and open a PR. It never claims success it didn't achieve, and never fails
// the run for a problem of the tech lead's own making (a bad split, an
// unresolvable conflict) without first letting it reassess and try again.
func fanOut(ctx context.Context, c *Conductor, id string) {
	r, ok := c.Get(id)
	if !ok {
		return
	}

	// Resolve the run's working folders (supplied at launch). The first folder is
	// the repo the run branches and opens its PR in; the rest are extra context
	// (--add-dir). These are ENVIRONMENTAL prerequisites — re-splitting the work
	// can't fix a missing repo, so a failure here is honest and terminal, not a
	// reason to re-plan.
	folders, err := c.folders(r)
	if err != nil {
		fail(ctx, c, id, "tl", "Couldn't resolve the run's folders: "+err.Error()+". Launch with at least one folder whose first entry is a git repository.")
		return
	}
	if len(folders) == 0 {
		fail(ctx, c, id, "tl", "The run has no folders. Launch it with at least one (the first is the git repository the run works in).")
		return
	}
	repo := expandHome(folders[0])
	extra := make([]string, 0, len(folders)-1)
	for _, f := range folders[1:] {
		extra = append(extra, expandHome(f))
	}
	if !isGitRepo(ctx, repo) {
		fail(ctx, c, id, "tl", "The run's first folder isn't a git repository: "+repo+". A run branches and opens its PR there.")
		return
	}
	base, err := currentBranch(ctx, repo)
	if err != nil {
		fail(ctx, c, id, "tl", "Couldn't read the repo's current branch: "+err.Error())
		return
	}

	// Everything happens in throwaway worktrees under wtRoot, so the user's own
	// checkout is never switched or dirtied. Clean any leftovers from a prior
	// attempt of THIS run (e.g. a restart), and again on the way out.
	wtRoot := filepath.Join(os.TempDir(), "candyland-wt", id)
	cleanup(c, id, repo, wtRoot)
	defer cleanup(c, id, repo, wtRoot)

	// ── Plan → code → integrate, REASSESSING the split on a plan-attributable
	//    failure. A coder that can't finish, or slices that conflict and can't be
	//    reconciled, mean the tech lead's partition was wrong — feed that back and
	//    let it re-partition rather than failing the run for its own mistake. ──
	replans := maxReplans()
	feedback := ""
	integDir := ""
	for attempt := 1; attempt <= replans; attempt++ {
		if ctx.Err() != nil {
			return
		}
		if attempt > 1 {
			// Fresh slate: drop the prior attempt's worktrees + per-agent branches so
			// the re-partition (which may use different task ids) starts clean.
			cleanup(c, id, repo, wtRoot)
			log.Printf("candyland: run %s re-planning (attempt %d/%d) after: %s", id, attempt, replans, feedback)
		}
		res := attemptDelivery(ctx, c, id, repo, r.Prompt, r.Branch, base, extra, wtRoot, feedback, attempt)
		if res.ok {
			integDir = res.integDir
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
	if integDir == "" {
		return // defensive — a successful loop always sets integDir
	}

	// ── Deliver: push the run branch and open one PR. These are ENVIRONMENTAL too
	//    (a missing 'origin' or an unauthenticated gh can't be fixed by re-splitting),
	//    so a failure here is honest and terminal. The phase advances to PR only once
	//    the PR actually opens — a push/auth failure must not show "PR · 100%". ──
	c.Update(id, func(r *run.Run) {
		r.StatusLine = "Pushing the branch and opening the pull request…"
		setAgentState(r, "tl", "working", "pushing the branch and opening the PR")
	})
	if err := pushBranch(ctx, integDir, r.Branch); err != nil {
		fail(ctx, c, id, "tl", "Couldn't push the run branch: "+err.Error()+". Check the repo has an 'origin' remote you can push to.")
		return
	}
	prURL, err := openPR(ctx, integDir, base, r.Branch, prTitle(r), prBody(r))
	if err != nil {
		fail(ctx, c, id, "tl", "Couldn't open the pull request: "+err.Error()+". Make sure GitHub CLI (gh) is installed and authenticated (gh auth login).")
		return
	}
	c.Update(id, func(r *run.Run) {
		r.PrURL = prURL
		r.Phase = len(run.Phases) - 1 // PR — reached only now that the PR is open
		r.StatusLine = "Opened the pull request."
		setAgentState(r, "tl", "done", "opened the PR")
	})
}

// attemptDeliveryResult is the outcome of ONE partition → code → integrate pass.
type attemptDeliveryResult struct {
	integDir string // integration worktree, ready to push (success only)
	ok       bool   // integration completed cleanly
	// replan, when non-empty, is feedback for the tech lead: a failure of its OWN
	// plan (a coder couldn't finish, slices conflicted unresolvably) that warrants
	// re-partitioning. ok==false && replan=="" means a terminal failure was already
	// recorded (claude missing, tech lead can't partition at all) or the run stopped.
	replan string
}

// attemptDelivery runs the tech lead → coders → integrate flow once. The tech lead
// reassesses across attempts: on a re-plan (attempt > 1) the prior failure is woven
// into its prompt so it produces a DIFFERENT breakdown.
func attemptDelivery(ctx context.Context, c *Conductor, id, repo, prompt, branch, base string, extra []string, wtRoot, feedback string, attempt int) attemptDeliveryResult {
	// ── Tech lead: partition the work (in its own worktree; not merged). ──
	c.Update(id, func(r *run.Run) {
		r.Status = "running"
		r.Phase = 1
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
	tlDir := filepath.Join(wtRoot, "tl")
	if err := addWorktree(ctx, repo, tlDir, branchName(id, "tl"), base); err != nil {
		fail(ctx, c, id, "tl", "Couldn't create the tech lead's worktree: "+err.Error())
		return attemptDeliveryResult{}
	}
	tasks := runAgentResilient(ctx, c, id, "tl", techLeadPrompt(prompt, feedback), true, tlDir, extra)
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

	// ── Coders: one per task, each in its own worktree off base, in parallel. ──
	runCoders(ctx, c, id, repo, base, wtRoot, tasks, extra)
	if ctx.Err() != nil {
		return attemptDeliveryResult{} // stopped
	}
	if cr, _ := c.Get(id); cr.Error != "" {
		reason := cr.Error
		if strings.HasPrefix(reason, startFailurePrefix) {
			// claude couldn't even start — environmental, not the plan's fault.
			// Re-partitioning can't help, so leave the error and end the run.
			return attemptDeliveryResult{}
		}
		// A coder couldn't finish — that's the tech lead's delegation failing.
		// Clear the error (so it isn't terminal) and re-plan with the reason.
		c.Update(id, func(r *run.Run) { r.Error = "" })
		return attemptDeliveryResult{replan: "A coder couldn't complete its task: " + reason +
			" Re-split into smaller, clearer, fully self-contained tasks (or sequence dependent ones with deps)."}
	}

	// ── Integrate: merge every task branch into the run branch, in its own
	//    worktree (the user's checkout stays untouched). ──
	c.Update(id, func(r *run.Run) {
		r.Phase = len(run.Phases) - 2 // Review
		r.StatusLine = "Integrating the work into one branch…"
		setAgentState(r, "tl", "integrating", "merging the slices")
	})
	integDir := filepath.Join(wtRoot, "integrate")
	if err := addWorktree(ctx, repo, integDir, branch, base); err != nil {
		fail(ctx, c, id, "tl", "Couldn't create the integration worktree: "+err.Error())
		return attemptDeliveryResult{}
	}
	for _, t := range tasks {
		conflicted, files, err := mergeBranch(ctx, integDir, branchName(id, t.ID))
		if err != nil {
			// git couldn't even start the merge cleanly — treat as the split's fault.
			abortMerge(integDir)
			return attemptDeliveryResult{replan: "Merging " + t.ID + " failed: " + err.Error() +
				" Re-partition so tasks own disjoint files."}
		}
		if conflicted {
			// First, the tech lead reconciles overlapping work IN PLACE (cheap). Only
			// if that genuinely can't be done do we reassess the whole split.
			if err := resolveConflict(ctx, c, id, integDir, t, files, extra); err != nil {
				if ctx.Err() != nil {
					return attemptDeliveryResult{} // stopped mid-resolution
				}
				abortMerge(integDir)
				return attemptDeliveryResult{replan: "Task " + t.ID + " conflicted with an earlier slice in " +
					strings.Join(files, ", ") + " and couldn't be reconciled (" + err.Error() +
					"). Re-partition so NO two tasks edit the same file, or sequence the dependent one with deps."}
			}
			c.Update(id, func(r *run.Run) {
				r.StatusLine = "Resolved a merge conflict — integrating…"
				appendToAgent(r, "tl", run.Event{T: "system", Text: "resolved conflict in " + strings.Join(files, ", ")}, 0)
				setAgentState(r, "tl", "integrating", "merging the slices")
			})
		}
		if ctx.Err() != nil {
			return attemptDeliveryResult{}
		}
	}
	return attemptDeliveryResult{integDir: integDir, ok: true}
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
			runAgentResilient(ctx, c, id, t.ID, coderPrompt(t), false, wtDir, extra)
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

// cleanup removes this run's worktrees and the throwaway per-agent branches
// (best-effort). The run branch itself is left intact — it's the PR.
func cleanup(c *Conductor, id, repo, wtRoot string) {
	ctx := context.Background()
	entries, _ := os.ReadDir(wtRoot)
	for _, e := range entries {
		removeWorktree(ctx, repo, filepath.Join(wtRoot, e.Name()))
		_, _ = git(ctx, repo, "branch", "-D", branchName(id, e.Name()))
	}
	_, _ = git(ctx, repo, "worktree", "prune")
	_ = os.RemoveAll(wtRoot)
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

func techLeadPrompt(prompt, feedback string) string {
	p := "You are the tech lead. First, emit exactly one line beginning with `PARTITION ` " +
		"followed by a JSON array of fork-safe tasks: " +
		`[{"id","title","role","emoji","files":[],"test","deps":[]}]. ` +
		"Tasks must own DISJOINT files so they can be implemented and merged in parallel. " +
		"Then stop. Request:\n\n" + prompt
	if feedback != "" {
		p += "\n\n--- PREVIOUS ATTEMPT FAILED ---\n" + feedback +
			"\nProduce a DIFFERENT partition that avoids this: ensure no two tasks edit the same file " +
			"(split by file/module ownership), keep each task small and self-contained, and use \"deps\" " +
			"to sequence work that genuinely can't run in parallel."
	}
	return p
}

func coderPrompt(t partitionTask) string {
	return "Implement this fork-safe task until its defining test is green: " + t.Title +
		". Files: " + strings.Join(t.Files, ", ") + ". Test: " + t.Test +
		". Make the changes with tools — do not just describe them." +
		" When you run the defining test, report the result as one line beginning with `TEST ` " +
		`followed by JSON {"pass":<count>,"fail":<count>} (e.g. ` + "`TEST {\"pass\":3,\"fail\":0}`" +
		"), so the run records real verification counts."
}

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
		out := streamOnce(ctx, c, id, "tl", reinforce(conflictPrompt(t, files), attempt, false), integDir, extra)
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

func conflictPrompt(t partitionTask, files []string) string {
	return "You are resolving a git merge conflict as the tech lead integrating parallel work into one branch. " +
		"Merging task \"" + t.Title + "\" produced conflicts in: " + strings.Join(files, ", ") +
		". Open each conflicted file and reconcile the two sides so BOTH changes are preserved and the result is correct — " +
		"remove every git conflict marker (<<<<<<<, =======, >>>>>>>). Use your editing tools to write the resolved files. " +
		"Do not ask questions and do not leave any conflict unresolved."
}

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
