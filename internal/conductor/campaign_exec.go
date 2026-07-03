package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/candyland/internal/run"
)

// This file owns the campaign EXECUTION layer — the program-level supervisor. A
// campaign is the full intent→delivery cycle: once BeginCampaign launches it, the
// supervisor NEVER asks the user; roles decide/escalate within the hierarchy. The
// supervisor drives a bounded sequence of stages, recording each on the Campaign:
//
//	1. INTENT BRIEF  — an intent-lead agent restates the immutable OriginalInput
//	   into a structured brief (goal, scope-by-domain, commitments, draft tasks,
//	   dependencies, review-routing). The agent loads core/planning + core/dream via
//	   kb_get and APPLIES them — no inlined rubric (the Composition Constraint).
//	2. BRIEF GATE    — a deterministic consistency check that the brief reflects the
//	   OriginalInput before planning proceeds. A failed gate routes back to the
//	   intent lead (bounded) — it never asks the user.
//	3. DECOMPOSE     — the TECH MANAGER partitions the brief into concurrent child
//	   QUESTS, emitting one machine-readable QUESTS line. Each becomes a child quest
//	   with CampaignID set, so its runs COMMIT onto the campaign branch (campaign/<id>
//	   — the same name in each impacted repo) and open NO PR.
//	4. GATE 1        — the INTENT MANAGER reviews the quest partition against the brief
//	   ("would these quests plausibly deliver the commitments?"); disagreement routes
//	   back to the tech manager, bounded by maxPartitionAttempts. Both-agree required.
//	5. EXECUTE       — run the child quests CONCURRENTLY (deps sequence dependents);
//	   their work accumulates on the shared campaign branch; ctx halts them on stop.
//	6. GATE 2        — dual sign-off: the INTENT MANAGER emits a per-commitment verdict
//	   {satisfied|partial|missed} (core/intent-review), and the TECH MANAGER confirms
//	   technical done. Both load their doctrine via kb_get (composition, not a rubric).
//	7. DELIVERY GATE — a `missed` verdict (or withheld technical sign-off) feeds a
//	   remediation QUEST via the tech manager, bounded by maxRemediationRounds; a
//	   `partial` annotates but does NOT block. Only both-clean opens ONE PR PER REPO
//	   from the campaign branch (reusing the run push+openPR machinery).
//
// The loop logic stays in Go (bounded stages, a global token cap);
// the INTELLIGENCE (the brief, the per-commitment judgment) lives in the agents,
// which compose the detritus doctrine via kb_get rather than re-encoding it here.

// The campaign runs exactly TWO high-level roles, each instantiated as fresh
// claude -p processes per stage (fresh instantiation = maker≠checker):
//
//   - the INTENT MANAGER owns intent gating end-to-end — it produces the Intent
//     Brief (intentLeadID), reviews the tech manager's quest partition against the
//     brief at gate 1 (intentManagerID), and runs the final per-commitment intent
//     review at gate 2 (intentReviewerID). It composes core/planning + core/dream
//     (brief) and core/intent-review (review) via kb_get.
//   - the TECH MANAGER owns everything technical — it partitions the brief into
//     concurrent child QUESTS (techManagerID, the QUESTS line), and confirms
//     technical done at gate 2. It composes roles/tech-lead via kb_get.
//
// Each id keys its agent's brief on the bus the same way tl/coder ids do.
const (
	intentLeadID     = "intent-lead"     // stage 1: the Intent Brief
	intentManagerID  = "intent-manager"  // gate 1: the intent manager reviewing the quest partition
	intentReviewerID = "intent-reviewer" // gate 2: the final per-commitment intent review
	techManagerID    = "tech-manager"    // decompose (QUESTS) + gate 2 technical-done confirmation
)

// maxPartitionAttempts bounds gate 1 (the partition-convergence gate): how many
// times a disagreement between the tech manager's quest partition and the intent
// manager's review routes back to the tech manager before the campaign blocks. It
// is the partition-phase sibling of maxBriefAttempts. Tunable via
// CANDYLAND_CAMPAIGN_PARTITION_ATTEMPTS.
func maxPartitionAttempts() int { return envInt("CANDYLAND_CAMPAIGN_PARTITION_ATTEMPTS", 2) }

// campaignConcurrency caps how many independent child quests execute at once (the
// dep DAG still sequences dependents behind their dependencies). Tunable via
// CANDYLAND_CONCURRENCY; the default keeps a program from spawning an unbounded
// number of quest process trees simultaneously.
func campaignConcurrency() int { return envInt("CANDYLAND_CONCURRENCY", 4) }

// maxBriefAttempts bounds how many times a failed BRIEF GATE routes back to the
// intent lead before the campaign blocks — the brief-phase analogue of maxReplans.
// Tunable via CANDYLAND_CAMPAIGN_BRIEF_ATTEMPTS.
func maxBriefAttempts() int { return envInt("CANDYLAND_CAMPAIGN_BRIEF_ATTEMPTS", 2) }

// maxRemediationRounds bounds how many times the DELIVERY GATE spawns remediation
// child runs to close commitments the intent review judged unmet before it blocks.
// A campaign never parks in "blocked" on the FIRST review while it still has the
// authority to finish the work: an unmet commitment routes back into a fresh child
// run targeting exactly that commitment, then re-reviews — bounded here so a
// genuinely un-closeable gap eventually blocks (a real hard blocker) instead of
// looping forever. Tunable via CANDYLAND_CAMPAIGN_REMEDIATION_ROUNDS.
func maxRemediationRounds() int { return envInt("CANDYLAND_CAMPAIGN_REMEDIATION_ROUNDS", 2) }

// campaignTokenCap is the global token cap across the whole campaign. A campaign's
// own TokenBudget (from the spec) takes precedence when set; otherwise this env cap
// applies. 0 (neither set) means no cap. When exceeded the supervisor degrades to
// deliver-partial rather than a pre-PR pause that strands with no PR (settled
// decision): it skips remaining children and proceeds to review + delivery.
func campaignTokenCap() int { return envInt("CANDYLAND_CAMPAIGN_TOKEN_CAP", 0) }

// campaignDriver tracks a campaign's running supervisor goroutine so pause/stop can
// halt it (id → cancel), mirroring questDriver.
type campaignDriver struct {
	cancel context.CancelFunc
}

// BeginCampaign starts (or resumes) a campaign's supervisor in a goroutine,
// mirroring BeginQuest. It is idempotent (a campaign already being driven is left
// alone), refuses a terminal (stopped/done) campaign, and resumes a paused one.
func (c *Conductor) BeginCampaign(id string) bool {
	cam, ok := c.GetCampaign(id)
	if !ok {
		return false
	}
	if cam.Status == "stopped" || cam.Status == "done" {
		return false // terminal — start a new campaign instead
	}

	c.mu.Lock()
	if c.campaignDrivers == nil {
		c.campaignDrivers = map[string]*campaignDriver{}
	}
	if _, running := c.campaignDrivers[id]; running {
		c.mu.Unlock()
		return true // already driving — idempotent (a double POST can't spawn two supervisors)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.campaignDrivers[id] = &campaignDriver{cancel: cancel}
	c.mu.Unlock()

	c.UpdateCampaign(id, func(cam *run.Campaign) {
		if cam.Status == "paused" || cam.Status == "blocked" || cam.Status == "" {
			cam.Status = "running"
			cam.PauseReason = ""
		}
	})
	log.Printf("candyland: campaign %s supervisor started", id)
	go c.driveCampaign(ctx, id)
	return true
}

// StopCampaign is terminal: it cancels the supervisor and marks the campaign stopped
// with the reason. A stopped campaign never runs again (BeginCampaign refuses it). It
// also stops any in-flight child runs so the process trees don't outlive the campaign.
func (c *Conductor) StopCampaign(id, reason string) bool {
	c.haltCampaignDrive(id)
	c.stopCampaignChildren(id)
	if reason == "" {
		reason = "manual stop" // q4 fix: a stop always records a reason (q4 recorded none)
	}
	return c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.Status = "stopped"
		cam.StopReason = reason
		cam.PauseReason = reason
	})
}

// ArchiveCampaign clears a campaign from the dashboard while keeping it in the
// Work history (hide, never delete). Storage-backed via UpdateCampaign, so it
// works for tracked and untracked campaigns alike. Returns false for an unknown one.
func (c *Conductor) ArchiveCampaign(id string) bool {
	return c.UpdateCampaign(id, func(cam *run.Campaign) { cam.Archived = true })
}

// haltCampaignDrive cancels and forgets a campaign's running supervisor (if any).
// Returns true when a live drive was halted.
func (c *Conductor) haltCampaignDrive(id string) bool {
	c.mu.Lock()
	d := c.campaignDrivers[id]
	delete(c.campaignDrivers, id)
	c.mu.Unlock()
	if d == nil {
		return false
	}
	d.cancel()
	return true
}

// stopCampaignChildren stops every still-running child run of a campaign (best-effort).
func (c *Conductor) stopCampaignChildren(id string) {
	// Halt live child runs. CampaignChildRuns covers grandchild runs too (a child
	// quest's runs inherit the CampaignID), so this reaches the whole run subtree.
	c.stopChildRuns(c.CampaignChildRuns(id))
	// Cascade to child quests: mark them stopped and halt their tick drives so no
	// quest keeps ticking (and launching runs) after the campaign is stopped.
	for _, q := range c.CampaignChildQuests(id) {
		if q.Status != "stopped" && q.Status != "done" {
			c.StopQuest(q.ID, "campaign stopped")
		}
	}
}

// CampaignChildRuns returns every run whose CampaignID == id, read from storage so
// it covers finished/untracked runs too (mirrors QuestChildRuns).
func (c *Conductor) CampaignChildRuns(id string) []run.Run {
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
		if json.Unmarshal(obj.Data, &r) == nil && r.CampaignID == id {
			out = append(out, cloneRun(r))
		}
	}
	return out
}

// CampaignChildQuests returns every quest whose CampaignID == id. Child quests are
// optional for v1 (direct child runs are sufficient — see DECOMPOSE), so this is
// usually empty; the endpoint exists so the dashboard can surface them when present.
func (c *Conductor) CampaignChildQuests(id string) []run.Quest {
	var out []run.Quest
	for _, q := range c.ListQuests() {
		if q.CampaignID == id {
			out = append(out, q)
		}
	}
	return out
}

// driveCampaign runs the bounded stage sequence. Each stage records its result on
// the Campaign and updates Status; the supervisor halts cooperatively on ctx (a
// pause/stop). It never asks the user — a failed gate routes back (bounded), a
// `missed` verdict blocks delivery with a visible reason, and the campaign branch
// persists for resume.
func (c *Conductor) driveCampaign(ctx context.Context, id string) {
	defer c.haltCampaignDrive(id)
	defer c.cleanupBusConfigs(intentLeadID)
	defer c.cleanupBusConfigs(intentManagerID)
	defer c.cleanupBusConfigs(intentReviewerID)
	defer c.cleanupBusConfigs(techManagerID)

	cam, ok := c.GetCampaign(id)
	if !ok {
		return
	}
	folders := campaignFolders(cam)
	if len(folders) == 0 {
		c.blockCampaign(id, "the campaign has no folders (launch it with at least the git repo to work in)")
		return
	}

	// ── Stage 1+2: INTENT BRIEF + BRIEF GATE (bounded route-back). ──
	brief, ok := c.briefUntilGated(ctx, id, cam, folders)
	if ctx.Err() != nil {
		return // paused/stopped mid-brief — not a failure
	}
	if !ok {
		return // blocked (gate failed past the bound, or the lead produced no brief) — recorded
	}

	// ── Stage 3+4: DECOMPOSE into child QUESTS (the tech manager) + GATE 1 (the intent
	//    manager reviews the partition against the brief), a bounded convergence loop. ──
	quests, ok := c.partitionUntilGated(ctx, id, cam, brief, folders)
	if ctx.Err() != nil {
		return // paused/stopped mid-gate — not a failure
	}
	if !ok {
		return // blocked (no partition, or gate 1 never converged) — recorded
	}

	// ── Stage 5: EXECUTE the child quests concurrently on the shared campaign branch
	//    (deps sequence dependents; independent quests run in parallel). ──
	if !c.executeChildQuests(ctx, id, cam, folders, quests) {
		return // stopped, or every quest failed and there is nothing to review — recorded
	}
	if ctx.Err() != nil {
		return
	}

	// ── Stage 6+7: GATE 2 (dual sign-off) → REMEDIATE → DELIVER. ──
	// Both managers must sign off before delivery: the intent manager runs the
	// per-commitment intent review, and the tech manager confirms technical done. A
	// `missed` verdict (or a not-done technical verdict) does NOT park the campaign
	// in "blocked" on the first look — the tech manager spawns a remediation QUEST
	// targeting exactly the gap, the loop re-reviews, and only both-clean delivers.
	// Bounded by maxRemediationRounds so a genuinely un-closeable gap eventually
	// blocks (a real hard blocker). A lingering `partial` annotates the PR, never blocks.
	rounds := maxRemediationRounds()
	var review run.IntentReview
	for round := 0; ; round++ {
		var ok bool
		review, ok = c.intentReview(ctx, id, cam, brief, folders)
		if ctx.Err() != nil {
			return
		}
		if !ok {
			return // the reviewer produced no verdict (blocked) — recorded
		}
		techDone, ok := c.techManagerDone(ctx, id, cam, brief, review, folders)
		if ctx.Err() != nil {
			return
		}
		if !ok {
			return // the tech manager produced no verdict (blocked) — recorded
		}

		missed := missedCommitments(brief, review)
		if len(missed) == 0 && techDone.Done {
			break // dual sign-off clean — deliver (partials annotate the PR)
		}
		gaps := gapSummary(missed, techDone)
		if round >= rounds {
			c.blockCampaign(id, fmt.Sprintf("gate 2 still finds gaps after %d remediation round(s) — %s. The campaign branch persists; resume to retry.", rounds, gaps))
			return
		}

		remediation := remediationQuests(cam, brief, review, techDone)
		if len(remediation) == 0 {
			// Defensive: a gap with no addressable target — block rather than loop
			// with nothing to spawn.
			c.blockCampaign(id, fmt.Sprintf("gate 2 found gaps with no remediable target — %s. The campaign branch persists; resume to retry.", gaps))
			return
		}
		c.appendCampaignNote(id, fmt.Sprintf("remediation round %d/%d: the tech manager spawns %d quest(s) to close %s", round+1, rounds, len(remediation), gaps))
		if !c.executeChildQuests(ctx, id, cam, folders, remediation) {
			// executeChildQuests blocked with a generic "no quest delivered work"
			// reason (or ctx was cancelled). When it's the former, replace it with the
			// actionable gap context so the operator sees WHICH commitments are stuck.
			if ctx.Err() == nil {
				c.blockCampaign(id, fmt.Sprintf("remediation delivered no work for %s. The campaign branch persists; resume to retry.", gaps))
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		cam, _ = c.GetCampaign(id) // refresh so the next review sees remediation state
	}

	c.deliverCampaign(ctx, id, folders, brief, review)
}

// briefUntilGated runs the INTENT BRIEF stage (Stage 1) and the BRIEF GATE (Stage 2)
// in a bounded loop: a failed gate weaves the reason into the next intent-lead spawn
// (route-back, never a user prompt). It returns the settled brief, or false having
// blocked the campaign when the bound is exhausted or the lead produced no brief.
func (c *Conductor) briefUntilGated(ctx context.Context, id string, cam run.Campaign, folders []string) (run.IntentBrief, bool) {
	attempts := maxBriefAttempts()
	feedback := ""
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctx.Err() != nil {
			return run.IntentBrief{}, false
		}
		c.UpdateCampaign(id, func(cam *run.Campaign) {
			if cam.Status == "stopped" || cam.Status == "done" {
				return // a concurrent Stop/completion is authoritative — don't resurrect
			}
			cam.Status = "running"
			cam.PauseReason = ""
		})
		brief, tokens, errMsg := c.emitIntentBrief(ctx, id, cam, folders, feedback)
		c.addCampaignTokens(id, tokens)
		if ctx.Err() != nil {
			return run.IntentBrief{}, false
		}
		if errMsg != "" {
			c.blockCampaign(id, "intent brief: "+errMsg)
			return run.IntentBrief{}, false
		}
		c.UpdateCampaign(id, func(cam *run.Campaign) {
			cam.IntentBrief = brief
			cam.ReviewRouting = append([]string(nil), brief.ReviewRouting...)
		})

		reason, passed := briefGate(cam.OriginalInput, brief)
		c.recordBriefGate(id, passed, reason)
		if passed {
			return brief, true
		}
		feedback = reason
	}
	c.blockCampaign(id, fmt.Sprintf("the intent brief failed its consistency gate after %d attempts", attempts))
	return run.IntentBrief{}, false
}

// emitIntentBrief spawns the intent lead for one attempt and parses its INTENT_BRIEF
// verdict. The lead runs in the campaign's primary folder with the rest as --add-dir
// context; its PROMPT instructs it to load core/planning + core/dream via kb_get and
// APPLY them (no inlined rubric). feedback (non-empty on a route-back) is woven into
// the brief so the lead corrects the prior brief.
func (c *Conductor) emitIntentBrief(ctx context.Context, id string, cam run.Campaign, folders []string, feedback string) (run.IntentBrief, int, string) {
	primary := folders[0]
	extra := extraDirsFor(primary, folders)
	c.putBrief(intentLeadID, bus.Brief{
		To:       intentLeadID,
		Role:     "intent-lead",
		Prompt:   intentLeadBriefPrompt(cam),
		Feedback: feedback,
	})
	res := streamOnce(ctx, c, id, intentLeadID, intentLeadBootstrap, primary, extra)
	if ctx.Err() != nil {
		return run.IntentBrief{}, res.tokens, ""
	}
	if res.startErr != nil {
		return run.IntentBrief{}, res.tokens, startFailurePrefix + res.startErr.Error()
	}
	if res.stalled {
		return run.IntentBrief{}, res.tokens, "the intent lead stalled before producing a brief"
	}
	brief, ok := parseIntentBrief(res.allText)
	if !ok {
		return run.IntentBrief{}, res.tokens, "the intent lead produced no INTENT_BRIEF verdict"
	}
	return brief, res.tokens, ""
}

// executeChildQuests launches the campaign's child QUESTS respecting the tech
// manager's dependency DAG: quests with no unmet dependency run CONCURRENTLY (capped
// by campaignConcurrency), and a quest whose deps are declared waits until every
// dependency quest reaches a terminal state. Each child quest carries CampaignID, so
// its child runs commit onto the shared campaign branch and open no PR (QuestBranch →
// campaign/<id>). The campaign ctx halts in-flight quests on pause/stop. A global
// token cap, once exceeded, skips the remaining quests (deliver-partial, never a
// pre-PR pause that strands with no PR). It returns false only when stopped, or when
// nothing landed at all (blocked).
func (c *Conductor) executeChildQuests(ctx context.Context, id string, cam run.Campaign, folders []string, quests []questPartitionItem) bool {
	quests = sanitizeDeps(c, id, quests)
	tokenCap := effectiveTokenCap(cam)
	// One done-channel per quest id, closed when that quest reaches terminal — a
	// dependent selects on its deps' channels before it launches. parseQuests already
	// guarantees non-empty, unique ids; this is a belt-and-suspenders guard so a
	// caller that ever bypasses that validation can't key two goroutines onto the same
	// channel and panic with "close of closed channel" (which would kill the sidecar).
	// The dedup keeps exactly one owner (and one close) per channel.
	doneCh := make(map[string]chan struct{}, len(quests))
	unique := make([]questPartitionItem, 0, len(quests))
	for _, q := range quests {
		if _, exists := doneCh[q.ID]; exists {
			c.appendCampaignNote(id, fmt.Sprintf("duplicate quest id %q in partition — skipping the duplicate to protect the sidecar from a double channel-close", q.ID))
			continue
		}
		doneCh[q.ID] = make(chan struct{})
		unique = append(unique, q)
	}
	quests = unique
	sem := make(chan struct{}, campaignConcurrency())
	var mu sync.Mutex
	completed := 0
	var wg sync.WaitGroup
	for _, q := range quests {
		wg.Add(1)
		go func(q questPartitionItem) {
			defer wg.Done()
			defer close(doneCh[q.ID])
			// Wait for every declared dependency to reach terminal (or the campaign
			// to stop). Unknown dep ids were dropped by sanitizeDeps.
			for _, d := range q.Deps {
				if ch, ok := doneCh[d]; ok {
					select {
					case <-ch:
					case <-ctx.Done():
						return
					}
				}
			}
			if ctx.Err() != nil {
				return
			}
			if tokenCap > 0 {
				if used := c.campaignTokensUsed(id); used >= tokenCap {
					log.Printf("candyland: campaign %s token cap reached (%d/%d) — skipping quest %q", id, used, tokenCap, q.Title)
					c.appendCampaignNote(id, fmt.Sprintf("token cap reached (%d/%d) — skipped child quest %q, delivering partial", used, tokenCap, q.Title))
					return
				}
			}
			// Concurrency gate: hold a slot for the quest's lifetime.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			questID := c.launchCampaignChildQuest(ctx, id, cam, folders, q)
			if cq, ok := c.GetQuest(questID); ok {
				c.addCampaignTokens(id, cq.TokensUsed)
				if questDelivered(cq) {
					mu.Lock()
					completed++
					mu.Unlock()
				}
			}
		}(q)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return false
	}
	if completed == 0 {
		c.blockCampaign(id, "no child quest delivered work onto the campaign branch — nothing to review or deliver")
		return false
	}
	return true
}

// questDelivered reports whether a terminal child quest actually delivered work (so
// the campaign has something to review/deliver). A "surfaced-only" no-op or a quest
// that blocked without completing an item does not count.
func questDelivered(q run.Quest) bool {
	return q.ItemsCompleted > 0
}

// sanitizeDeps drops dependency ids that reference no quest in the partition and, if
// the declared deps form a cycle, clears ALL deps (running everything concurrently)
// with a durable note — a cyclic DAG would otherwise deadlock the dependency waits.
func sanitizeDeps(c *Conductor, id string, quests []questPartitionItem) []questPartitionItem {
	known := make(map[string]bool, len(quests))
	for _, q := range quests {
		known[q.ID] = true
	}
	out := make([]questPartitionItem, len(quests))
	for i, q := range quests {
		var deps []string
		for _, d := range q.Deps {
			if known[d] && d != q.ID {
				deps = append(deps, d)
			}
		}
		q.Deps = deps
		out[i] = q
	}
	if hasCycle(out) {
		if c != nil && id != "" {
			c.appendCampaignNote(id, "the tech manager's quest deps form a cycle — running all quests concurrently")
		}
		for i := range out {
			out[i].Deps = nil
		}
	}
	return out
}

// hasCycle reports whether the quests' dependency edges contain a cycle (Kahn's
// algorithm: if not every node can be topologically removed, a cycle remains).
func hasCycle(quests []questPartitionItem) bool {
	indeg := map[string]int{}
	adj := map[string][]string{}
	for _, q := range quests {
		if _, ok := indeg[q.ID]; !ok {
			indeg[q.ID] = 0
		}
		for _, d := range q.Deps {
			adj[d] = append(adj[d], q.ID)
			indeg[q.ID]++
		}
	}
	var queue []string
	for id, n := range indeg {
		if n == 0 {
			queue = append(queue, id)
		}
	}
	removed := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		removed++
		for _, m := range adj[n] {
			indeg[m]--
			if indeg[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	return removed < len(indeg)
}

// linkCampaignChild creates a child run and links it BOTH WAYS at launch (O3): the
// child is stamped with CampaignID, the campaign branch, and Deliver=branch, AND the
// parent campaign's RunIDs is appended immediately — so the rollup is never empty
// (runIds:[]) while the campaign runs, not only after a child finishes.
func (c *Conductor) linkCampaignChild(id string, spec run.Spec) string {
	childID := c.Create(spec)
	cam, _ := c.GetCampaign(id)
	branch := CampaignBranch(cam)
	c.Update(childID, func(r *run.Run) {
		r.CampaignID = id
		if campaignTargetsPR(cam) {
			// feedback/review campaign: children land on the EXISTING target PR
			// (feedback updates it in place, review reports) instead of committing
			// onto the campaign branch and letting the parent open a PR.
			r.Deliver = cam.Deliver
			r.TargetPR = cam.TargetPR
			return
		}
		r.Branch = branch
		r.Deliver = run.DeliverBranch
	})
	c.UpdateCampaign(id, func(cam *run.Campaign) { cam.RunIDs = append(cam.RunIDs, childID) })
	return childID
}

// campaignTargetsPR reports whether a campaign delivers onto an EXISTING PR
// (feedback/review) rather than its own campaign branch. Its child runs carry the
// campaign's Deliver + TargetPR instead of the default branch delivery.
func campaignTargetsPR(cam run.Campaign) bool {
	return cam.Deliver == run.DeliverFeedback || cam.Deliver == run.DeliverReview
}

// launchCampaignChildQuest creates and drives ONE child QUEST via the existing quest
// machinery (CreateQuest → BeginQuest → the tick loop), stamping CampaignID so the
// quest integrates onto the campaign branch (campaign/<id>) and opens NO PR — the
// campaign opens the per-repo PRs at its delivery gate. A feedback/review campaign
// instead hands the child quest the campaign's Deliver + TargetPR so it works the
// existing PR. Its Title comes from the tech manager's QUESTS emission. It blocks
// until the quest reaches a terminal (non-running) state or the campaign is stopped.
func (c *Conductor) launchCampaignChildQuest(ctx context.Context, id string, cam run.Campaign, folders []string, item questPartitionItem) string {
	spec := run.QuestSpec{
		CampaignID: id,
		Objective:  campaignChildQuestObjective(cam, item),
		Title:      strings.TrimSpace(item.Title),
		Folders:    resolveQuestFolders(item.Folders, folders),
	}
	if campaignTargetsPR(cam) {
		// feedback/review campaign: the child quest works the EXISTING PR (feedback
		// updates it in place, review reports) instead of the campaign branch.
		spec.Deliver = cam.Deliver
		spec.TargetPR = cam.TargetPR
	}
	questID := c.CreateQuest(spec)
	c.UpdateCampaign(id, func(cam *run.Campaign) { cam.QuestIDs = append(cam.QuestIDs, questID) })
	c.BeginQuest(questID)
	for {
		select {
		case <-ctx.Done():
			c.StopQuest(questID, "campaign stopped")
			return questID
		case <-time.After(50 * time.Millisecond):
		}
		q, ok := c.GetQuest(questID)
		if !ok {
			return questID
		}
		if q.Status != "running" {
			return questID // terminal (done/surfaced-only/reviewed) or paused/blocked/stopped
		}
	}
}

// resolveQuestFolders maps the tech manager's per-quest folder tokens onto the
// campaign's actual folders: a token matches a campaign folder when it equals the
// folder path or its basename (so the tech manager can name "web" for
// /path/to/web). With no token (or no match) the quest inherits ALL campaign folders.
func resolveQuestFolders(tokens, campaignFolders []string) []string {
	if len(tokens) == 0 {
		return campaignFolders
	}
	var out []string
	for _, f := range campaignFolders {
		base := f
		if i := strings.LastIndexAny(f, "/\\"); i >= 0 {
			base = f[i+1:]
		}
		for _, t := range tokens {
			t = strings.TrimSpace(t)
			if t != "" && (t == f || t == base) {
				out = append(out, f)
				break
			}
		}
	}
	if len(out) == 0 {
		return campaignFolders
	}
	return out
}

// partitionUntilGated runs the DECOMPOSE stage (the tech manager emits a QUESTS
// partition) and GATE 1 (the intent manager reviews that partition against the
// brief) in a bounded convergence loop: a disagreement weaves the intent manager's
// reason into the next tech-manager spawn (route-back, never a user prompt). It
// returns the agreed partition, or false having blocked the campaign when the bound
// is exhausted or a stage produced nothing.
func (c *Conductor) partitionUntilGated(ctx context.Context, id string, cam run.Campaign, brief run.IntentBrief, folders []string) ([]questPartitionItem, bool) {
	attempts := maxPartitionAttempts()
	feedback := ""
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctx.Err() != nil {
			return nil, false
		}
		c.setCampaignRunning(id)
		quests, tokens, errMsg, retryFeedback := c.emitQuests(ctx, id, cam, brief, folders, feedback)
		c.addCampaignTokens(id, tokens)
		if ctx.Err() != nil {
			return nil, false
		}
		if errMsg != "" {
			c.blockCampaign(id, "tech manager: "+errMsg)
			return nil, false
		}
		if retryFeedback != "" {
			feedback = retryFeedback
			continue
		}
		if len(quests) == 0 {
			feedback = "you emitted no child quests — a campaign must decompose into at least one quest that delivers the brief's commitments"
			continue
		}
		verdict, ok := c.reviewPartition(ctx, id, cam, brief, quests, folders)
		if ctx.Err() != nil {
			return nil, false
		}
		if !ok {
			return nil, false // the intent manager produced no verdict (blocked) — recorded
		}
		reason := orDefault(strings.TrimSpace(verdict.Reason), fmt.Sprintf("%d quest(s) reviewed against %d commitment(s)", len(quests), len(brief.Commitments)))
		c.recordPartitionGate(id, verdict.Agree, reason)
		if verdict.Agree {
			return quests, true
		}
		feedback = reason
	}
	c.blockCampaign(id, fmt.Sprintf("the quest partition failed gate 1 (intent-manager convergence) after %d attempts", attempts))
	return nil, false
}

// emitQuests spawns the tech manager for one attempt and parses its QUESTS line. The
// tech manager runs in the campaign's primary folder with the rest as --add-dir
// context; its PROMPT tells it to load roles/tech-lead via kb_get and APPLY it (no
// inlined rubric). feedback (non-empty on a gate-1 route-back) rides the bus brief so
// the tech manager corrects the prior partition — never on argv.
//
// A parsed-but-invalid partition (empty or duplicate quest id) is not a hard error: it
// is surfaced as retryFeedback so partitionUntilGated routes it back to the tech
// manager for a clean re-emission, bounded by maxPartitionAttempts.
func (c *Conductor) emitQuests(ctx context.Context, id string, cam run.Campaign, brief run.IntentBrief, folders []string, feedback string) (quests []questPartitionItem, tokens int, errMsg, retryFeedback string) {
	primary := folders[0]
	extra := extraDirsFor(primary, folders)
	c.putBrief(techManagerID, bus.Brief{
		To:       techManagerID,
		Role:     "tech-manager",
		Prompt:   techManagerBriefPrompt(cam, brief),
		Feedback: feedback,
	})
	res := streamOnce(ctx, c, id, techManagerID, techManagerBootstrap, primary, extra)
	if ctx.Err() != nil {
		return nil, res.tokens, "", ""
	}
	if res.startErr != nil {
		return nil, res.tokens, startFailurePrefix + res.startErr.Error(), ""
	}
	if res.stalled {
		return nil, res.tokens, "the tech manager stalled before producing a quest partition", ""
	}
	parsed, ok, reason := parseQuests(res.allText)
	if !ok {
		if reason != "" {
			// A QUESTS line was present but its ids violate the uniqueness invariant —
			// recoverable: route back to the tech manager for a clean re-emission.
			return nil, res.tokens, "", "your quest partition is unusable: " + reason
		}
		return nil, res.tokens, "the tech manager produced no QUESTS partition", ""
	}
	return parsed, res.tokens, "", ""
}

// reviewPartition spawns the intent manager (gate 1) and parses its PARTITION_REVIEW
// verdict. The intent manager runs in the campaign's primary folder; its PROMPT names
// the brief's commitments and the tech manager's proposed quests, and asks whether
// they would plausibly deliver the intent — {agree, reason}. A missing verdict blocks.
func (c *Conductor) reviewPartition(ctx context.Context, id string, cam run.Campaign, brief run.IntentBrief, quests []questPartitionItem, folders []string) (partitionVerdict, bool) {
	primary := folders[0]
	extra := extraDirsFor(primary, folders)
	c.putBrief(intentManagerID, bus.Brief{
		To:     intentManagerID,
		Role:   "intent-manager",
		Prompt: partitionReviewBriefPrompt(cam, brief, quests),
	})
	res := streamOnce(ctx, c, id, intentManagerID, partitionReviewBootstrap, primary, extra)
	if ctx.Err() != nil {
		return partitionVerdict{}, false
	}
	if res.startErr != nil {
		c.blockCampaign(id, "gate 1 partition review: "+startFailurePrefix+res.startErr.Error())
		return partitionVerdict{}, false
	}
	if res.stalled {
		c.blockCampaign(id, "the intent manager stalled before reviewing the quest partition")
		return partitionVerdict{}, false
	}
	verdict, ok := parsePartitionVerdict(res.allText)
	if !ok {
		c.blockCampaign(id, "the intent manager produced no PARTITION_REVIEW verdict — refusing to launch un-reviewed work")
		return partitionVerdict{}, false
	}
	return verdict, true
}

// techManagerDone spawns the tech manager at gate 2 to confirm technical done (all
// child quests integrated green on the campaign branch, review-loop clean). Its
// PROMPT names the branch diff command and the intent review's verdicts so it judges
// the integrated state — {done, reason}. A missing verdict blocks (never a silent pass).
func (c *Conductor) techManagerDone(ctx context.Context, id string, cam run.Campaign, brief run.IntentBrief, review run.IntentReview, folders []string) (techDoneVerdict, bool) {
	primary := folders[0]
	extra := extraDirsFor(primary, folders)
	c.setCampaignRunning(id)
	base, _ := currentBranch(ctx, primary)
	c.putBrief(techManagerID, bus.Brief{
		To:     techManagerID,
		Role:   "tech-manager",
		Prompt: techDoneBriefPrompt(cam, brief, review, orDefault(base, "main")),
	})
	res := streamOnce(ctx, c, id, techManagerID, techDoneBootstrap, primary, extra)
	c.addCampaignTokens(id, res.tokens)
	if ctx.Err() != nil {
		return techDoneVerdict{}, false
	}
	if res.startErr != nil {
		c.blockCampaign(id, "gate 2 technical sign-off: "+startFailurePrefix+res.startErr.Error())
		return techDoneVerdict{}, false
	}
	if res.stalled {
		c.blockCampaign(id, "the tech manager stalled before confirming technical done")
		return techDoneVerdict{}, false
	}
	verdict, ok := parseTechDone(res.allText)
	if !ok {
		c.blockCampaign(id, "the tech manager produced no TECH_DONE verdict — refusing to deliver un-confirmed work")
		return techDoneVerdict{}, false
	}
	return verdict, true
}

// setCampaignRunning flips a campaign back to running (clearing a transient pause
// reason) unless a concurrent Stop/completion already made it terminal.
func (c *Conductor) setCampaignRunning(id string) {
	c.UpdateCampaign(id, func(cam *run.Campaign) {
		if cam.Status == "stopped" || cam.Status == "done" {
			return
		}
		cam.Status = "running"
		cam.PauseReason = ""
	})
}

// intentReview spawns the intent reviewer (Stage 6) and parses its per-commitment
// verdicts. The reviewer runs in the campaign's primary folder against the campaign
// branch diff; its PROMPT instructs it to load core/intent-review via kb_get and
// APPLY it — emitting {satisfied|partial|missed} per commitment with cited evidence.
func (c *Conductor) intentReview(ctx context.Context, id string, cam run.Campaign, brief run.IntentBrief, folders []string) (run.IntentReview, bool) {
	primary := folders[0]
	extra := extraDirsFor(primary, folders)
	c.UpdateCampaign(id, func(cam *run.Campaign) {
		if cam.Status == "stopped" || cam.Status == "done" {
			return // a concurrent Stop/completion is authoritative — don't resurrect
		}
		cam.Status = "running"
		cam.PauseReason = ""
	})
	base, _ := currentBranch(ctx, primary)
	c.putBrief(intentReviewerID, bus.Brief{
		To:     intentReviewerID,
		Role:   "intent-reviewer",
		Prompt: intentReviewerBriefPrompt(cam, brief, orDefault(base, "main")),
	})
	res := streamOnce(ctx, c, id, intentReviewerID, intentReviewerBootstrap, primary, extra)
	c.addCampaignTokens(id, res.tokens)
	if ctx.Err() != nil {
		return run.IntentReview{}, false
	}
	if res.startErr != nil {
		c.blockCampaign(id, "intent review: "+startFailurePrefix+res.startErr.Error())
		return run.IntentReview{}, false
	}
	if res.stalled {
		c.blockCampaign(id, "the intent reviewer stalled before producing a verdict")
		return run.IntentReview{}, false
	}
	review, ok := parseIntentReview(res.allText)
	if !ok {
		c.blockCampaign(id, "the intent reviewer produced no INTENT_REVIEW verdict — refusing to deliver un-reviewed work")
		return run.IntentReview{}, false
	}
	review.ReviewedAt = time.Now().UTC().Format(time.RFC3339)
	c.UpdateCampaign(id, func(cam *run.Campaign) { cam.IntentReview = review })
	return review, true
}

// deliverCampaign is the DELIVERY GATE (Stage 7). A `missed` verdict BLOCKS that
// repo's PR — the campaign stays blocked with a visible reason and the branch
// persists for resume; no PR opens. A `partial` annotates the PR body but does NOT
// block. With no `missed`, it opens ONE PR PER IMPACTED REPO from the campaign
// branch (reusing the run push+openPR machinery, partial-failure isolation per repo).
func (c *Conductor) deliverCampaign(ctx context.Context, id string, folders []string, brief run.IntentBrief, review run.IntentReview) {
	missed := missedCommitments(brief, review)
	if len(missed) > 0 {
		reason := fmt.Sprintf("intent review BLOCKS delivery: %d commitment(s) missed — %s. The campaign branch persists; resolve and resume.", len(missed), strings.Join(missed, "; "))
		c.blockCampaign(id, reason)
		return
	}

	cam, ok := c.GetCampaign(id)
	if !ok {
		return
	}
	annotations := partialAnnotations(brief, review)
	branch := CampaignBranch(cam)
	title := campaignPRTitle(cam)
	body := campaignPRBody(cam, brief, annotations)

	prs := openBranchPRs(ctx, folders, branch, title, body)

	opened := 0
	for _, pr := range prs {
		if pr.URL != "" {
			opened++
		}
	}
	c.UpdateCampaign(id, func(cam *run.Campaign) {
		if cam.Status == "stopped" || cam.Status == "done" {
			return // a concurrent Stop/completion is authoritative
		}
		cam.PRs = prs
		if opened == 0 {
			cam.Status = "blocked"
			cam.PauseReason = "no pull request could be opened from the campaign branch: " + firstPRErr(prs) +
				" Check each repo has an 'origin' remote you can push to and that gh is authenticated. The branch persists; resume to retry."
			return
		}
		cam.Status = "done"
		cam.PauseReason = ""
	})
}

// --- gates (deterministic checks; doctrine lives in the agent prompts) ---

// briefGate is the BRIEF GATE: a deterministic consistency check that the brief
// reflects the OriginalInput before planning proceeds. The brief must restate a
// goal, commit to at least one checkable assertion, and the restated goal must
// share meaningful terms with the original input (so a brief about a different
// thing is caught). It returns the reason and whether it passed. A failed gate
// routes back to the intent lead — it never asks the user.
func briefGate(originalInput string, brief run.IntentBrief) (string, bool) {
	if strings.TrimSpace(brief.RestatedGoal) == "" {
		return "the brief restated no goal", false
	}
	if len(brief.Commitments) == 0 {
		return "the brief committed to no checkable assertions", false
	}
	for _, cm := range brief.Commitments {
		if strings.TrimSpace(cm.Statement) == "" {
			return "a commitment has no statement", false
		}
	}
	if !sharesTerms(originalInput, brief.RestatedGoal) {
		return "the restated goal does not reflect the original input (no shared terms)", false
	}
	return "the brief restates the original input with checkable commitments", true
}

// sharesTerms reports whether two strings share at least one meaningful term (a
// lowercased word of 4+ chars). It is the cheap consistency signal the brief gate
// uses to catch a brief that drifted off the original input.
func sharesTerms(a, b string) bool {
	terms := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(a), notWord) {
		if len(w) >= 4 {
			terms[w] = true
		}
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(b), notWord) {
		if len(w) >= 4 && terms[w] {
			return true
		}
	}
	return false
}

func notWord(r rune) bool {
	return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
}

// missedCommitments returns a human-readable reason per commitment the intent review
// judged `missed` (the verdicts that BLOCK delivery). It joins the commitment
// statement with the verdict's evidence so the block reason is actionable.
func missedCommitments(brief run.IntentBrief, review run.IntentReview) []string {
	byID := commitmentByID(brief)
	var out []string
	for _, v := range review.Verdicts {
		if strings.EqualFold(strings.TrimSpace(v.Verdict), "missed") {
			stmt := byID[v.CommitmentID]
			ev := strings.Join(v.Evidence, "; ")
			out = append(out, strings.TrimSpace(orDefault(stmt, v.CommitmentID)+" ("+ev+")"))
		}
	}
	return out
}

// partialAnnotations returns a human-readable note per `partial` commitment (which
// annotate the PR but do NOT block delivery).
func partialAnnotations(brief run.IntentBrief, review run.IntentReview) []string {
	byID := commitmentByID(brief)
	var out []string
	for _, v := range review.Verdicts {
		if strings.EqualFold(strings.TrimSpace(v.Verdict), "partial") {
			stmt := byID[v.CommitmentID]
			ev := strings.Join(v.Evidence, "; ")
			out = append(out, strings.TrimSpace(orDefault(stmt, v.CommitmentID)+" — "+ev))
		}
	}
	return out
}

// gapSummary is the human-readable description of what gate 2 still finds unmet: the
// missed commitments plus a not-done technical verdict (if any). It drives the
// remediation note and the eventual hard-block reason.
func gapSummary(missed []string, techDone techDoneVerdict) string {
	var parts []string
	if len(missed) > 0 {
		parts = append(parts, fmt.Sprintf("%d missed commitment(s): %s", len(missed), strings.Join(missed, "; ")))
	}
	if !techDone.Done {
		parts = append(parts, "technical sign-off withheld: "+orDefault(strings.TrimSpace(techDone.Reason), "the tech manager reports the campaign branch is not integrated/clean"))
	}
	if len(parts) == 0 {
		return "no specific gap"
	}
	return strings.Join(parts, " — ")
}

// remediationQuests builds one targeted remediation QUEST per commitment the intent
// review judged `missed` or `partial` — the work the campaign must still finish
// before it can deliver. Each quest names the specific commitment and quotes the
// reviewer's evidence, so the quest's runs close that exact gap on the campaign
// branch (Deliver=branch, campaign-child) rather than re-running the whole
// decomposition. When the tech manager withheld technical sign-off with a reason,
// one more remediation quest targets that technical gap. Missed commitments come
// first (they block); partials follow (deliver-blocking only if they regress to missed).
func remediationQuests(cam run.Campaign, brief run.IntentBrief, review run.IntentReview, techDone techDoneVerdict) []questPartitionItem {
	byID := commitmentByID(brief)
	var missed, partial []questPartitionItem
	n := 0
	for _, v := range review.Verdicts {
		verdict := strings.ToLower(strings.TrimSpace(v.Verdict))
		if verdict != "missed" && verdict != "partial" {
			continue
		}
		stmt := strings.TrimSpace(orDefault(byID[v.CommitmentID], v.CommitmentID))
		if stmt == "" {
			continue
		}
		n++
		q := questPartitionItem{
			ID:        fmt.Sprintf("r%d", n),
			Title:     truncate("remediate: "+stmt, 72),
			Objective: remediationChildPrompt(cam, brief, stmt, verdict, v.Evidence),
		}
		if verdict == "missed" {
			missed = append(missed, q)
		} else {
			partial = append(partial, q)
		}
	}
	out := append(missed, partial...)
	if !techDone.Done {
		n++
		out = append(out, questPartitionItem{
			ID:        fmt.Sprintf("r%d", n),
			Title:     truncate("remediate: technical sign-off", 72),
			Objective: techRemediationObjective(cam, brief, techDone),
		})
	}
	return out
}

// techRemediationObjective frames the tech manager's withheld sign-off as a
// remediation quest objective (integrate/clean the campaign branch to green).
func techRemediationObjective(cam run.Campaign, brief run.IntentBrief, techDone techDoneVerdict) string {
	var b strings.Builder
	b.WriteString("REMEDIATE the campaign's technical integration so the tech manager can sign off.\n\n")
	if r := strings.TrimSpace(techDone.Reason); r != "" {
		fmt.Fprintf(&b, "WHAT IS STILL WRONG (tech manager): %s\n", r)
	}
	fmt.Fprintf(&b, "\nThis is remediation work for the campaign goal: %s\n", brief.RestatedGoal)
	if len(brief.ScopeByDomain) > 0 {
		fmt.Fprintf(&b, "Stay in scope: %s\n", strings.Join(brief.ScopeByDomain, "; "))
	}
	b.WriteString("Deliver only the work needed to make the campaign branch integrate green with the review-loop clean.\n")
	return b.String()
}

// remediationChildPrompt frames a single unmet commitment as a child run: it states
// the commitment, the reviewer's verdict + cited evidence for what is still missing,
// and the campaign scope, so the child delivers exactly that gap on the campaign
// branch. It reuses the campaign framing so the child inherits the same bounds.
func remediationChildPrompt(cam run.Campaign, brief run.IntentBrief, stmt, verdict string, evidence []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REMEDIATE an unmet campaign commitment. The final intent review judged this commitment %q — deliver the missing work so it becomes fully satisfied.\n\n", verdict)
	fmt.Fprintf(&b, "COMMITMENT: %s\n", stmt)
	if ev := strings.TrimSpace(strings.Join(evidence, "; ")); ev != "" {
		fmt.Fprintf(&b, "WHAT IS STILL MISSING (reviewer evidence): %s\n", ev)
	}
	fmt.Fprintf(&b, "\nThis is remediation work for the campaign goal: %s\n", brief.RestatedGoal)
	if len(brief.ScopeByDomain) > 0 {
		fmt.Fprintf(&b, "Stay in scope: %s\n", strings.Join(brief.ScopeByDomain, "; "))
	}
	fmt.Fprintf(&b, "Deliver only the work needed to satisfy this commitment; commit it onto the campaign branch. Do not re-do already-satisfied commitments.\n")
	return b.String()
}

func commitmentByID(brief run.IntentBrief) map[string]string {
	m := make(map[string]string, len(brief.Commitments))
	for _, cm := range brief.Commitments {
		m[cm.ID] = cm.Statement
	}
	return m
}

// --- decomposition: the tech manager's QUESTS partition ---

// questPartitionItem is one child quest the tech manager emits on a `QUESTS <json>`
// line (the campaign-altitude analogue of the run tech lead's PARTITION task). It is
// a parsed-from-stdout convention, not a stored type: {id,title,objective,folders,
// deps}. deps names the ids this quest must wait for (empty → concurrent).
type questPartitionItem struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Objective string   `json:"objective"`
	Folders   []string `json:"folders"`
	Deps      []string `json:"deps"`
}

// partitionVerdict is the intent manager's gate-1 verdict on the QUESTS partition,
// emitted on a `PARTITION_REVIEW <json>` line: {agree, reason}. agree=false routes
// back to the tech manager (bounded by maxPartitionAttempts).
type partitionVerdict struct {
	Agree  bool   `json:"agree"`
	Reason string `json:"reason"`
}

// techDoneVerdict is the tech manager's gate-2 technical sign-off, emitted on a
// `TECH_DONE <json>` line: {done, reason}. done=false feeds a remediation quest
// (bounded by maxRemediationRounds).
type techDoneVerdict struct {
	Done   bool   `json:"done"`
	Reason string `json:"reason"`
}

// parseQuests extracts the tech manager's child-quest partition from a `QUESTS <json>`
// line (mirroring parsePartition). ok is false when no such line is present (no
// verdict — a failure, never a silent pass). The last QUESTS line wins.
//
// A parsed partition is only returned with ok=true once its ids satisfy the
// doneCh-keying invariant: every quest id is non-empty and unique (see
// validateQuestIDs). When a QUESTS line is present but violates that invariant, ok is
// false and reason is a non-empty explanation — the caller routes it back to the tech
// manager (the gate-1 loop) to re-request a clean partition rather than launching work
// that would panic the sidecar on a double channel-close.
func parseQuests(text string) (quests []questPartitionItem, ok bool, reason string) {
	found := false
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "QUESTS ") {
			continue
		}
		var parsed []questPartitionItem
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "QUESTS ")), &parsed) == nil {
			quests, found = parsed, true
		}
	}
	if !found {
		return nil, false, ""
	}
	if bad := validateQuestIDs(quests); bad != "" {
		return nil, false, bad
	}
	return quests, true, ""
}

// validateQuestIDs enforces the invariant executeChildQuests relies on to build its
// per-quest done-channels: every quest carries a non-empty, unique id. Two quests with
// the same id (or two with an empty id) would key two goroutines onto the same
// doneCh[q.ID] channel, and each closes it on exit — a `close of closed channel` panic
// in a bare goroutine with no recover, which would tear down the whole conductor
// sidecar and every other campaign/quest/run in it. Returns "" when valid, otherwise a
// human-readable reason (routed back to the tech manager as gate-1 feedback).
func validateQuestIDs(quests []questPartitionItem) string {
	seen := make(map[string]bool, len(quests))
	for i, q := range quests {
		if strings.TrimSpace(q.ID) == "" {
			return fmt.Sprintf("quest #%d (%q) has an empty id — every quest must carry a unique, non-empty id", i+1, q.Title)
		}
		if seen[q.ID] {
			return fmt.Sprintf("quest id %q is used by more than one quest — every quest must carry a unique id", q.ID)
		}
		seen[q.ID] = true
	}
	return ""
}

// parsePartitionVerdict extracts the intent manager's gate-1 verdict from a
// `PARTITION_REVIEW <json>` line. ok is false when no such line is present. Last wins.
func parsePartitionVerdict(text string) (partitionVerdict, bool) {
	var v partitionVerdict
	ok := false
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "PARTITION_REVIEW ") {
			continue
		}
		var parsed partitionVerdict
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "PARTITION_REVIEW ")), &parsed) == nil {
			v, ok = parsed, true
		}
	}
	return v, ok
}

// parseTechDone extracts the tech manager's gate-2 technical sign-off from a
// `TECH_DONE <json>` line. ok is false when no such line is present. Last wins.
func parseTechDone(text string) (techDoneVerdict, bool) {
	var v techDoneVerdict
	ok := false
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "TECH_DONE ") {
			continue
		}
		var parsed techDoneVerdict
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "TECH_DONE ")), &parsed) == nil {
			v, ok = parsed, true
		}
	}
	return v, ok
}

// campaignChildQuestObjective frames one QUESTS item as a child-quest objective,
// inheriting the campaign goal / scope / commitments so the quest stays in-bounds.
// The quest's runs commit onto the campaign branch and open no PR.
func campaignChildQuestObjective(cam run.Campaign, item questPartitionItem) string {
	var b strings.Builder
	brief := cam.IntentBrief
	fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(orDefault(item.Objective, item.Title)))
	if g := strings.TrimSpace(brief.RestatedGoal); g != "" {
		fmt.Fprintf(&b, "This is one quest of the campaign goal: %s\n", g)
	}
	if len(brief.ScopeByDomain) > 0 {
		fmt.Fprintf(&b, "Stay in scope: %s\n", strings.Join(brief.ScopeByDomain, "; "))
	}
	if len(brief.Commitments) > 0 {
		stmts := make([]string, 0, len(brief.Commitments))
		for _, cm := range brief.Commitments {
			stmts = append(stmts, cm.Statement)
		}
		fmt.Fprintf(&b, "The campaign commits to: %s\n", strings.Join(stmts, "; "))
	}
	return b.String()
}

// --- recorders (single durable updates) ---

func (c *Conductor) recordBriefGate(id string, passed bool, reason string) {
	c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.BriefGate = run.GateResult{Passed: passed, Reason: reason, DecidedAt: time.Now().UTC().Format(time.RFC3339)}
	})
}

// recordPartitionGate records gate 1 (the intent-manager partition-convergence gate)
// on the campaign's PlanGate field — the agentic gate 1 replaces the former
// deterministic plan gate but keeps the same durable slot the UI renders.
func (c *Conductor) recordPartitionGate(id string, passed bool, reason string) bool {
	return c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.PlanGate = run.GateResult{Passed: passed, Reason: reason, DecidedAt: time.Now().UTC().Format(time.RFC3339)}
	})
}

// blockCampaign records a hard blocker with a visible reason. A blocked campaign is
// not terminal — its branch persists and BeginCampaign restarts the supervisor; it
// never asks the user and never abandons the work (handle/escalate, not abandon).
func (c *Conductor) blockCampaign(id, reason string) {
	log.Printf("candyland: campaign %s blocked: %s", id, reason)
	c.UpdateCampaign(id, func(cam *run.Campaign) {
		if cam.Status == "stopped" || cam.Status == "done" {
			return // a concurrent Stop/completion is authoritative
		}
		cam.Status = "blocked"
		cam.PauseReason = reason
	})
}

// appendCampaignNote appends a DURABLE non-blocking note to the campaign (e.g. a
// token-cap degrade-to-partial), without changing status. Unlike PauseReason — the
// transient pause/block reason that clean delivery clears and block overwrites —
// Notes survives delivery, so an operator still learns the campaign delivered
// partial after a clean PR. It renders in the campaign trace/UI (a campaign field).
func (c *Conductor) appendCampaignNote(id, note string) {
	c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.Notes = append(cam.Notes, note)
	})
}

func (c *Conductor) addCampaignTokens(id string, tokens int) {
	if tokens == 0 {
		return
	}
	c.UpdateCampaign(id, func(cam *run.Campaign) { cam.TokensUsed += tokens })
}

func (c *Conductor) campaignTokensUsed(id string) int {
	cam, ok := c.GetCampaign(id)
	if !ok {
		return 0
	}
	return cam.TokensUsed
}

// effectiveTokenCap is the campaign's global token cap: its spec TokenBudget when
// set, else the CANDYLAND_CAMPAIGN_TOKEN_CAP env cap, else 0 (uncapped).
func effectiveTokenCap(cam run.Campaign) int {
	if cam.TokenBudget > 0 {
		return cam.TokenBudget
	}
	return campaignTokenCap()
}

// campaignFolders resolves a campaign's working folders, expanding ~ (mirrors the
// quest folder handling).
func campaignFolders(cam run.Campaign) []string {
	out := make([]string, 0, len(cam.Folders))
	for _, f := range cam.Folders {
		out = append(out, expandHome(f))
	}
	return out
}

// --- PR text ---

func campaignPRTitle(cam run.Campaign) string {
	if t := strings.TrimSpace(cam.IntentBrief.RestatedGoal); t != "" {
		return truncate(t, 72)
	}
	return truncate(orDefault(strings.SplitN(cam.OriginalInput, "\n", 2)[0], "candyland campaign "+cam.ID), 72)
}

func campaignPRBody(cam run.Campaign, brief run.IntentBrief, partial []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Delivered by a candyland campaign (intent→delivery).\n\n## Original request\n\n%s\n", strings.TrimSpace(cam.OriginalInput))
	if brief.RestatedGoal != "" {
		fmt.Fprintf(&b, "\n## Goal\n\n%s\n", brief.RestatedGoal)
	}
	if len(brief.Commitments) > 0 {
		b.WriteString("\n## Commitments\n\n")
		for _, cm := range brief.Commitments {
			fmt.Fprintf(&b, "- %s\n", cm.Statement)
		}
	}
	if len(partial) > 0 {
		// `partial` verdicts annotate the PR (they do NOT block delivery).
		b.WriteString("\n## ⚠️ Partially satisfied commitments (intent review)\n\n")
		for _, p := range partial {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	if len(cam.Notes) > 0 {
		// Durable supervisor notes (e.g. a token-cap degrade-to-partial) — surfaced
		// so the operator learns about a degraded delivery on the PR itself.
		b.WriteString("\n## ⚠️ Delivery notes\n\n")
		for _, n := range cam.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	b.WriteString("\n🍬 Opened by [candyland](https://github.com/benitogf/candyland).")
	return b.String()
}

// --- parsing (the fenced agent-verdict conventions) ---

// parseIntentBrief extracts the intent lead's brief from an `INTENT_BRIEF <json>`
// line (the fenced convention, like PARTITION/WORKITEMS). ok is false when no such
// line is present (no verdict — a failure, never a silent pass). The last line wins.
func parseIntentBrief(text string) (run.IntentBrief, bool) {
	var brief run.IntentBrief
	ok := false
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "INTENT_BRIEF ") {
			continue
		}
		var b run.IntentBrief
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "INTENT_BRIEF ")), &b) == nil {
			brief, ok = b, true
		}
	}
	return brief, ok
}

// parseIntentReview extracts the intent reviewer's per-commitment verdicts from an
// `INTENT_REVIEW <json>` line carrying {"verdicts":[{commitmentId,verdict,evidence}]}.
// ok is false when no such line is present (no verdict — a failure). The last wins.
func parseIntentReview(text string) (run.IntentReview, bool) {
	var review run.IntentReview
	ok := false
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "INTENT_REVIEW ") {
			continue
		}
		var r run.IntentReview
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "INTENT_REVIEW ")), &r) == nil {
			review, ok = r, true
		}
	}
	return review, ok
}

// --- prompts (composition, not inlined rubrics) ---

// intentLeadBootstrap is the CONSTANT brief prompt. Like the tech-lead/coder/quest
// bootstraps it carries NO campaign context on argv (that rides the brief via
// brief_get); it tells the intent lead to load core/planning + core/dream via kb_get
// and APPLY them, then emit a structured INTENT_BRIEF verdict. It must NOT inline a
// rubric — the doctrine is the rubric (the Composition Constraint).
const intentLeadBootstrap = "You are the intent lead opening a campaign — the program-level intake that turns an immutable original request into a structured, checkable plan. " +
	"Call the brief_get tool FIRST to read the campaign's ORIGINAL INPUT and any prior-attempt feedback — it is no longer on your command line. " +
	"Load and APPLY the detritus doctrine via the kb_get tool: kb_get name=\"core/planning\" (what a settled plan is and the .plan contract) and kb_get name=\"core/dream\" (executive intake: own the technical decisions, never ask the stakeholder). " +
	"Do NOT improvise your own rubric — use the doctrine you loaded, and decide every technical question yourself (this is a launched campaign; it never asks the user). " +
	"Restate the goal, split the scope by domain, derive the CHECKABLE COMMITMENTS (each a single assertion intent review can later judge satisfied/partial/missed), draft the task list, list dependencies, and suggest human review-routing. " +
	"Then emit EXACTLY ONE verdict line and stop: `INTENT_BRIEF ` followed by a JSON object " +
	`{"restatedGoal":"…","scopeByDomain":["…"],"resolvedQuestions":["…"],"openQuestions":["…"],"draftTasks":["…"],"dependencies":["…"],"roughSizing":"…","reviewRouting":["…"],"commitments":[{"id":"c1","statement":"one checkable assertion"}]}` +
	". Do not ask questions and do not defer."

// intentLeadBriefPrompt is the per-campaign context the intent lead reads via
// brief_get: the IMMUTABLE original input it must restate (never rewritten).
func intentLeadBriefPrompt(cam run.Campaign) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CAMPAIGN ORIGINAL INPUT (immutable — restate this, never substitute your own goal):\n%s\n", cam.OriginalInput)
	if len(cam.Folders) > 0 {
		fmt.Fprintf(&b, "TARGET FOLDERS/REPOS: %s\n", strings.Join(cam.Folders, ", "))
	}
	b.WriteString("AUTONOMY: a launched campaign — decide and escalate within the hierarchy; never ask the user.\n")
	return b.String()
}

// intentReviewerBootstrap is the CONSTANT final-review prompt. It composes the
// final-review method via kb_get (core/intent-review) — NOT an inlined rubric — and
// emits a per-commitment verdict {satisfied|partial|missed} with cited evidence.
const intentReviewerBootstrap = "You are the intent reviewer closing a campaign: judge whether the delivered work satisfies what the campaign COMMITTED to, per commitment, against the ORIGINAL INPUT — not just whether tasks ran. " +
	"Call the brief_get tool FIRST to read the original input, the commitments to judge, and the diff command for the campaign branch. " +
	"Load and APPLY the detritus final-review method via the kb_get tool: kb_get name=\"core/intent-review\" (the per-commitment verdict method). If that document is unavailable, fall back to kb_get name=\"core/completion\" (the definition of done) and kb_get name=\"core/review-rigor\"; APPLY the doctrine, do NOT improvise your own rubric. " +
	"Inspect the delivered work (run the diff command in the brief, read the changed files) and judge EACH commitment: satisfied (fully delivered with evidence), partial (some but not all), or missed (not delivered). Cite concrete evidence for every verdict. " +
	"Then emit EXACTLY ONE verdict line and stop: `INTENT_REVIEW ` followed by JSON " +
	`{"verdicts":[{"commitmentId":"c1","verdict":"satisfied|partial|missed","evidence":["file:line or fact backing the verdict"]}]}` +
	". Judge every commitment; do not ask questions and do not defer."

// intentReviewerBriefPrompt is the per-campaign context the reviewer reads via
// brief_get: the original input, the commitments to judge, and the campaign-branch
// diff command so it inspects the delivered work.
func intentReviewerBriefPrompt(cam run.Campaign, brief run.IntentBrief, base string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CAMPAIGN ORIGINAL INPUT (judge against this):\n%s\n\n", cam.OriginalInput)
	b.WriteString("COMMITMENTS TO JUDGE (one verdict each):\n")
	for _, cm := range brief.Commitments {
		fmt.Fprintf(&b, "- [%s] %s\n", cm.ID, cm.Statement)
	}
	branch := CampaignBranch(cam)
	fmt.Fprintf(&b, "\nThe delivered work is on the campaign branch %q. Review it with: git diff %s..%s\n", branch, base, branch)
	return b.String()
}

// --- tech manager + intent manager prompts (composition, not inlined rubrics) ---

// techManagerBootstrap is the CONSTANT decompose prompt for the tech manager. Like
// the intent-lead/tech-lead bootstraps it carries NO campaign context on argv (that
// rides the brief via brief_get); it tells the tech manager to load roles/tech-lead
// via kb_get and APPLY its program-altitude partition rules, then emit ONE machine-
// readable QUESTS line. It must NOT inline a rubric — the doctrine is the rubric.
const techManagerBootstrap = "You are the tech manager opening a campaign: you own everything technical — how the Intent Brief is partitioned into concurrent child QUESTS, integration across the shared branch, and remediation targeting. " +
	"Call the brief_get tool FIRST to read the campaign's Intent Brief (goal, scope-by-domain, commitments, draft tasks) and any prior-attempt feedback — it is no longer on your command line. " +
	"Load and APPLY the detritus doctrine via the kb_get tool: kb_get name=\"roles/tech-lead\" (its program-altitude partition rules for a campaign). Do NOT improvise your own rubric — use the doctrine you loaded, and decide every technical question yourself (this is a launched campaign; it never asks the user). " +
	"Partition the brief into the SMALLEST set of child quests that together deliver its commitments. Each quest is a bounded objective a quest-lead can drive to completion on the shared campaign branch. Make quests CONCURRENT by default; declare a dependency ONLY where one quest genuinely must finish before another can start. " +
	"Then emit EXACTLY ONE line and stop: `QUESTS ` followed by a JSON array " +
	`[{"id":"q1","title":"short label","objective":"what this quest delivers","folders":["optional repo subset"],"deps":["ids this quest waits for"]}]` +
	". The supervisor stamps branch delivery — you only decide the partition. Do not ask questions and do not defer."

// techManagerBriefPrompt is the per-campaign context the tech manager reads via
// brief_get for the decompose stage: the settled brief to partition into quests.
func techManagerBriefPrompt(cam run.Campaign, brief run.IntentBrief) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CAMPAIGN GOAL (settled Intent Brief): %s\n", brief.RestatedGoal)
	if len(brief.ScopeByDomain) > 0 {
		fmt.Fprintf(&b, "SCOPE BY DOMAIN: %s\n", strings.Join(brief.ScopeByDomain, "; "))
	}
	if len(brief.Commitments) > 0 {
		b.WriteString("COMMITMENTS the quests must together deliver:\n")
		for _, cm := range brief.Commitments {
			fmt.Fprintf(&b, "- [%s] %s\n", cm.ID, cm.Statement)
		}
	}
	if len(brief.DraftTasks) > 0 {
		fmt.Fprintf(&b, "DRAFT TASKS (the brief's first cut — regroup into quests as you see fit): %s\n", strings.Join(brief.DraftTasks, "; "))
	}
	if len(cam.Folders) > 0 {
		fmt.Fprintf(&b, "TARGET FOLDERS/REPOS (name a subset per quest via its folders, or leave empty for all): %s\n", strings.Join(cam.Folders, ", "))
	}
	b.WriteString("Partition into concurrent child quests; add deps only for real ordering. Emit one QUESTS line.\n")
	return b.String()
}

// partitionReviewBootstrap is the CONSTANT gate-1 prompt for the intent manager: it
// judges whether the tech manager's quest partition would plausibly deliver the
// brief's commitments, and emits a single PARTITION_REVIEW verdict. It composes the
// intent method via kb_get — never an inlined rubric.
const partitionReviewBootstrap = "You are the intent manager at gate 1 of a campaign: judge whether the tech manager's proposed child-quest partition would plausibly deliver the Intent Brief's commitments BEFORE any work launches. This is a partition-convergence gate between two managers, not the final review. " +
	"Call the brief_get tool FIRST to read the brief's commitments and the tech manager's proposed quests. " +
	"Load and APPLY the detritus doctrine via the kb_get tool: kb_get name=\"core/planning\" (what a settled plan must cover). Do NOT improvise your own rubric. " +
	"Judge coverage: does every commitment map to at least one quest, is the scope right, are the dependencies sane? " +
	"Then emit EXACTLY ONE line and stop: `PARTITION_REVIEW ` followed by JSON " +
	`{"agree":true|false,"reason":"why the partition does or does not cover the brief"}` +
	". agree=false routes back to the tech manager to re-partition. Do not ask questions and do not defer."

// partitionReviewBriefPrompt is the per-campaign context the intent manager reads via
// brief_get at gate 1: the commitments to cover and the tech manager's proposed quests.
func partitionReviewBriefPrompt(cam run.Campaign, brief run.IntentBrief, quests []questPartitionItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CAMPAIGN GOAL: %s\n\n", brief.RestatedGoal)
	b.WriteString("COMMITMENTS the partition must cover:\n")
	for _, cm := range brief.Commitments {
		fmt.Fprintf(&b, "- [%s] %s\n", cm.ID, cm.Statement)
	}
	b.WriteString("\nTHE TECH MANAGER'S PROPOSED QUESTS:\n")
	for _, q := range quests {
		deps := ""
		if len(q.Deps) > 0 {
			deps = " (deps: " + strings.Join(q.Deps, ", ") + ")"
		}
		fmt.Fprintf(&b, "- [%s] %s — %s%s\n", q.ID, q.Title, q.Objective, deps)
	}
	b.WriteString("\nWould these quests plausibly deliver every commitment? Emit one PARTITION_REVIEW verdict.\n")
	return b.String()
}

// techDoneBootstrap is the CONSTANT gate-2 prompt for the tech manager: it confirms
// the campaign branch is technically done (all child quests integrated, review-loop
// clean) and emits a single TECH_DONE verdict. Composition via kb_get, no inlined rubric.
const techDoneBootstrap = "You are the tech manager at gate 2 of a campaign confirming technical sign-off: judge whether the child quests are integrated GREEN on the campaign branch with the review loop clean, before the campaign opens its PRs. " +
	"Call the brief_get tool FIRST to read the campaign goal, the intent review's per-commitment verdicts, and the branch diff command. " +
	"Load and APPLY the detritus doctrine via the kb_get tool: kb_get name=\"roles/tech-lead\" (integration/definition-of-done). Inspect the integrated work (run the diff command, read changed files). Do NOT improvise your own rubric. " +
	"Then emit EXACTLY ONE line and stop: `TECH_DONE ` followed by JSON " +
	`{"done":true|false,"reason":"integration/review-loop status"}` +
	". done=false feeds a remediation quest to close the technical gap. Do not ask questions and do not defer."

// techDoneBriefPrompt is the per-campaign context the tech manager reads via brief_get
// at gate 2: the goal, the intent review's verdicts, and the campaign-branch diff command.
func techDoneBriefPrompt(cam run.Campaign, brief run.IntentBrief, review run.IntentReview, base string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CAMPAIGN GOAL: %s\n\n", brief.RestatedGoal)
	if len(review.Verdicts) > 0 {
		b.WriteString("THE INTENT REVIEW'S PER-COMMITMENT VERDICTS (for context):\n")
		for _, v := range review.Verdicts {
			fmt.Fprintf(&b, "- [%s] %s\n", v.CommitmentID, v.Verdict)
		}
		b.WriteString("\n")
	}
	branch := CampaignBranch(cam)
	fmt.Fprintf(&b, "The child quests integrated onto the campaign branch %q. Inspect it with: git diff %s..%s\n", branch, base, branch)
	b.WriteString("Confirm whether the branch is integrated green with the review loop clean. Emit one TECH_DONE verdict.\n")
	return b.String()
}
