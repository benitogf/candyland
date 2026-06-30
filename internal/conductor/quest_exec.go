package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/candyland/internal/run"
)

// This file owns the quest EXECUTION layer (the iterative loop). A quest is the
// Candyland-native homologue of /janitor: each TICK spawns a discovery/triage
// agent (the "quest lead") that surfaces work items, and for the accepted items
// the loop LAUNCHES child runs through the EXISTING run executor (Create + the
// ClaudeExecutor fanOut/attemptDelivery flow) — it does not fork a parallel run
// engine. The loop logic stays in Go (bounded ticks, autonomy gating, budget
// caps); the per-tick INTELLIGENCE (what to do, whether it's safe/in-scope)
// lives in the quest-lead agent, which loads the detritus loop/audit/completion
// doctrine via kb_get rather than an inlined rubric (the Composition Constraint).

// questLeadID is the single discovery/triage agent identity per tick. It keys the
// agent's brief on the bus (brief_get) the same way the tech-lead/coder ids do.
const questLeadID = "quest-lead"

// maxQuestTicks bounds the total ticks one BeginQuest drive performs, so a quest
// whose discovery keeps surfacing the same item can't loop forever in one drive.
// Tunable via CANDYLAND_QUEST_MAX_TICKS. A pause/resume starts a fresh drive.
func maxQuestTicks() int { return envInt("CANDYLAND_QUEST_MAX_TICKS", 20) }

// maxItemAttempts bounds how many times the loop will launch a child run for the
// SAME work-item title before giving up on it (so a quest can't thrash one blocked
// item forever — the per-item analogue of maxReplans). Tunable via
// CANDYLAND_QUEST_ITEM_ATTEMPTS.
func maxItemAttempts() int { return envInt("CANDYLAND_QUEST_ITEM_ATTEMPTS", 2) }

// questDriver tracks a quest's running tick-loop goroutine so pause/stop can halt
// it. It mirrors how a run's runtime holds the executor's control channel: a quest
// id maps to a cancel func; cancelling it ends the current drive cooperatively
// (the loop checks ctx between ticks and each child-run wait).
type questDriver struct {
	cancel context.CancelFunc
}

// BeginQuest starts (or continues) a quest's tick loop in a goroutine, mirroring
// how Begin launches a run executor. It is idempotent: a quest already being
// driven is left alone, and a terminal (stopped/done) quest is refused. A paused
// quest is resumed (its status flips back to running) before the drive starts.
func (c *Conductor) BeginQuest(id string) bool {
	q, ok := c.GetQuest(id)
	if !ok {
		return false
	}
	if q.Status == "stopped" || q.Status == "done" || q.Status == "surfaced-only" || q.Status == "reviewed" {
		return false // terminal — start a new quest instead
	}

	c.mu.Lock()
	if c.questDrivers == nil {
		c.questDrivers = map[string]*questDriver{}
	}
	if _, running := c.questDrivers[id]; running {
		c.mu.Unlock()
		return true // already driving — idempotent (a double POST can't spawn two loops)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.questDrivers[id] = &questDriver{cancel: cancel}
	c.mu.Unlock()

	// Resume a paused quest: the drive only runs while status is running.
	c.UpdateQuest(id, func(q *run.Quest) {
		if q.Status == "paused" || q.Status == "" {
			q.Status = "running"
			q.PauseReason = ""
		}
	})
	log.Printf("candyland: quest %s drive started", id)
	go c.driveQuest(ctx, id)
	return true
}

// PauseQuest halts future ticks without deleting the quest: it cancels the running
// drive and records Status=paused + the reason. ResumeQuest restarts the drive.
func (c *Conductor) PauseQuest(id, reason string) bool {
	// Halt any live drive. A quest not currently driving is still allowed to pause
	// (so a quest paused between drives stays paused); the UpdateQuest below decides
	// the outcome (unknown quest → false), so the halt result is intentionally unused.
	c.haltQuestDrive(id)
	return c.UpdateQuest(id, func(q *run.Quest) {
		if q.Status == "stopped" || q.Status == "done" {
			return // terminal stays terminal
		}
		q.Status = "paused"
		if reason != "" {
			q.PauseReason = reason
		}
	})
}

// ResumeQuest restarts a paused quest's drive. A quest that isn't paused is left
// as-is (returns false), and a terminal quest can't resume.
func (c *Conductor) ResumeQuest(id string) bool {
	q, ok := c.GetQuest(id)
	if !ok || q.Status != "paused" {
		return false
	}
	return c.BeginQuest(id)
}

// StopQuest is terminal: it cancels the drive and marks the quest stopped with the
// reason. A stopped quest never ticks again (BeginQuest/ResumeQuest refuse it).
func (c *Conductor) StopQuest(id, reason string) bool {
	c.haltQuestDrive(id)
	return c.UpdateQuest(id, func(q *run.Quest) {
		q.Status = "stopped"
		if reason != "" {
			q.PauseReason = reason
		}
	})
}

// haltQuestDrive cancels and forgets a quest's running drive goroutine (if any).
// Returns true when a live drive was halted.
func (c *Conductor) haltQuestDrive(id string) bool {
	c.mu.Lock()
	d := c.questDrivers[id]
	delete(c.questDrivers, id)
	c.mu.Unlock()
	if d == nil {
		return false
	}
	d.cancel()
	return true
}

// QuestChildRuns returns every run whose QuestID == id (the quest's child runs),
// read from storage so it covers finished/untracked runs too.
func (c *Conductor) QuestChildRuns(id string) []run.Run {
	if c.server == nil {
		return nil
	}
	keys, err := c.server.Storage.Keys()
	if err != nil {
		return nil
	}
	var out []run.Run
	for _, k := range keys {
		if !strings.HasPrefix(k, "runs/") {
			continue
		}
		obj, err := c.server.Storage.Get(k)
		if err != nil {
			continue
		}
		var r run.Run
		if json.Unmarshal(obj.Data, &r) == nil && r.QuestID == id {
			out = append(out, cloneRun(r))
		}
	}
	return out
}

// driveQuest is the bounded tick loop. Each tick discovers + triages work via the
// quest lead, launches child runs for accepted items per the autonomy level, and
// records the tick. It stops when stopped/paused/blocked, when no safe work
// remains, when the token budget is exceeded, or when the tick bound is reached.
func (c *Conductor) driveQuest(ctx context.Context, id string) {
	defer c.haltQuestDrive(id)    // forget the driver on exit so a later BeginQuest can re-drive
	defer c.cleanupBusConfigs(id) // drop the quest lead's per-spawn --mcp-config files

	ticks := maxQuestTicks()
	itemAttempts := map[string]int{} // work-item title → times a child run was launched (thrash cap)
	for tick := 1; tick <= ticks; tick++ {
		if ctx.Err() != nil {
			return // paused/stopped — cooperative halt between ticks
		}
		q, ok := c.GetQuest(id)
		if !ok || q.Status != "running" {
			return // paused/stopped/terminal — only a running quest ticks
		}
		// Token-budget gate: when usage exceeds the budget, pause with a visible
		// reason rather than keep spending (CANDYLAND_QUEST_TOKEN_CAP honoring).
		if q.TokenBudget > 0 && q.TokensUsed >= q.TokenBudget {
			c.pauseQuestForBudget(id, q.TokensUsed, q.TokenBudget)
			return
		}

		cont := c.runQuestTick(ctx, id, tick, itemAttempts)
		if !cont {
			return // the tick decided the loop should stop (no work / blocked / stopped / budget)
		}
	}
	// Tick bound reached without a natural stop — pause so the quest can be resumed
	// (a fresh drive) rather than silently ending.
	c.UpdateQuest(id, func(q *run.Quest) {
		if q.Status == "running" {
			q.Status = "paused"
			q.PauseReason = fmt.Sprintf("tick bound (%d) reached this drive; resume to continue", ticks)
		}
	})
}

// runQuestTick performs one iteration and returns whether the loop should continue.
// It spawns the quest lead, parses its work items, launches child runs for the
// accepted ones (autonomy-gated), and records the Tick + updates rollups.
func (c *Conductor) runQuestTick(ctx context.Context, id string, tick int, itemAttempts map[string]int) bool {
	q, ok := c.GetQuest(id)
	if !ok {
		return false
	}
	tickID := fmt.Sprintf("t%d", tick)
	now := time.Now().UTC().Format(time.RFC3339)
	rec := run.Tick{ID: tickID, StartedAt: now}

	// ── Discovery + triage: spawn the quest lead in the quest's primary folder. ──
	items, summary, tokens, perr := c.questDiscover(ctx, id, q, tickID)
	if ctx.Err() != nil {
		return false // paused/stopped mid-discovery — not a failure
	}
	rec.DiscoverySummary = summary
	if perr != "" {
		rec.Blockers = append(rec.Blockers, perr)
		rec.NextAction = "blocked — discovery failed"
		c.recordTick(id, rec, tokens, nil)
		c.UpdateQuest(id, func(q *run.Quest) {
			if q.Status == "stopped" || q.Status == "done" {
				return // a concurrent Stop/completion is authoritative — don't resurrect
			}
			q.Status = "blocked"
			q.PauseReason = perr
		})
		return false
	}

	// No safe work surfaced this tick → stop the loop (done — nothing left to do).
	accepted := acceptedItems(items)
	if len(items) == 0 || len(accepted) == 0 {
		rec.NextAction = "no safe work remaining — stopping"
		c.recordTick(id, rec, tokens, nil)
		c.finishQuest(id)
		return false
	}

	// ── Launch: report-only (L1) records the items but launches nothing; L2/L3
	//    launch a child run per accepted item via the existing run executor. Each
	//    item gets a durable WorkItem with the real disposition (no positional
	//    guessing — the disposition is the launch outcome). ──
	var ledger []run.WorkItem
	for i, it := range accepted {
		w := run.WorkItem{
			ID:             fmt.Sprintf("%s-w%d", tickID, i),
			SourceTick:     tickID,
			Evidence:       it.Evidence,
			Classification: it.Classification,
			Decision:       orDefault(it.Decision, "do"),
		}
		if q.AutonomyLevel == run.AutonomyReportOnly {
			// L1: surface only — no child-run edits/PRs. Skipped disposition (reported,
			// not acted on), which is the report-only contract.
			rec.TriageDecisions = append(rec.TriageDecisions, it.Title+": report-only (L1) — surfaced, not launched")
			w.Disposition = "skipped"
			ledger = append(ledger, w)
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		rec.TriageDecisions = append(rec.TriageDecisions, fmt.Sprintf("%s: do now (%s)", it.Title, q.AutonomyLevel))
		if itemAttempts[it.Title] >= maxItemAttempts() {
			rec.Blockers = append(rec.Blockers, fmt.Sprintf("giving up on %q after %d attempts", it.Title, maxItemAttempts()))
			w.Disposition = "blocked"
			ledger = append(ledger, w)
			continue
		}
		itemAttempts[it.Title]++
		childID, childPRs, childErr := c.launchChildRun(ctx, q, it, tickID)
		if ctx.Err() != nil {
			return false
		}
		w.ChildRunID = childID
		if childID != "" {
			rec.LaunchedRunIDs = append(rec.LaunchedRunIDs, childID)
		}
		rec.PRs = append(rec.PRs, childPRs...)
		if childErr != "" {
			rec.Blockers = append(rec.Blockers, it.Title+": "+childErr)
			w.Disposition = "blocked"
		} else {
			w.Disposition = "completed"
		}
		ledger = append(ledger, w)
	}
	if q.AutonomyLevel == run.AutonomyReportOnly {
		rec.NextAction = "report-only — surfaced findings, launched nothing"
	} else {
		rec.NextAction = "launched child runs — continue next tick"
	}

	c.recordTick(id, rec, tokens, ledger)
	// Report-only quests have nothing to build — one discovery pass per drive is the
	// whole job, so stop after surfacing rather than re-discovering the same findings.
	if q.AutonomyLevel == run.AutonomyReportOnly {
		c.finishQuest(id)
		return false
	}
	return true
}

// questDiscover spawns the quest lead for one tick and returns the parsed work
// items, a discovery summary, the tokens it consumed, and a non-empty error string
// when the discovery agent failed (couldn't start / produced no verdict). The
// quest lead runs in the quest's primary folder with the others as --add-dir
// context, and its PROMPT instructs it to load the detritus loop/audit/completion
// doctrine via kb_get and emit a WORKITEMS / WORKITEMS_NONE verdict.
func (c *Conductor) questDiscover(ctx context.Context, id string, q run.Quest, tickID string) (items []questWorkItem, summary string, tokens int, errMsg string) {
	folders := append([]string(nil), q.Folders...)
	if len(folders) == 0 {
		return nil, "", 0, "the quest has no folders (launch it with at least the git repo to work in)"
	}
	for i := range folders {
		folders[i] = expandHome(folders[i])
	}
	primary := folders[0]
	extra := extraDirsFor(primary, folders)

	c.putBrief(questLeadID, bus.Brief{
		To:     questLeadID,
		Role:   "quest-lead",
		Prompt: questBriefPrompt(q, tickID),
	})
	// A quest lead has no in-memory run.Run; it runs against the quest id, and
	// mapAgentLine routes its agent state+events onto the quest's own Agents slice
	// (via updateAgentHost), so the parent shows what it is itself doing. The
	// quest's Tick record remains the durable work trace. streamOnce still parses
	// the agent's stdout for us via mapAgentLine's returned text on each line.
	out := c.streamQuestLead(ctx, id, primary, extra)
	if ctx.Err() != nil {
		return nil, "discovery interrupted", out.tokens, ""
	}
	if out.startErr != nil {
		return nil, "discovery failed to start", out.tokens, startFailurePrefix + out.startErr.Error()
	}
	if out.stalled {
		return nil, "discovery stalled", out.tokens, "the quest lead stalled before producing a verdict"
	}
	parsed, none, ok := parseWorkItems(out.text)
	if !ok {
		return nil, "no verdict", out.tokens, "the quest lead produced no WORKITEMS verdict"
	}
	if none {
		return nil, "no work items surfaced", out.tokens, ""
	}
	return parsed, fmt.Sprintf("surfaced %d work %s", len(parsed), plural(len(parsed), "item", "items")), out.tokens, ""
}

// questLeadOutcome is the slice of streamOnce a discovery pass needs.
type questLeadOutcome struct {
	text     string
	tokens   int
	startErr error
	stalled  bool
}

// streamQuestLead runs the quest lead as a single claude process and returns its
// aggregated text + token usage. It reuses streamOnce's process machinery (stall
// watchdog, ctx kill, bus mcp-config wiring) exactly as runs do — no parallel
// engine. The quest id is passed so the bus mcp-config / brief are keyed per quest.
func (c *Conductor) streamQuestLead(ctx context.Context, questID, workdir string, extra []string) questLeadOutcome {
	// streamOnce records agent state+events through mapAgentLine; for a quest id it
	// routes onto the quest's Agents slice (updateAgentHost), so the quest-lead is
	// visible on the parent. We run a dedicated single-shot streamOnce against the
	// quest id namespace and collect the agent's aggregated text.
	res := streamOnce(ctx, c, questID, questLeadID, questLeadBootstrap, workdir, extra)
	// allText joins every assistant/result block, so the WORKITEMS verdict is found
	// wherever the quest lead emitted it (not only on the final block).
	return questLeadOutcome{text: res.allText, tokens: res.tokens, startErr: res.startErr, stalled: res.stalled}
}

// launchChildRun creates and drives ONE child run for an accepted work item using
// the EXISTING run flow (Create → ClaudeExecutor), with QuestID set and delivery
// per the quest's Deliver: standalone (pr) opens its own PR; campaign-owned
// (branch) commits onto QuestBranch and opens no PR. It blocks until the child run
// reaches a terminal state (or the quest is paused/stopped), then returns the
// child id, any PRs it opened, and an error string when the child failed.
func (c *Conductor) launchChildRun(ctx context.Context, q run.Quest, it questWorkItem, tickID string) (childID string, prs []run.PR, errMsg string) {
	prompt := childRunPrompt(q, it)
	childID = c.linkQuestChild(q, run.Spec{
		Folders: q.Folders,
		Prompt:  prompt,
		Title:   it.Title,
	})
	branch := QuestBranch(q)

	c.Begin(childID)

	// Wait for the child run to reach a terminal state, honoring quest ctx so a
	// pause/stop halts the wait (and stops the child run too).
	for {
		select {
		case <-ctx.Done():
			c.Command(childID, "stop")
			return childID, prs, "interrupted"
		case <-time.After(50 * time.Millisecond):
		}
		r, ok := c.Get(childID)
		if !ok {
			return childID, prs, "child run lost"
		}
		if r.Status == "done" || r.Status == "cancelled" {
			prs = childRunPRs(r, branch)
			if r.Error != "" {
				return childID, prs, r.Error
			}
			return childID, prs, ""
		}
	}
}

// linkQuestChild creates a child run and stamps its parent link AND delivery mode
// at launch (O3 both-way linkage + O5 deliver serialized), so the child carries
// QuestID/CampaignID and a CONCRETE deliver value the moment it exists — never an
// empty deliver the frontend can't key on. A campaign-owned quest (QuestBranch
// non-empty) delivers onto the shared branch (Deliver=branch, no PR); a standalone
// quest child opens its own PR (Deliver=pr, the types.go:163 default made explicit).
// The parent-side link is the WorkItem.ChildRunID ledger recorded by the tick.
func (c *Conductor) linkQuestChild(q run.Quest, spec run.Spec) string {
	childID := c.Create(spec)
	branch := QuestBranch(q)
	c.Update(childID, func(r *run.Run) {
		r.QuestID = q.ID
		r.CampaignID = q.CampaignID
		switch {
		case q.Deliver == run.DeliverFeedback || q.Deliver == run.DeliverReview:
			// Update an EXISTING PR in place (feedback) / produce findings, no new PR
			// (review). The target PR rides on the child so fanOut bases its work on
			// that PR's head and pushes back onto it (or opens nothing, for review).
			r.Deliver = q.Deliver
			r.TargetPR = q.TargetPR
		case branch != "":
			r.Branch = branch
			r.Deliver = run.DeliverBranch // commit onto the shared branch, open no PR
		default:
			r.Deliver = run.DeliverPR // standalone: open its own PR (serialized, not empty)
		}
	})
	return childID
}

// childRunPRs returns the PRs a finished child run produced. A branch-delivered
// (campaign-owned) child opens no PR — its work is a commit on the shared branch,
// reported as a PR-less record so the tick still shows what landed.
func childRunPRs(r run.Run, branch string) []run.PR {
	if branch != "" {
		return nil // campaign-owned: commit onto the shared branch, no PR
	}
	return append([]run.PR(nil), r.PRs...)
}

// recordTick appends a completed tick, advances the work-item ledger, recomputes
// the rollups, and stamps token usage onto the quest in a single durable update.
func (c *Conductor) recordTick(id string, rec run.Tick, addTokens int, items []run.WorkItem) {
	rec.EndedAt = time.Now().UTC().Format(time.RFC3339)
	c.UpdateQuest(id, func(q *run.Quest) {
		q.Ticks = append(q.Ticks, rec)
		q.WorkItems = append(q.WorkItems, items...)
		q.TokensUsed += addTokens
		if len(rec.LaunchedRunIDs) > 0 || len(items) > 0 {
			q.LastProgress = rec.EndedAt
		}
		recomputeQuestRollups(q)
	})
}

// finishQuest moves a quest to its terminal state, choosing between plain "done"
// (it shipped, or its delivery is the branch commit by design) and the distinct
// "surfaced-only" no-op state (Q2) — and annotating an intent↔autonomy mismatch
// (Q4) when an execute-intent objective produced a report-only no-op. A concurrent
// Stop is authoritative and left alone.
func (c *Conductor) finishQuest(id string) {
	c.UpdateQuest(id, func(q *run.Quest) {
		if q.Status == "stopped" {
			return // a concurrent Stop is authoritative
		}
		q.Status = questTerminalStatus(q)
		q.Summary = questTerminalSummary(q)
		q.LastProgress = time.Now().UTC().Format(time.RFC3339)
	})
}

// questIsNoOp reports whether a terminal quest delivered NOTHING in-scope: zero
// executed items and zero PRs, with items having been surfaced/skipped. The
// CARVE-OUT: a branch-delivered quest (Deliver=branch) legitimately opens 0 PRs —
// its delivery IS the branch commit — so a branch quest that completed items is NOT
// a no-op. The rule keys on actual zero-delivery, never on prsOpened==0 alone.
func questIsNoOp(q *run.Quest) bool {
	if q.Deliver == run.DeliverBranch && q.ItemsCompleted > 0 {
		return false // branch delivery by design — the commit is the delivery
	}
	// Feedback/review delivery never open NEW PRs (feedback updates an existing one;
	// review may apply none) — same family as the branch carve-out. A feedback run
	// that updated a PR and a review run with no findings are legitimately done /
	// reviewed, NOT zero-delivery no-ops.
	if q.Deliver == run.DeliverFeedback || q.Deliver == run.DeliverReview {
		return false
	}
	delivered := q.ItemsCompleted > 0 || q.PRsOpened > 0
	surfaced := q.ItemsSkipped > 0 || len(q.WorkItems) > 0
	return !delivered && surfaced
}

// questTerminalStatus is the terminal status a finished quest should carry:
// "surfaced-only" for a zero-delivery no-op (Q2), else plain "done".
func questTerminalStatus(q *run.Quest) string {
	if q.Deliver == run.DeliverReview {
		return "reviewed" // a review quest opens no PR — its terminal state is "reviewed", not "done"
	}
	if questIsNoOp(q) {
		return "surfaced-only"
	}
	return "done"
}

// questTerminalSummary names a terminal quest's outcome so a no-op is reported as
// such instead of an undifferentiated "done". For a no-op it accounts the
// surfaced/executed/PR counts, and — when the objective IMPLIED execution but the
// quest ran report-only (L1) — WARNS about the intent↔autonomy mismatch (Q4).
func questTerminalSummary(q *run.Quest) string {
	if q.Deliver == run.DeliverReview {
		if q.ItemsCompleted > 0 {
			return fmt.Sprintf("reviewed (findings applied to PR #%d)", q.TargetPR)
		}
		return "reviewed (no actionable findings)"
	}
	if !questIsNoOp(q) {
		return ""
	}
	surfaced := q.ItemsSkipped + q.ItemsBlocked + q.ItemsCompleted
	summary := fmt.Sprintf("surfaced-only: %d surfaced, 0 executed, 0 PRs", surfaced)
	if q.AutonomyLevel == run.AutonomyReportOnly && objectiveImpliesExecution(q.Objective) {
		summary += " — WARNING: intent↔autonomy mismatch (objective implies execution but autonomy is report-only L1; raise autonomy to L2/L3 to execute)"
	}
	return summary
}

// objectiveImpliesExecution reports whether an objective asks for work to be DONE
// (implement/add/fix/refactor…) rather than merely surfaced (review/audit/report).
// It is the Q4 misconfig signal, kept separate from the terminal-state computation.
func objectiveImpliesExecution(objective string) bool {
	o := strings.ToLower(objective)
	for _, verb := range []string{"implement", "add ", "fix", "build", "create", "refactor", "write", "migrate", "rename", "delete", "remove", "update", "wire", "integrate"} {
		if strings.Contains(o, verb) {
			return true
		}
	}
	return false
}

// recomputeQuestRollups derives the dashboard counters from the work-item ledger,
// the single source of truth (mirroring recompute for runs).
func recomputeQuestRollups(q *run.Quest) {
	prs, completed, skipped, blocked := 0, 0, 0, 0
	for _, w := range q.WorkItems {
		switch w.Disposition {
		case "completed":
			completed++
		case "skipped":
			skipped++
		case "blocked":
			blocked++
		}
	}
	for _, t := range q.Ticks {
		for _, pr := range t.PRs {
			if pr.URL != "" {
				prs++
			}
		}
	}
	q.PRsOpened = prs
	q.ItemsCompleted = completed
	q.ItemsSkipped = skipped
	q.ItemsBlocked = blocked
}

// pauseQuestForBudget pauses a quest whose token usage exceeded its budget, with a
// visible reason (the CANDYLAND_QUEST_TOKEN_CAP honoring required by the contract).
func (c *Conductor) pauseQuestForBudget(id string, used, budget int) {
	reason := fmt.Sprintf("token budget exceeded: used %d of %d — paused", used, budget)
	log.Printf("candyland: quest %s %s", id, reason)
	c.UpdateQuest(id, func(q *run.Quest) {
		if q.Status == "stopped" || q.Status == "done" {
			return // a concurrent Stop/completion is authoritative
		}
		q.Status = "paused"
		q.PauseReason = reason
	})
}

// --- work-item parsing (the quest-lead verdict convention) ---

// questWorkItem is one item the quest lead surfaces on a WORKITEMS line. It mirrors
// run.partitionTask's role as a parsed-from-stdout convention: a fenced line the
// loop parses, not a stored type. classification/decision come from triage.
type questWorkItem struct {
	Title          string `json:"title"`
	Evidence       string `json:"evidence"`
	Classification string `json:"classification"`
	Decision       string `json:"decision"` // do | skip | block
}

// parseWorkItems extracts the quest lead's verdict from its output. A WORKITEMS_NONE
// line means no work this tick (none=true). A `WORKITEMS <json>` line carries the
// items. ok is false when NEITHER line is present (no verdict — a failure, never a
// silent pass), mirroring parseReview. The last verdict line wins.
func parseWorkItems(text string) (items []questWorkItem, none, ok bool) {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case ln == "WORKITEMS_NONE":
			items, none, ok = nil, true, true
		case strings.HasPrefix(ln, "WORKITEMS "):
			var parsed []questWorkItem
			if json.Unmarshal([]byte(strings.TrimPrefix(ln, "WORKITEMS ")), &parsed) == nil {
				items, none, ok = parsed, false, true
			}
		}
	}
	return items, none, ok
}

// acceptedItems is the subset triage decided to act on (decision "do", or empty —
// an item with no explicit decision defaults to doable, matching how a coder treats
// a task as work to do unless told otherwise). "skip"/"block" are excluded.
func acceptedItems(items []questWorkItem) []questWorkItem {
	out := make([]questWorkItem, 0, len(items))
	for _, it := range items {
		d := strings.ToLower(strings.TrimSpace(it.Decision))
		if d == "skip" || d == "block" {
			continue
		}
		out = append(out, it)
	}
	return out
}

// --- prompts (composition, not inlined rubrics) ---

// questLeadBootstrap is the CONSTANT discovery/triage prompt. Like the tech-lead /
// coder bootstraps it carries no quest context on argv (that rides the brief via
// brief_get); it tells the quest lead to load the detritus loop/audit/completion
// doctrine via kb_get and APPLY it, then emit a structured WORKITEMS verdict. It
// must NOT inline a rubric — the doctrine is the rubric (the Composition Constraint).
const questLeadBootstrap = "You are the quest lead driving one tick of an iterative work loop. " +
	"Call the brief_get tool FIRST to read the quest's objective, scope, safety boundary, and verification — it is no longer on your command line. " +
	"Load and APPLY the detritus doctrine via the kb_get tool: kb_get name=\"core/loop\" (loop fundamentals: cadence, skip-streak, durability), " +
	"kb_get name=\"core/todo-audit\" (how to discover, prioritize, and fork-gate work items), and kb_get name=\"core/completion\" (the three dispositions and the definition of done). " +
	"Do NOT improvise your own rubric — use the doctrine you loaded. " +
	"Discover the next safe, in-scope work item(s): explore the folder, find concrete work that fits the objective and respects the scope and safety boundary, and TRIAGE each (is it safe? in scope? a single self-contained change?). " +
	"Then emit EXACTLY ONE verdict line and stop: either `WORKITEMS_NONE` (no safe in-scope work remains this tick) " +
	"OR `WORKITEMS ` followed by a JSON array " + `[{"title":"…","evidence":"why it's needed","classification":"category","decision":"do|skip|block"}]` +
	" listing only items you triaged as safe and in scope (decision \"do\"); use \"skip\"/\"block\" for items you surfaced but will not act on. Do not ask questions and do not defer."

// questBriefPrompt is the per-quest context the quest lead reads via brief_get. It
// carries the working objective, scope, safety boundary, verification, stop
// criteria, and the tick number — never on argv.
func questBriefPrompt(q run.Quest, tickID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "QUEST OBJECTIVE: %s\n", q.Objective)
	if q.Scope != "" {
		fmt.Fprintf(&b, "SCOPE (in-bounds work only): %s\n", q.Scope)
	}
	if q.Safety != "" {
		fmt.Fprintf(&b, "SAFETY BOUNDARY (never touch): %s\n", q.Safety)
	}
	if len(q.Verify) > 0 {
		fmt.Fprintf(&b, "VERIFICATION every change must pass: %s\n", strings.Join(q.Verify, " && "))
	}
	if q.Stop != "" {
		fmt.Fprintf(&b, "STOP CRITERIA: %s\n", q.Stop)
	}
	fmt.Fprintf(&b, "TICK: %s\n", tickID)
	return b.String()
}

// childRunPrompt is the prompt for the child run launched to do one work item. It
// frames the item against the quest's objective/scope/safety + verification so the
// child run's tech-lead/coders inherit the quest's bounds. A campaign-owned child
// commits onto the shared branch and opens no PR; a standalone child opens its own.
func childRunPrompt(q run.Quest, it questWorkItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", it.Title)
	if it.Evidence != "" {
		fmt.Fprintf(&b, "Why: %s\n", it.Evidence)
	}
	fmt.Fprintf(&b, "\nThis is one work item of the quest: %s\n", q.Objective)
	if q.Scope != "" {
		fmt.Fprintf(&b, "Stay in scope: %s\n", q.Scope)
	}
	if q.Safety != "" {
		fmt.Fprintf(&b, "Never touch: %s\n", q.Safety)
	}
	if len(q.Verify) > 0 {
		fmt.Fprintf(&b, "Every change must pass: %s\n", strings.Join(q.Verify, " && "))
	}
	return b.String()
}
