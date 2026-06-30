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
//	3. DECOMPOSE     — the brief's draft tasks become direct child RUNS, each
//	   launched via the EXISTING run executor with CampaignID set and Deliver=branch
//	   so it COMMITS onto the campaign branch (campaign/<id> — the same name in each
//	   impacted repo) and opens NO PR.
//	4. PLAN GATE     — a deterministic check that the proposed children would
//	   plausibly deliver the brief before executing.
//	5. EXECUTE       — run the children sequentially (so their work accumulates on
//	   the shared campaign branch); the campaign ctx halts them on pause/stop.
//	6. INTENT REVIEW — an intent-reviewer agent emits a per-commitment verdict
//	   {satisfied|partial|missed} with cited evidence (it loads core/intent-review
//	   via kb_get and APPLIES it — composition, not an inlined rubric).
//	7. DELIVERY GATE — a `missed` verdict BLOCKS that repo's PR (the campaign stays
//	   blocked with a visible reason; the branch persists for resume). A `partial`
//	   annotates the PR but does NOT block. With no `missed`, open ONE PR PER REPO
//	   from the campaign branch (reusing the run push+openPR machinery).
//
// The loop logic stays in Go (bounded stages, autonomy gating, a global token cap);
// the INTELLIGENCE (the brief, the per-commitment judgment) lives in the agents,
// which compose the detritus doctrine via kb_get rather than re-encoding it here.

// intentLeadID / intentReviewerID are the single agent identities for the brief and
// review stages. They key each agent's brief on the bus the same way tl/coder ids do.
const (
	intentLeadID     = "intent-lead"
	intentReviewerID = "intent-reviewer"
)

// maxBriefAttempts bounds how many times a failed BRIEF GATE routes back to the
// intent lead before the campaign blocks — the brief-phase analogue of maxReplans.
// Tunable via CANDYLAND_CAMPAIGN_BRIEF_ATTEMPTS.
func maxBriefAttempts() int { return envInt("CANDYLAND_CAMPAIGN_BRIEF_ATTEMPTS", 2) }

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

// PauseCampaign halts the supervisor without deleting the campaign: it cancels the
// running drive and records Status=paused + the reason. ResumeCampaign restarts it.
func (c *Conductor) PauseCampaign(id, reason string) bool {
	c.haltCampaignDrive(id)
	return c.UpdateCampaign(id, func(cam *run.Campaign) {
		if cam.Status == "stopped" || cam.Status == "done" {
			return // terminal stays terminal
		}
		cam.Status = "paused"
		if reason != "" {
			cam.PauseReason = reason
		}
	})
}

// ResumeCampaign restarts a paused (or blocked) campaign's supervisor. A campaign
// that isn't paused/blocked is left as-is (false); a terminal campaign can't resume.
func (c *Conductor) ResumeCampaign(id string) bool {
	cam, ok := c.GetCampaign(id)
	if !ok || (cam.Status != "paused" && cam.Status != "blocked") {
		return false
	}
	return c.BeginCampaign(id)
}

// StopCampaign is terminal: it cancels the supervisor and marks the campaign stopped
// with the reason. A stopped campaign never runs again (Begin/Resume refuse it). It
// also stops any in-flight child runs so the process trees don't outlive the campaign.
func (c *Conductor) StopCampaign(id, reason string) bool {
	c.haltCampaignDrive(id)
	c.stopCampaignChildren(id)
	return c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.Status = "stopped"
		if reason != "" {
			cam.PauseReason = reason
		}
	})
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
	for _, r := range c.CampaignChildRuns(id) {
		if r.Status == "running" || r.Status == "planning" {
			c.Command(r.ID, "stop")
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
	defer c.cleanupBusConfigs(intentReviewerID)

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

	// ── Stage 3: DECOMPOSE the settled brief into direct child RUNS (branch delivery). ──
	childPrompts := decomposeChildren(cam, brief)
	if len(childPrompts) == 0 {
		c.blockCampaign(id, "the intent brief produced no draft tasks to decompose into child runs")
		return
	}

	// ── Stage 4: PLAN GATE — would the proposed children plausibly deliver the brief? ──
	if reason, passed := planGate(brief, childPrompts); !c.recordPlanGate(id, passed, reason) || !passed {
		return // blocked — recorded
	}

	// ── Stage 5: EXECUTE the children sequentially on the shared campaign branch. ──
	if !c.executeChildren(ctx, id, cam, folders, childPrompts) {
		return // stopped, or every child failed and there is nothing to review — recorded
	}
	if ctx.Err() != nil {
		return
	}

	// ── Stage 6: FINAL INTENT REVIEW — per-commitment verdicts with cited evidence. ──
	review, ok := c.intentReview(ctx, id, cam, brief, folders)
	if ctx.Err() != nil {
		return
	}
	if !ok {
		return // the reviewer produced no verdict (blocked) — recorded
	}

	// ── Stage 7: DELIVERY GATE — `missed` blocks the repo PR; `partial` annotates. ──
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

// executeChildren launches the campaign's child runs SEQUENTIALLY (so their commits
// accumulate on the shared campaign branch), each via the EXISTING run executor with
// CampaignID set and Deliver=branch (commit onto CampaignBranch, open no PR). The
// campaign ctx halts an in-flight child on pause/stop. A global token cap, once
// exceeded, degrades-to-serial-then-deliver-partial: it skips the remaining children
// and proceeds to review (never a pre-PR pause that strands with no PR). It returns
// false only when stopped, or when nothing landed at all (blocked).
func (c *Conductor) executeChildren(ctx context.Context, id string, cam run.Campaign, folders []string, prompts []childPrompt) bool {
	tokenCap := effectiveTokenCap(cam)
	launched, completed := 0, 0
	for _, cp := range prompts {
		if ctx.Err() != nil {
			return false
		}
		if tokenCap > 0 {
			if used := c.campaignTokensUsed(id); used >= tokenCap {
				log.Printf("candyland: campaign %s token cap reached (%d/%d) — delivering partial", id, used, tokenCap)
				c.appendCampaignNote(id, fmt.Sprintf("token cap reached (%d/%d) — skipped %d remaining child run(s), delivering partial", used, tokenCap, len(prompts)-launched))
				break
			}
		}
		childID := c.launchCampaignChild(ctx, id, cam, folders, cp)
		launched++
		c.UpdateCampaign(id, func(cam *run.Campaign) { cam.RunIDs = append(cam.RunIDs, childID) })
		if ctx.Err() != nil {
			return false
		}
		child, ok := c.Get(childID)
		if ok {
			c.addCampaignTokens(id, child.TokensUsed)
			if child.Status == "done" && child.Error == "" {
				completed++
			}
		}
	}
	if completed == 0 {
		c.blockCampaign(id, "no child run delivered work onto the campaign branch — nothing to review or deliver")
		return false
	}
	return true
}

// launchCampaignChild creates and drives ONE child run via the existing run executor,
// stamping CampaignID, the campaign branch (campaign/<id> — the same name in each
// impacted repo), and Deliver=branch (so it commits onto the branch and opens NO PR —
// children never open PRs). It blocks until
// the child reaches a terminal state or the campaign is paused/stopped.
func (c *Conductor) launchCampaignChild(ctx context.Context, id string, cam run.Campaign, folders []string, cp childPrompt) string {
	childID := c.Create(run.Spec{Folders: folders, Prompt: cp.prompt, Title: cp.title})
	branch := CampaignBranch(cam)
	c.Update(childID, func(r *run.Run) {
		r.CampaignID = id
		r.Branch = branch
		r.Deliver = run.DeliverBranch
	})
	c.Begin(childID)
	for {
		select {
		case <-ctx.Done():
			c.Command(childID, "stop")
			return childID
		case <-time.After(50 * time.Millisecond):
		}
		r, ok := c.Get(childID)
		if !ok {
			return childID
		}
		if r.Status == "done" || r.Status == "cancelled" {
			return childID
		}
	}
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

	prs := make([]run.PR, 0, len(folders))
	for _, repo := range folders {
		repo = expandHome(repo)
		if !isGitRepo(ctx, repo) {
			continue // only impacted git repos get a PR
		}
		base, _ := currentBranch(ctx, repo)
		pr := run.PR{Repo: repoBase(repo)}
		// Only repos the campaign branch actually exists on have work to deliver.
		if sha, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", branch); err != nil || sha == "" {
			continue
		}
		if err := pushBranch(ctx, repo, branch); err != nil {
			pr.Err = "push failed: " + err.Error()
		} else if url, err := openPR(ctx, repo, base, branch, title, body); err != nil {
			pr.Err = "PR failed: " + err.Error()
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

// planGate is the PLAN GATE: a deterministic check that the proposed children would
// plausibly deliver the brief before executing — there must be at least one child,
// and at least as many children as is reasonable to cover the commitments is NOT
// required (one child can satisfy several), but zero children can deliver nothing.
func planGate(brief run.IntentBrief, children []childPrompt) (string, bool) {
	if len(children) == 0 {
		return "no child work was decomposed from the brief — nothing would be delivered", false
	}
	if len(brief.Commitments) == 0 {
		return "the brief has no commitments for the children to deliver against", false
	}
	return fmt.Sprintf("%d child run(s) decomposed to deliver %d commitment(s)", len(children), len(brief.Commitments)), true
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

func commitmentByID(brief run.IntentBrief) map[string]string {
	m := make(map[string]string, len(brief.Commitments))
	for _, cm := range brief.Commitments {
		m[cm.ID] = cm.Statement
	}
	return m
}

// --- decomposition ---

// childPrompt is one decomposed child run: a title + the prompt the run executor
// drives. It is an internal value (not stored), the campaign analogue of a quest's
// per-item child-run prompt.
type childPrompt struct {
	title  string
	prompt string
}

// decomposeChildren turns the settled brief into direct child RUNS — one per draft
// task (v1: direct runs are sufficient; child quests are allowed but optional). Each
// child prompt frames the task against the campaign goal, scope-by-domain, and the
// commitments so the child's tech-lead/coders inherit the campaign's bounds and
// commit onto the campaign branch.
func decomposeChildren(cam run.Campaign, brief run.IntentBrief) []childPrompt {
	out := make([]childPrompt, 0, len(brief.DraftTasks))
	for _, task := range brief.DraftTasks {
		if strings.TrimSpace(task) == "" {
			continue
		}
		out = append(out, childPrompt{title: truncate(task, 72), prompt: childCampaignPrompt(cam, brief, task)})
	}
	return out
}

// childCampaignPrompt frames one draft task as a child run, inheriting the campaign
// goal / scope / commitments so the child stays in-bounds. The child commits onto the
// campaign branch and opens no PR (Deliver=branch, set at launch).
func childCampaignPrompt(cam run.Campaign, brief run.IntentBrief, task string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", task)
	fmt.Fprintf(&b, "This is one task of the campaign goal: %s\n", brief.RestatedGoal)
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

// recordPlanGate records the plan gate and blocks the campaign on a failure. It
// returns whether the campaign is still alive (false when the UpdateCampaign failed,
// i.e. an unknown campaign).
func (c *Conductor) recordPlanGate(id string, passed bool, reason string) bool {
	ok := c.UpdateCampaign(id, func(cam *run.Campaign) {
		cam.PlanGate = run.GateResult{Passed: passed, Reason: reason, DecidedAt: time.Now().UTC().Format(time.RFC3339)}
	})
	if ok && !passed {
		c.blockCampaign(id, "plan gate: "+reason)
	}
	return ok
}

// blockCampaign records a hard blocker with a visible reason. A blocked campaign is
// not terminal — its branch persists and ResumeCampaign restarts the supervisor; it
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
	fmt.Fprintf(&b, "AUTONOMY: %s (a launched campaign — decide and escalate within the hierarchy; never ask the user).\n", cam.AutonomyLevel)
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
