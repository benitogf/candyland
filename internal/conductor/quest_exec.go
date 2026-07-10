package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
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
// engine. The loop logic stays in Go (bounded ticks, budget
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
// CANDYLAND_QUEST_ITEM_ATTEMPTS. The ceiling exists to stop a malfunctioning loop
// (thrash, non-convergence); it does not bound legitimate convergence — a rigorous
// review legitimately consumes several rounds.
func maxItemAttempts() int { return envInt("CANDYLAND_QUEST_ITEM_ATTEMPTS", 3) }

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

// StopQuest is terminal: it cancels the drive, marks the quest stopped with the
// reason, and CASCADES to the quest's child runs (halting any that are still
// live). A stopped quest never ticks again (BeginQuest refuses it).
func (c *Conductor) StopQuest(id, reason string) bool {
	c.haltQuestDrive(id)
	if reason == "" {
		reason = "manual stop" // q4 fix: a stop always records a reason (q4 recorded none)
	}
	ok := c.UpdateQuest(id, func(q *run.Quest) {
		q.Status = "stopped"
		q.StopReason = reason
		q.PauseReason = reason
		// Stamp the quest's own coordinating agents (the quest lead) terminal so
		// the dashboard doesn't show "Working" beside a stopped quest. A live
		// drive's in-flight stream write may land after this, but its exit defer
		// re-stamps, so the final state is always terminal.
		stopInFlightAgents(q.Agents)
	})
	if ok {
		c.stopChildRuns(c.QuestChildRuns(id))
	}
	return ok
}

// ArchiveQuest clears a quest from the dashboard while keeping it in the Work
// history (hide, never delete). Storage-backed via UpdateQuest, so it works for
// tracked and untracked quests alike. Returns false for an unknown quest.
func (c *Conductor) ArchiveQuest(id string) bool {
	return c.UpdateQuest(id, func(q *run.Quest) { q.Archived = true })
}

// stopChildRuns halts every still-live run in the set (cascade from a stopped
// quest). Command reaches only tracked runs with a live executor;
// terminal/untracked children have already finished and are skipped.
func (c *Conductor) stopChildRuns(runs []run.Run) {
	for _, r := range runs {
		c.Command(r.ID, "stop")
	}
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
// quest lead, launches child runs for the accepted items, and
// records the tick. It stops when stopped/paused/blocked, when no safe work
// remains, when the token budget is exceeded, or when the tick bound is reached.
func (c *Conductor) driveQuest(ctx context.Context, id string) {
	defer c.haltQuestDrive(id)    // forget the driver on exit so a later BeginQuest can re-drive
	defer c.cleanupBusConfigs(id) // drop the quest lead's per-spawn --mcp-config files
	// Every quest-agent write happens synchronously in this goroutine (streamOnce
	// inside the ticks), so drive exit is the race-safe join point to stamp the
	// coordinating agents terminal on a stop — mirroring the run executor's
	// stopped <-done branch. StopQuest also stamps (for the no-live-drive case);
	// this defer runs after any stream write a live drive got in post-stop.
	defer func() {
		if q, ok := c.GetQuest(id); ok && q.Status == "stopped" {
			c.UpdateQuest(id, func(q *run.Quest) { stopInFlightAgents(q.Agents) })
		}
	}()

	ticks := maxQuestTicks()
	itemAttempts := map[string]int{} // work-item title → times a child run was launched (thrash cap)
	// blocked holds items whose child run FAILED but still have attempts left (a
	// transient block). Convergence re-surfaces them on a later tick for a retry
	// instead of terminating with them unresolved — they only become a durable
	// "blocked" WorkItem once they exhaust the thrash cap. Keyed by title so a
	// re-surfaced item shares the same itemAttempts counter (bounded retries).
	blocked := map[string]questWorkItem{}
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

		cont := c.runQuestTick(ctx, id, tick, itemAttempts, blocked)
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
// accepted ones, and records the Tick + updates rollups.
func (c *Conductor) runQuestTick(ctx context.Context, id string, tick int, itemAttempts map[string]int, blocked map[string]questWorkItem) bool {
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
	// q4 fix — own-artifacts triage guard: drop any surfaced item that points at the
	// loop's OWN delivery artifacts (its quest/<id> branch, or a PR
	// it already opened). Without it, discovery re-surfaces the quest's own PR/branch
	// as new work and the loop feeds on itself (q4 ticks 3–10 reconcile/supersede).
	items = dropOwnArtifacts(q, items)
	rec.DiscoverySummary = summary
	if perr != "" {
		rec.Blockers = append(rec.Blockers, perr)
		// E3: escalate this give-up exactly ONE tier up BEFORE any terminal block — a
		// standalone quest escalates to the quest-lead itself (the top tier), decided
		// autonomously and recorded, never a
		// human. A RESOLVED decision that is not a block → finish honestly (the tier
		// with authority chose to stop with what was surfaced) rather than hard-block.
		// An UNRESOLVED escalation (the same wall at the decider — e.g. a real
		// capability failure where the decider also can't start) → terminal blocked
		// WITH a schema-valid postmortem (E2 invariant).
		folders := append([]string(nil), q.Folders...)
		for i := range folders {
			folders[i] = expandHome(folders[i])
		}
		var workdir string
		var extra []string
		if len(folders) > 0 {
			workdir, extra = folders[0], extraDirsFor(folders[0], folders)
		}
		esc, resolved := c.escalateQuestDecision(ctx, q, "quest discovery could not proceed: "+perr+" — decide whether to finish with what was surfaced or block it", workdir, extra)
		if ctx.Err() != nil {
			return false // paused/stopped during escalation — not a failure
		}
		if resolved && !decisionBlocks(esc.Answer) {
			rec.NextAction = fmt.Sprintf("discovery failed but escalation resolved (%s: %s) — finishing", esc.Decider, esc.Answer)
			c.recordTick(id, rec, tokens, nil)
			c.finishQuest(ctx, id)
			return false
		}
		rec.NextAction = "blocked — discovery failed; escalation reached no path forward"
		c.recordTick(id, rec, tokens, nil)
		c.attachQuestPostmortem(id, questLeadID, "quest discovery failed: "+perr, perr) // E2: no blocked write without a postmortem
		c.UpdateQuest(id, func(q *run.Quest) {
			if q.Status == "stopped" || q.Status == "done" {
				return // a concurrent Stop/completion is authoritative — don't resurrect
			}
			q.Status = "blocked"
			q.PauseReason = perr
		})
		return false
	}

	// No safe work surfaced this tick. Normally that means the loop is done. But a
	// targeted review/feedback quest must actually review its PR before it can
	// terminate as "reviewed": if nothing surfaced and the PR was never reviewed,
	// seed the review itself as the work item so a child run runs against the PR.
	accepted := acceptedItems(items)
	// F1: the skip/block TRIAGE decisions enter the ledger too — a "skip" as a benign
	// Disposition "skipped" (the writer PR #30 removed), a "block" as Disposition
	// "blocked" that gates the clean terminal (the triage blocker rule). Without this
	// an all-skip quest recorded nothing and terminated plain "done" (the 2026-07-03
	// scar: the exact "looks-done-but-isn't" class). They are recorded whether or not
	// any item is also accepted, so triage is always durable.
	triageLedger, triageDecisions := triagedWorkItems(items, tickID)
	rec.TriageDecisions = append(rec.TriageDecisions, triageDecisions...)

	if len(items) == 0 || len(accepted) == 0 {
		if retry := resurfaceBlocked(blocked); len(retry) > 0 {
			// Convergence re-surface: nothing NEW surfaced, but items are still blocked
			// with attempts to spare. Re-surface them for a retry so a transient block
			// converges (or exhausts its cap into a durable block) rather than leaving
			// the quest to terminate with unresolved work.
			accepted = retry
			rec.DiscoverySummary = fmt.Sprintf("discovery surfaced nothing new — re-surfacing %d blocked item(s) for a convergence retry", len(retry))
		} else if seed, ok := seedReviewItem(q); ok {
			accepted = []questWorkItem{seed}
			rec.DiscoverySummary = fmt.Sprintf("discovery surfaced nothing — seeding a review of target PR #%d (it was not yet reviewed)", q.TargetPR)
		} else {
			// Record the skip ledger BEFORE finishing so an all-skip quest's surfaced
			// items are durable and questIsNoOp sees them → surfaced-only, not "done".
			if len(triageLedger) > 0 {
				rec.NextAction = "surfaced/triaged only — nothing in-scope to execute — stopping"
			} else {
				rec.NextAction = "no work surfaced — stopping"
			}
			c.recordTick(id, rec, tokens, triageLedger)
			c.finishQuest(ctx, id)
			return false
		}
	}

	// ── Launch: every accepted item launches a child run via the existing run
	//    executor. Each item gets a durable WorkItem with the real disposition (no
	//    positional guessing — the disposition is the launch outcome). The skip/block
	//    triage ledger is carried alongside the launched items. ──
	// Objective-met dedup on relaunch: a prior drive's completed items live in the
	// quest's durable ledger. On a shared-branch quest their delivery accumulated on
	// that branch (no per-finding PR), so re-surfacing one on a later drive would re-do
	// work already delivered. Close such items from the ledger evidence — no re-spawn
	// (core/completion objective-met dedup).
	delivered := deliveredTitles(q)
	if delivered != nil && !questBranchExists(ctx, q) {
		delivered = nil // shared branch gone — its delivered ledger no longer on disk; re-execute, don't dedup
	}
	ledger := triageLedger
	deduped := 0
	for i, it := range accepted {
		w := run.WorkItem{
			ID:             fmt.Sprintf("%s-w%d", tickID, i),
			SourceTick:     tickID,
			Evidence:       it.Evidence,
			Classification: it.Classification,
			Decision:       orDefault(it.Decision, "do"),
		}
		if ctx.Err() != nil {
			return false
		}
		rec.TriageDecisions = append(rec.TriageDecisions, it.Title+": do now")
		if delivered[dedupKey(it.Title)] {
			deduped++
			w.Disposition = "completed"
			w.Deduped = true
			ledger = append(ledger, w)
			delete(blocked, it.Title) // a prior drive already delivered it — stop tracking
			continue
		}
		if itemAttempts[it.Title] >= maxItemAttempts() {
			rec.Blockers = append(rec.Blockers, fmt.Sprintf("giving up on %q after %d attempts", it.Title, maxItemAttempts()))
			w.Disposition = "blocked"
			ledger = append(ledger, w)
			delete(blocked, it.Title) // durably blocked now — stop re-surfacing it
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
			if itemAttempts[it.Title] >= maxItemAttempts() {
				// Exhausted the thrash cap on this very attempt — a DURABLE block: record
				// it in the ledger (ItemsBlocked > 0 gates the terminal clean "done").
				w.Disposition = "blocked"
				ledger = append(ledger, w)
				delete(blocked, it.Title)
			} else {
				// Transient block — hold it for a convergence re-surface, not durable yet.
				blocked[it.Title] = it
			}
			continue
		}
		w.Disposition = "completed"
		ledger = append(ledger, w)
		delete(blocked, it.Title) // a prior transient block converged on retry
	}

	// A dedup-only tick did no new work: everything accepted was already delivered on
	// the shared branch by a prior drive (nothing launched, nothing blocked). Continuing
	// would re-surface the same delivered items forever, so finish instead — the quest
	// has converged (core/completion objective-met dedup).
	if deduped > 0 && len(rec.LaunchedRunIDs) == 0 && len(rec.Blockers) == 0 {
		rec.NextAction = fmt.Sprintf("all %d surfaced item(s) already delivered on the shared branch — deduped, nothing to launch — stopping", deduped)
		c.recordTick(id, rec, tokens, ledger)
		c.finishQuest(ctx, id)
		return false
	}
	switch {
	case deduped > 0 && len(rec.LaunchedRunIDs) == 0:
		rec.NextAction = fmt.Sprintf("nothing new launched (%d deduped, %d blocker(s)) — continue next tick", deduped, len(rec.Blockers))
	case deduped > 0:
		rec.NextAction = fmt.Sprintf("launched child runs (%d already-delivered item(s) deduped) — continue next tick", deduped)
	default:
		rec.NextAction = "launched child runs — continue next tick"
	}

	c.recordTick(id, rec, tokens, ledger)
	return true
}

// questDiscover spawns the quest lead for one tick and returns the parsed work
// items, a discovery summary, the tokens it consumed, and a non-empty error string
// when the discovery agent failed (couldn't start / produced no verdict). The
// quest lead runs in the quest's primary folder with the others as --add-dir
// context, with the loop/audit/completion doctrine in context — forked from the
// quest-lead session template when one resolves, loaded via kb_get by the cold
// bootstrap otherwise — and emits a WORKITEMS / WORKITEMS_NONE verdict.
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
	tokens = out.tokens
	parsed, none, ok := parseWorkItems(out.text)
	if !ok {
		// One bounded resume-and-re-ask before giving up on a missing verdict.
		model, thinking := c.agentConfig(RoleQuestLead)
		if rep, did := c.repairVerdict(ctx, id, questLeadID, out.sessionID, "WORKITEMS", primary, extra, model, thinking); did {
			tokens += rep.tokens // the repair spawn's usage still belongs to this tick
			if p, n, ok2 := parseWorkItems(rep.allText); ok2 {
				parsed, none, ok = p, n, ok2
			} else {
				return nil, "no verdict", tokens, "the quest lead " + noVerdictReason(rep, questLeadID, "WORKITEMS")
			}
		} else {
			return nil, "no verdict", tokens, "the quest lead " + noVerdictReason(out.full, questLeadID, "WORKITEMS")
		}
	}
	if none {
		return nil, "no work items surfaced", tokens, ""
	}
	return parsed, fmt.Sprintf("surfaced %d work %s", len(parsed), plural(len(parsed), "item", "items")), tokens, ""
}

// questLeadOutcome is the slice of streamOnce a discovery pass needs.
type questLeadOutcome struct {
	text      string
	tokens    int
	startErr  error
	stalled   bool
	sessionID string         // the quest lead's session, for a bounded verdict repair
	full      attemptOutcome // the raw outcome, for truthful limit-vs-clean postmortem classification
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
	model, thinking := c.agentConfig(RoleQuestLead)
	// Pre-seed the quest-lead's effective model+thinking on the quest record (L2
	// telemetry) — the self-seeding recorder would otherwise leave them empty.
	c.updateAgentHost(questID, func(agents *[]run.Agent) {
		a := ensureAgent(agents, questLeadID)
		a.Model, a.Thinking, a.Role = model, thinking, "quest-lead"
	})
	// Fork the quest-lead doctrine template when one is available: the spawn
	// resumes a session that already loaded the loop/audit/completion doctrine, so
	// the bootstrap drops the kb_get instructions (questLeadForkBootstrap). The
	// quest lead runs in the quest's primary repo folder — workdir IS the repo the
	// template was created in, so no transcript copy is needed. No template
	// (kill switch off, creation failed, …) → today's cold start, byte-for-byte;
	// a fork that doesn't resolve falls back to the full bootstrap inside the
	// same attempt (streamOnce's fallbackPrompt).
	prompt := questLeadBootstrap
	opts := spawnOpts{model: model, thinking: thinking}
	if tpl, ok := c.templateFor(RoleQuestLead, workdir); ok {
		prompt = questLeadForkBootstrap
		opts.forkFrom = tpl
		opts.fallbackPrompt = questLeadBootstrap
		opts.onForkUnresolved = func() { c.invalidateTemplate(RoleQuestLead, workdir) }
	}
	res := streamOnce(ctx, c, questID, questLeadID, prompt, workdir, extra, opts)
	// allText joins every assistant/result block, so the WORKITEMS verdict is found
	// wherever the quest lead emitted it (not only on the final block).
	return questLeadOutcome{text: res.allText, tokens: res.tokens, startErr: res.startErr, stalled: res.stalled, sessionID: res.sessionID, full: res}
}

// launchChildRun creates and drives ONE child run for an accepted work item using
// the EXISTING run flow (Create → ClaudeExecutor), with QuestID set and delivery
// per the quest's Deliver: perFinding (pr) opens its own PR; converge
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
// QuestID and a CONCRETE deliver value the moment it exists — never an empty
// deliver the frontend can't key on. A converge quest (QuestBranch non-empty)
// delivers onto the shared branch (Deliver=branch, no PR); a perFinding quest
// child opens its own PR (Deliver=pr, the default made explicit). The parent-side
// link is the WorkItem.ChildRunID ledger recorded by the tick.
func (c *Conductor) linkQuestChild(q run.Quest, spec run.Spec) string {
	childID := c.Create(spec)
	branch := QuestBranch(q)
	c.Update(childID, func(r *run.Run) {
		r.QuestID = q.ID
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
			r.Deliver = run.DeliverPR // perFinding: open its own PR (serialized, not empty)
		}
	})
	return childID
}

// childRunPRs returns the PRs a finished child run produced. A branch-delivered
// (converge) child opens no PR — its work is a commit on the shared branch,
// reported as a PR-less record so the tick still shows what landed.
func childRunPRs(r run.Run, branch string) []run.PR {
	if branch != "" {
		return nil // converge: commit onto the shared branch, no PR
	}
	return append([]run.PR(nil), r.PRs...)
}

// recordTick appends a completed tick, advances the work-item ledger, recomputes
// the rollups, and stamps token usage onto the quest in a single durable update.
func (c *Conductor) recordTick(id string, rec run.Tick, addTokens int, items []run.WorkItem) {
	rec.EndedAt = time.Now().UTC().Format(time.RFC3339)
	accounting := run.SumRunAccounting(c.QuestChildRuns(id))
	c.UpdateQuest(id, func(q *run.Quest) {
		q.Ticks = append(q.Ticks, rec)
		q.WorkItems = append(q.WorkItems, items...)
		q.TokensUsed += addTokens
		q.Accounting = accounting
		if len(rec.LaunchedRunIDs) > 0 || len(items) > 0 {
			q.LastProgress = rec.EndedAt
		}
		recomputeQuestRollups(q)
	})
}

// finishQuest moves a quest to its terminal state. A CONVERGE quest
// first opens ONE PR per impacted repo from its quest/<id> branch (via
// openBranchPRs) — its child runs accumulated their commits
// there with no per-finding PRs. A perFinding/feedback/review quest
// has already delivered per child run, so no terminal PR opens. It then chooses
// between plain "done" and the distinct "surfaced-only" no-op state (Q2). A
// concurrent Stop is authoritative and left alone.
func (c *Conductor) finishQuest(ctx context.Context, id string) {
	q, ok := c.GetQuest(id)
	if !ok {
		return
	}
	// Quest DELIVERY GATE: before any PR opens, hard-review the quest's shared branch
	// with the same review→fix→re-review primitive the run gate uses. A clean gate proceeds;
	// a non-convergent citable review blocks; structural findings requeue as pre-
	// accepted review-gap child runs and the gate re-runs. Only runs for a converge quest
	// that delivered work onto its shared branch.
	if branch := QuestBranch(q); branch != "" && (q.ItemsCompleted > 0 || q.ItemsDeduped > 0) {
		if !c.questDeliveryGate(ctx, id) {
			return // gate blocked/requeued (recorded) — never deliver un-reviewed work
		}
		q, _ = c.GetQuest(id) // refresh: the gate may have consumed rounds / added items
	}
	// The terminal per-repo PR open is I/O (git push + gh) — do it OUTSIDE the
	// UpdateQuest write lock, then persist the result in a single mutation.
	var prs []run.PR
	if branch := QuestBranch(q); strings.HasPrefix(branch, "quest/") && (q.ItemsCompleted > 0 || q.ItemsDeduped > 0) {
		prs = openBranchPRs(ctx, q.Folders, branch, questPRTitle(q), questPRBody(q))
	}
	// Convergence gate: a quest with unresolved blocked items cannot reach a clean
	// terminal — it converges to "blocked", never "done"/"reviewed"/"surfaced-only".
	// The E2 invariant demands a schema-valid postmortem before any blocked write.
	if q.ItemsBlocked > 0 {
		c.attachQuestPostmortem(id, questLeadID,
			fmt.Sprintf("%d work item(s) could not converge after %d attempts each", q.ItemsBlocked, maxItemAttempts()),
			questBlockedEvidence(q))
	}
	accounting := run.SumRunAccounting(c.QuestChildRuns(id))
	c.UpdateQuest(id, func(q *run.Quest) {
		if q.Status == "stopped" {
			return // a concurrent Stop is authoritative
		}
		q.Accounting = accounting
		if prs != nil {
			q.PRs = mergeTerminalPRs(q.PRs, prs)
			recomputeQuestRollups(q) // fold the terminal PRs into PRsOpened
		}
		q.Status = questTerminalStatus(q)
		q.Summary = questTerminalSummary(q)
		q.LastProgress = time.Now().UTC().Format(time.RFC3339)
	})
}

// questDeliveryGate runs the quest's delivery gate: a bounded review→fix→re-review
// of the shared branch. Returns true to proceed to delivery; false when the
// gate blocked the quest (recorded) or its structural-remediation could not converge.
func (c *Conductor) questDeliveryGate(ctx context.Context, id string) bool {
	for attempt := range maxReviewRounds() {
		if ctx.Err() != nil {
			return false
		}
		q, ok := c.GetQuest(id)
		if !ok {
			return false
		}
		branch := QuestBranch(q)
		if branch == "" || (q.ItemsCompleted == 0 && q.ItemsDeduped == 0) {
			return true
		}
		wtRoot := filepath.Join(os.TempDir(), "candyland-gate", id)
		delivered := map[string]string{}
		for _, repo := range q.Folders {
			repo = expandHome(repo)
			if !isGitRepo(ctx, repo) {
				continue
			}
			sha, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", branch)
			if err != nil || strings.TrimSpace(sha) == "" {
				continue // the branch carries no work in this repo
			}
			base, _ := currentBranch(ctx, repo)
			if !branchHasDiff(ctx, repo, orDefault(base, "HEAD"), branch) {
				continue // no divergence from base — nothing to review
			}
			wt := filepath.Join(wtRoot, repoBase(repo))
			if err := addDetachedWorktree(ctx, repo, wt, strings.TrimSpace(sha)); err != nil {
				removeWorktree(ctx, repo, wt)
				gateCleanup(ctx, delivered, wtRoot)
				c.failReview(ctx, id, reviewerID, "quest gate: couldn't create the review worktree for "+repoBase(repo)+": "+err.Error())
				return false
			}
			delivered[repo] = wt
		}
		if len(delivered) == 0 {
			gateCleanup(ctx, delivered, wtRoot)
			return true
		}
		fixModel, fixThinking := c.agentConfig(RoleFix)
		// A quest has no higher layer to contradict, so its root-intent channel stays
		// DISARMED (rootIntent == "") — questGateTaskIntent always renders
		// scope/safety/verify lines after the objective, so the render rule's equality
		// disarm would never fire (the same hazard fanOut handles for standalone runs).
		clean, structural := c.reviewUntilClean(ctx, q.ID, delivered, branch, questGateTaskIntent(q), "", fixModel, fixThinking)
		gateCleanup(ctx, delivered, wtRoot)
		if ctx.Err() != nil {
			return false
		}
		if !clean && len(structural) == 0 {
			return false // reviewUntilClean already blocked the quest
		}
		// Two-layer intent routing: any root-intent contradiction the reviewer
		// flagged this round (captured onto the quest by reviewUntilClean) pauses the
		// unit and asks for a ruling one tier up. `proceed` ships as-is; `fix` converts
		// each contradiction into a pre-accepted review-gap item and re-gates.
		proceed, fixIssues := c.routeQuestIntentConflicts(ctx, id, branch)
		if ctx.Err() != nil {
			return false
		}
		if !proceed {
			if !c.launchReviewGapItems(ctx, q, issuesToFindings(fixIssues), fmt.Sprintf("conflict%d", attempt+1)) {
				if ctx.Err() == nil {
					c.failReview(ctx, id, reviewerID, "quest gate: intent-conflict remediation delivered no work")
				}
				return false
			}
			continue // re-gate after reconciling the contradiction(s)
		}
		if len(structural) == 0 {
			return true // clean gate — proceed to delivery
		}
		// Structural findings need decomposition: requeue as pre-accepted review-gap
		// child runs onto the branch, then re-run the gate.
		if !c.launchReviewGapItems(ctx, q, structural, fmt.Sprintf("gate%d", attempt+1)) {
			if ctx.Err() == nil {
				c.failReview(ctx, id, reviewerID, "quest gate: review-gap remediation delivered no work")
			}
			return false
		}
	}
	c.failReview(ctx, id, reviewerID, "quest delivery gate did not converge within the round budget")
	return false
}

// routeQuestIntentConflicts handles any root-intent contradictions the gate reviewer
// flagged: it re-fetches the quest, collects the conflicts not yet
// ruled on, and — if any — pauses the unit to ask the quest-lead's tier for a ruling
// (escalateQuestDecision). The ruling is stamped on each conflict. Returns whether to
// PROCEED to delivery, and (when the ruling is `fix`) the conflict issues to reconcile.
func (c *Conductor) routeQuestIntentConflicts(ctx context.Context, id, branch string) (bool, []string) {
	q, ok := c.GetQuest(id)
	if !ok {
		return true, nil
	}
	issues := unruledConflictIssues(q.IntentConflicts)
	if len(issues) == 0 {
		return true, nil // no conflict raised — proceed exactly as today
	}
	folders := append([]string(nil), q.Folders...)
	for i := range folders {
		folders[i] = expandHome(folders[i])
	}
	var workdir string
	var extra []string
	if len(folders) > 0 {
		workdir, extra = folders[0], extraDirsFor(folders[0], folders)
	}
	esc, resolved := c.escalateQuestDecision(ctx, q, intentConflictQuestion(issues, branch), workdir, extra)
	ruling := rulingFromAnswer(resolved, esc.Answer)
	c.UpdateQuest(id, func(q *run.Quest) { stampConflictRulings(q.IntentConflicts, ruling) })
	if ruling == "fix" {
		return false, issues
	}
	return true, nil
}

// issuesToFindings wraps conflict issue strings as reviewFindings (title = issue) so
// they feed the same review-gap remediation path structural findings use.
func issuesToFindings(issues []string) []reviewFinding {
	out := make([]reviewFinding, 0, len(issues))
	for _, iss := range issues {
		out = append(out, reviewFinding{Issue: iss})
	}
	return out
}

// questGateTaskIntent renders the quest's objective + scope/safety/verify as the
// task-layer intent the gate reviewer verifies the branch against (mirrors
// childRunPrompt's rendering).
func questGateTaskIntent(q run.Quest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", q.OriginalObjective)
	if q.Scope != "" {
		fmt.Fprintf(&b, "Scope: %s\n", q.Scope)
	}
	if q.Safety != "" {
		fmt.Fprintf(&b, "Never touch: %s\n", q.Safety)
	}
	if len(q.Verify) > 0 {
		fmt.Fprintf(&b, "Every change must pass: %s\n", strings.Join(q.Verify, " && "))
	}
	return b.String()
}

// launchReviewGapItems turns each structural gate finding into a pre-accepted
// review-gap work item, launches a child run for it onto the shared branch, and
// records the ledger. Returns whether at least one child delivered.
func (c *Conductor) launchReviewGapItems(ctx context.Context, q run.Quest, structural []reviewFinding, tickID string) bool {
	rec := run.Tick{ID: tickID, StartedAt: time.Now().UTC().Format(time.RFC3339),
		DiscoverySummary: fmt.Sprintf("delivery gate: %d structural review finding(s) requeued as review-gap items", len(structural))}
	var ledger []run.WorkItem
	launchedAny := false
	for i, f := range structural {
		it := questWorkItem{Title: f.Issue, Evidence: f.Issue, Classification: "review-gap", Decision: "do"}
		w := run.WorkItem{ID: fmt.Sprintf("%s-w%d", tickID, i), SourceTick: tickID,
			Evidence: it.Evidence, Classification: it.Classification, Decision: "do"}
		rec.TriageDecisions = append(rec.TriageDecisions, it.Title+": do now")
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
			launchedAny = true
		}
		ledger = append(ledger, w)
	}
	c.recordTick(q.ID, rec, 0, ledger)
	return launchedAny
}

// mergeTerminalPRs overlays freshly-opened terminal PRs onto any already on the
// quest, but never replaces a recorded successful PR (URL set) with an errored
// re-attempt for the same repo — an idempotent re-finish that hit a transient gh
// error must not erase a real PR URL. New successful entries and new repos win.
func mergeTerminalPRs(existing, fresh []run.PR) []run.PR {
	byRepo := map[string]run.PR{}
	order := []string{}
	for _, p := range existing {
		if _, seen := byRepo[p.Repo]; !seen {
			order = append(order, p.Repo)
		}
		byRepo[p.Repo] = p
	}
	for _, p := range fresh {
		prev, seen := byRepo[p.Repo]
		if seen && prev.URL != "" && p.URL == "" {
			continue // keep the good record; don't overwrite with an errored re-attempt
		}
		if !seen {
			order = append(order, p.Repo)
		}
		byRepo[p.Repo] = p
	}
	out := make([]run.PR, 0, len(order))
	for _, repo := range order {
		out = append(out, byRepo[repo])
	}
	return out
}

// questPRTitle is the title of a converge quest's terminal PR: its display Title,
// else the first line of its objective, truncated (the prTitle primitive).
func questPRTitle(q run.Quest) string {
	if t := strings.TrimSpace(q.Title); t != "" {
		return truncate(t, 72)
	}
	return truncate(orDefault(firstLine(q.OriginalObjective), "candyland quest "+q.ID), 72)
}

// questPRBody is the body of a converge quest's terminal PR.
func questPRBody(q run.Quest) string {
	return "Delivered by a candyland quest (bounded — converge to one PR per repo).\n\n## Objective\n\n" +
		strings.TrimSpace(q.OriginalObjective) +
		"\n\n" + provenanceFooter("quest", q.ID)
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
	delivered := q.ItemsCompleted > 0 || q.ItemsDeduped > 0 || q.PRsOpened > 0
	surfaced := q.ItemsSkipped > 0 || len(q.WorkItems) > 0
	return !delivered && surfaced
}

// questDeliveryFailed reports whether a converge quest attempted its terminal
// per-repo PR delivery and EVERY attempt errored (PRs recorded, none opened). A
// quest that completed work but could push/open no PR delivered nothing — it must
// terminate "delivery-failed", never "done". A branch/
// feedback/review/perFinding quest records no terminal q.PRs, so this is false for
// them. Partial-failure isolation: one opened PR makes the delivery a real (partial)
// success.
func questDeliveryFailed(q *run.Quest) bool {
	if len(q.PRs) == 0 {
		return false // no terminal per-repo delivery was attempted
	}
	for _, pr := range q.PRs {
		if pr.URL != "" {
			return false // at least one PR opened
		}
	}
	return true
}

// questTerminalStatus is the terminal status a finished quest should carry:
// "delivery-failed" when its terminal PR delivery errored outright (never "done"),
// "blocked" when any item failed to converge (never "done"), "reviewed" for a
// review quest, "surfaced-only" for a zero-delivery no-op (Q2), else plain "done".
func questTerminalStatus(q *run.Quest) string {
	if questDeliveryFailed(q) {
		return "delivery-failed" // completed work but shipped no PR — honor the delivery failure
	}
	if q.ItemsBlocked > 0 {
		return "blocked" // convergence gate: a clean terminal requires zero blocked items
	}
	if q.Deliver == run.DeliverReview {
		return "reviewed" // a review quest opens no PR — its terminal state is "reviewed", not "done"
	}
	if questIsNoOp(q) {
		return "surfaced-only"
	}
	return "done"
}

// questBlockedEvidence names the blocked items (their evidence/classification from
// the ledger) so the terminal postmortem cites what actually failed to converge.
func questBlockedEvidence(q run.Quest) string {
	var parts []string
	for _, w := range q.WorkItems {
		if w.Disposition == "blocked" {
			ev := strings.TrimSpace(w.Classification + " " + w.Evidence)
			parts = append(parts, orDefault(ev, w.ID))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("quest %s has %d blocked item(s)", q.ID, q.ItemsBlocked)
	}
	return strings.Join(parts, "; ")
}

// questTerminalSummary names a terminal quest's outcome so a no-op is reported as
// such instead of an undifferentiated "done". For a no-op it accounts the
// surfaced/executed/PR counts.
func questTerminalSummary(q *run.Quest) string {
	if questDeliveryFailed(q) {
		return fmt.Sprintf("delivery-failed: %d item(s) completed but no PR could be opened — %s", q.ItemsCompleted, firstPRErr(q.PRs))
	}
	if q.ItemsBlocked > 0 {
		return fmt.Sprintf("blocked: %d item(s) could not converge after %d attempts (%d completed, %d PRs)",
			q.ItemsBlocked, maxItemAttempts(), q.ItemsCompleted, q.PRsOpened)
	}
	if q.Deliver == run.DeliverReview {
		if q.ItemsCompleted > 0 {
			return fmt.Sprintf("reviewed PR #%d (%d review item(s) completed)", q.TargetPR, q.ItemsCompleted)
		}
		return fmt.Sprintf("reviewed PR #%d — no actionable findings", q.TargetPR)
	}
	if questIsNoOp(q) {
		surfaced := q.ItemsSkipped + q.ItemsBlocked + q.ItemsCompleted
		return fmt.Sprintf("surfaced-only: %d surfaced, 0 executed, 0 PRs", surfaced)
	}
	// A genuinely-nothing-found quest (nothing surfaced, nothing delivered) stamps an
	// explicit terminal Summary rather than an empty one — a no-op must never look
	// like an undifferentiated "done" (Q2 / the 2026-07-03 scar).
	if q.ItemsCompleted == 0 && q.PRsOpened == 0 && len(q.WorkItems) == 0 {
		return "nothing to do: 0 surfaced"
	}
	if q.ItemsDeduped > 0 {
		return fmt.Sprintf("done: %d completed, %d already delivered (deduped), %d PRs", q.ItemsCompleted, q.ItemsDeduped, q.PRsOpened)
	}
	return ""
}

// recomputeQuestRollups derives the dashboard counters from the work-item ledger,
// the single source of truth (mirroring recompute for runs).
func recomputeQuestRollups(q *run.Quest) {
	prs, completed, skipped, blocked, deduped := 0, 0, 0, 0, 0
	for _, w := range q.WorkItems {
		switch w.Disposition {
		case "completed":
			if w.Deduped {
				deduped++
			} else {
				completed++
			}
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
	// A converge quest's terminal per-repo PRs live on q.PRs (opened at finish, not
	// on any tick), so fold them in too.
	for _, pr := range q.PRs {
		if pr.URL != "" {
			prs++
		}
	}
	q.PRsOpened = prs
	q.ItemsCompleted = completed
	q.ItemsDeduped = deduped
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
	// seeded marks the internally-generated PR-review item (seedReviewItem). It is
	// unexported and json:"-", so a quest-lead agent's parsed output can never set
	// it — only the conductor can.
	seeded bool `json:"-"`
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

// dedupKey normalizes a work-item title for objective-met dedup matching (case-
// and surrounding-whitespace-insensitive), so a re-surfaced item matches its prior
// completed ledger entry regardless of incidental casing/spacing differences.
func dedupKey(title string) string {
	return strings.ToLower(strings.TrimSpace(title))
}

// deliveredTitles is the set of work-item titles the quest has ALREADY completed,
// recovered from its durable ledger — the evidence a relaunch uses to skip work a
// prior drive delivered on the shared branch (core/completion objective-met dedup).
// It is empty for a quest with no shared branch (a per-finding/PR quest opens a PR
// per finding and guards re-surfacing via dropOwnArtifacts, so ledger dedup does not
// apply), keyed by dedupKey so matching is stable across casing/spacing.
func deliveredTitles(q run.Quest) map[string]bool {
	if QuestBranch(q) == "" {
		return nil // no shared branch — nothing accumulates across drives to dedup against
	}
	ticks := make(map[string]run.Tick, len(q.Ticks))
	for _, t := range q.Ticks {
		ticks[t.ID] = t
	}
	done := map[string]bool{}
	for _, w := range q.WorkItems {
		if w.Disposition != "completed" {
			continue
		}
		title, derived := workItemTitleDerived(w, ticks[w.SourceTick])
		if !derived {
			continue
		}
		if k := dedupKey(title); k != "" {
			done[k] = true
		}
	}
	return done
}

// questBranchExists reports whether the quest's shared delivery branch still
// exists in its primary repo. Objective-met dedup trusts the ledger only while
// the branch holding the delivered commits is actually present — a reset or
// deleted branch means that work is gone and re-surfacing it must re-execute,
// not dedup-close.
func questBranchExists(ctx context.Context, q run.Quest) bool {
	branch := QuestBranch(q)
	if branch == "" || len(q.Folders) == 0 {
		return false
	}
	repo := expandHome(q.Folders[0])
	sha, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", branch)
	return err == nil && strings.TrimSpace(sha) != ""
}

// resurfaceBlocked returns the transiently-blocked items held for a convergence
// retry (a child run failed but the item still has thrash-cap attempts left). It
// is the "re-surface" half of convergence: when a tick discovers nothing new, the
// loop re-launches these instead of terminating with them unresolved. An item is
// only in this set while attempts remain; once it exhausts the cap it becomes a
// durable "blocked" WorkItem and is dropped from the map.
func resurfaceBlocked(blocked map[string]questWorkItem) []questWorkItem {
	if len(blocked) == 0 {
		return nil
	}
	out := make([]questWorkItem, 0, len(blocked))
	for _, it := range blocked {
		out = append(out, it)
	}
	return out
}

// triagedWorkItems builds the durable ledger for the items triage decided NOT to
// launch a child run for. A "skip" is BENIGN — surfaced but out of scope — and
// records Disposition "skipped" (the writer PR #30 removed, F1). A "block" is a
// triage-declared BLOCKER: the quest lead judged the item as work the loop cannot
// safely proceed on, so it records Disposition "blocked", which gates a clean
// terminal exactly like a child-run failure that exhausts its attempts
// (questTerminalStatus → "blocked", with an E2 postmortem). It also returns the
// triage-decision lines for the tick record. Accepted items ("do"/empty) are
// excluded (they launch child runs and get their own ledger entries).
func triagedWorkItems(items []questWorkItem, tickID string) ([]run.WorkItem, []string) {
	var ledger []run.WorkItem
	var decisions []string
	n := 0
	for _, it := range items {
		d := strings.ToLower(strings.TrimSpace(it.Decision))
		if d != "skip" && d != "block" {
			continue
		}
		disposition := "skipped"
		if d == "block" {
			disposition = "blocked" // a triage block gates the clean terminal, unlike a skip
		}
		ledger = append(ledger, run.WorkItem{
			ID:             fmt.Sprintf("%s-s%d", tickID, n),
			SourceTick:     tickID,
			Evidence:       it.Evidence,
			Classification: it.Classification,
			Decision:       d,
			Disposition:    disposition,
		})
		decisions = append(decisions, it.Title+": "+d)
		n++
	}
	return ledger, decisions
}

// ownArtifactTokens are the lowercased strings that identify a quest's OWN delivery
// artifacts: its shared branch (quest/<id>) and every PR URL it has
// already opened (on prior ticks or its terminal delivery). A surfaced work item that
// mentions any of these is the loop rediscovering its own output.
func ownArtifactTokens(q run.Quest) []string {
	var toks []string
	if b := QuestBranch(q); b != "" {
		toks = append(toks, strings.ToLower(b))
	}
	for _, t := range q.Ticks {
		for _, pr := range t.PRs {
			if pr.URL != "" {
				toks = append(toks, strings.ToLower(pr.URL))
			}
		}
	}
	for _, pr := range q.PRs {
		if pr.URL != "" {
			toks = append(toks, strings.ToLower(pr.URL))
		}
	}
	return toks
}

// dropOwnArtifacts filters out work items that reference the quest's own delivery
// artifacts (its branch or an already-opened PR), so discovery can never feed the
// loop its own output as new work (q4 fix 3). Items are matched against title,
// evidence, and classification.
func dropOwnArtifacts(q run.Quest, items []questWorkItem) []questWorkItem {
	own := ownArtifactTokens(q)
	if len(own) == 0 || len(items) == 0 {
		return items
	}
	out := make([]questWorkItem, 0, len(items))
	for _, it := range items {
		hay := strings.ToLower(it.Title + " " + it.Evidence + " " + it.Classification)
		self := false
		for _, tok := range own {
			if strings.Contains(hay, tok) {
				self = true
				break
			}
		}
		if !self {
			out = append(out, it)
		}
	}
	return out
}

// reviewClassification is the classification tag on the seeded PR-review work item
// (surfaced in the tick ledger/UI).
const reviewClassification = "pr-review"

// isTargetedReviewQuest reports whether a quest's whole job is to examine one
// EXISTING PR (review or feedback delivery against a concrete target PR).
func isTargetedReviewQuest(q run.Quest) bool {
	return q.TargetPR > 0 && (q.Deliver == run.DeliverReview || q.Deliver == run.DeliverFeedback)
}

// questReviewRan reports whether a prior tick already launched a child run (i.e.
// the target PR was actually reviewed once), so a later empty discovery is a
// legitimate "no more findings" rather than a no-op that skipped the review.
func questReviewRan(q run.Quest) bool {
	for _, t := range q.Ticks {
		if len(t.LaunchedRunIDs) > 0 {
			return true
		}
	}
	return false
}

// seedReviewItem returns the PR review itself as the work item when a targeted
// review/feedback quest surfaced nothing but has not yet actually reviewed its PR.
// Without it, such a quest terminates as "reviewed (no actionable findings)"
// without ever fetching the PR — the review that never ran. The child run this
// seeds carries the target PR + the review/feedback delivery machinery and the
// gh/diff tooling, so it performs the real review.
func seedReviewItem(q run.Quest) (questWorkItem, bool) {
	if !isTargetedReviewQuest(q) || questReviewRan(q) {
		return questWorkItem{}, false
	}
	verb := "Review"
	if q.Deliver == run.DeliverFeedback {
		verb = "Review and address feedback on"
	}
	return questWorkItem{
		Title:          fmt.Sprintf("%s PR #%d and address any findings", verb, q.TargetPR),
		Evidence:       "a review/feedback quest must actually read its target PR before it can report on it",
		Classification: reviewClassification,
		Decision:       "do",
		seeded:         true,
	}, true
}

// --- prompts (composition, not inlined rubrics) ---

// questLeadIntro / questLeadDiscoverRules are the two halves every quest-lead
// bootstrap shares. The full and fork variants below differ ONLY in the doctrine
// sentence between them, so the shared text can never drift between the two.
const questLeadIntro = "You are the quest lead driving one tick of an iterative work loop. " +
	"Call the brief_get tool FIRST to read the quest's objective, scope, safety boundary, and verification — it is no longer on your command line." + briefGetToolHint + ". "

const questLeadDiscoverRules = "Discover the next safe, in-scope work item(s): if the brief names a TARGET PR, that PR IS the subject — you MUST actually fetch and read it (its diff and review comments, e.g. `gh pr diff <n>` / `gh pr view <n>`) and base every finding on what you read; otherwise explore the folder for concrete work. Then TRIAGE each (is it safe? in scope? a single self-contained change?). " +
	"Never emit WORKITEMS_NONE for a TARGET PR without having read that PR first. " +
	"Then emit EXACTLY ONE verdict line and stop: either `WORKITEMS_NONE` (no safe in-scope work remains this tick) " +
	"OR `WORKITEMS ` followed by a JSON array " + `[{"title":"…","evidence":"why it's needed","classification":"category","decision":"do|skip|block"}]` +
	" listing only items you triaged as safe and in scope (decision \"do\"); use \"skip\"/\"block\" for items you surfaced but will not act on. " +
	"A missing prerequisite you do not own — an instrument, artifact, or change another unit is responsible for — is recorded as a BLOCKER (decision \"block\", its evidence naming the owning unit); NEVER re-derive it as this quest's own work item (that duplicates another unit's work). " +
	"Do not ask questions and do not defer." + incidentDoctrine

// questLeadBootstrap is the CONSTANT discovery/triage prompt. Like the tech-lead /
// coder bootstraps it carries no quest context on argv (that rides the brief via
// brief_get); it tells the quest lead to load the detritus loop/audit/completion
// doctrine via kb_get and APPLY it, then emit a structured WORKITEMS verdict. It
// must NOT inline a rubric — the doctrine is the rubric (the Composition Constraint).
const questLeadBootstrap = questLeadIntro +
	"Load and APPLY the detritus doctrine via the kb_get tool: kb_get name=\"core/loop\" (loop fundamentals: cadence, skip-streak, durability), " +
	"kb_get name=\"core/todo-audit\" (how to discover, prioritize, and fork-gate work items), and kb_get name=\"core/completion\" (the three dispositions and the definition of done). " +
	"Do NOT improvise your own rubric — use the doctrine you loaded. " +
	questLeadDiscoverRules

// questLeadForkBootstrap is the slim variant for a spawn that FORKS the quest-lead
// doctrine template (session_template.go): the template session has already run the
// kb_get loads, so the bootstrap drops them and points at the loaded doctrine
// instead. Everything else is questLeadBootstrap verbatim (the shared halves above).
const questLeadForkBootstrap = questLeadIntro +
	"Do NOT improvise your own rubric — apply the doctrine already loaded in this session. " +
	questLeadDiscoverRules

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
	// A review/feedback quest targets an EXISTING PR — name it and make reading it a
	// precondition of any verdict, so the lead can't report "no findings" on a PR it
	// never opened.
	if q.TargetPR > 0 && (q.Deliver == run.DeliverReview || q.Deliver == run.DeliverFeedback) {
		mode := "REVIEW"
		if q.Deliver == run.DeliverFeedback {
			mode = "FEEDBACK"
		}
		fmt.Fprintf(&b, "TARGET PR: #%d — this is %s work on that EXISTING PR. Fetch and read the PR (its diff and review comments) BEFORE surfacing anything; every finding must come from what you read. Do NOT emit WORKITEMS_NONE without having read PR #%d.\n", q.TargetPR, mode, q.TargetPR)
	}
	// Own-artifacts guard (q4 fix 3): tell the lead never to surface the quest's own
	// delivery artifacts as work. Name the branch it delivers on and any PR it opened
	// so a rediscovery of them is unambiguous.
	if branch := QuestBranch(q); branch != "" {
		fmt.Fprintf(&b, "OWN DELIVERY BRANCH (never surface it or work items about it): %s\n", branch)
	}
	if urls := openedPRURLs(q); len(urls) > 0 {
		fmt.Fprintf(&b, "YOUR OWN OPEN PRs (never surface them as new work — do not reconcile/supersede your own output): %s\n", strings.Join(urls, ", "))
	}
	b.WriteString("Do NOT surface, reconcile, or supersede your own prior deliveries (the branch/PRs above) — those are your output, not new work.\n")
	fmt.Fprintf(&b, "TICK: %s\n", tickID)
	// Decision memory: the durable triage ledger of every prior tick, so this
	// tick can neither re-surface an item already decided nor contradict the
	// decision. Empty history renders nothing — a first tick's brief is
	// byte-identical to one without this section.
	b.WriteString(priorTicksSection(q))
	return b.String()
}

// maxPriorTickLines bounds the PRIOR TICKS section of the tick brief: at most
// this many work-item lines, oldest dropped first (a long-running quest's brief
// must stay a brief, not a transcript).
const maxPriorTickLines = 30

// priorTicksSection renders the quest's work-item ledger — every item prior
// ticks triaged, one line each: what it was → the triage decision → the launch
// outcome. Pure function of the record so it is directly unit-testable. Returns
// "" when no items have been recorded yet.
func priorTicksSection(q run.Quest) string {
	if len(q.WorkItems) == 0 {
		return ""
	}
	ticks := make(map[string]run.Tick, len(q.Ticks))
	for _, t := range q.Ticks {
		ticks[t.ID] = t
	}
	// q.WorkItems is appended per tick in tick order (recordTick), so the slice
	// is already chronological — the bound drops from the front (oldest).
	lines := make([]string, 0, len(q.WorkItems))
	for _, w := range q.WorkItems {
		lines = append(lines, workItemLine(w, ticks[w.SourceTick]))
	}
	var b strings.Builder
	b.WriteString("PRIOR TICKS (already triaged — do not re-surface, do not contradict):\n")
	if drop := len(lines) - maxPriorTickLines; drop > 0 {
		fmt.Fprintf(&b, "(+%d earlier items)\n", drop)
		lines = lines[drop:]
	}
	for _, ln := range lines {
		b.WriteString("- " + ln + "\n")
	}
	return b.String()
}

// workItemLine is one ledger line: title → decision → outcome.
func workItemLine(w run.WorkItem, t run.Tick) string {
	return workItemTitle(w, t) + " → " + orDefault(w.Decision, "do") + " → " + workItemOutcome(w, t)
}

// workItemOutcome renders the item's recorded end state: the disposition plus,
// when a child run was launched, its id — and for a blocked item the tick's
// recorded failure one-liner.
func workItemOutcome(w run.WorkItem, t run.Tick) string {
	launched := ""
	if w.ChildRunID != "" {
		launched = " (run " + w.ChildRunID + ")"
	}
	switch w.Disposition {
	case "completed":
		return "done" + launched
	case "skipped":
		return "not launched"
	case "blocked":
		out := "failed" + launched
		if why := workItemFailure(t, workItemTitle(w, t)); why != "" {
			out += ": " + truncate(why, 160)
		}
		return out
	}
	return orDefault(w.Disposition, "unknown") + launched
}

// workItemFailure finds the tick's recorded blocker for the item: a launch
// failure is recorded as "<title>: <err>", a thrash-cap give-up quotes the
// title (`giving up on "<title>" after N attempts`).
func workItemFailure(t run.Tick, title string) string {
	if title == "" {
		return ""
	}
	for _, blk := range t.Blockers {
		if rest, ok := strings.CutPrefix(blk, title+": "); ok {
			return rest
		}
		if strings.Contains(blk, strconv.Quote(title)) {
			return blk
		}
	}
	return ""
}

// workItemTitle recovers the surfaced item's title. A WorkItem stores no title,
// but its originating tick recorded one triage-decision line per item —
// "<title>: do now" per accepted item in launch order, "<title>: skip|block"
// per skipped item in ledger order — and the item id encodes its index within
// its kind ("<tick>-w<i>" launched, "<tick>-s<n>" skip/block; see runQuestTick /
// triagedWorkItems). When the derivation misses (foreign id shape, rewritten
// history) it falls back to the item's evidence, classification, then id.
func workItemTitle(w run.WorkItem, t run.Tick) string {
	title, _ := workItemTitleDerived(w, t)
	return title
}

// workItemTitleDerived recovers a work item's title and reports whether the
// result is a REAL derivation (a matched triage-decision line, or the item's
// evidence text) rather than a weak fallback (classification or raw id). Dedup
// evidence must use only real derivations — a generic fallback like the
// classification "cleanup" would collapse every future item titled "cleanup".
func workItemTitleDerived(w run.WorkItem, t run.Tick) (string, bool) {
	kind, idx := workItemIndex(w.ID, w.SourceTick)
	var suffixes []string
	switch kind {
	case "w":
		suffixes = []string{": do now"}
	case "s":
		suffixes = []string{": skip", ": block"}
	}
	seen := 0
	for _, d := range t.TriageDecisions {
		for _, suf := range suffixes {
			if !strings.HasSuffix(d, suf) {
				continue
			}
			if seen == idx {
				return strings.TrimSuffix(d, suf), true
			}
			seen++
			break
		}
	}
	if ev := strings.TrimSpace(w.Evidence); ev != "" {
		return truncate(ev, 80), true
	}
	if cl := strings.TrimSpace(w.Classification); cl != "" {
		return cl, false
	}
	return w.ID, false
}

// workItemIndex splits a ledger id "<tick>-w<i>" / "<tick>-s<n>" into its kind
// and index. ("", -1) for any other shape — the caller falls back.
func workItemIndex(id, tick string) (kind string, idx int) {
	rest, ok := strings.CutPrefix(id, tick+"-")
	if !ok || len(rest) < 2 {
		return "", -1
	}
	n, err := strconv.Atoi(rest[1:])
	if err != nil || n < 0 {
		return "", -1
	}
	return rest[:1], n
}

// openedPRURLs collects every PR URL a quest has opened (prior ticks + terminal
// delivery), for naming the loop's own artifacts in the brief.
func openedPRURLs(q run.Quest) []string {
	var urls []string
	for _, t := range q.Ticks {
		for _, pr := range t.PRs {
			if pr.URL != "" {
				urls = append(urls, pr.URL)
			}
		}
	}
	for _, pr := range q.PRs {
		if pr.URL != "" {
			urls = append(urls, pr.URL)
		}
	}
	return urls
}

// childRunPrompt is the prompt for the child run launched to do one work item. It
// frames the item against the quest's objective/scope/safety + verification so the
// child run's tech-lead/coders inherit the quest's bounds. A converge child
// commits onto the shared branch and opens no PR; a perFinding child opens its own.
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
