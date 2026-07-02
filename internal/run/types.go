// Package run defines the run/agent/task data model that flows through ooo to
// the React app. The JSON shape matches exactly what the dashboard panels
// consume, so the UI reads live ooo state with no client-side mock.
package run

// TraceVersion is the schema version of the exported RunTrace. Bump it whenever
// the normalized trace shape changes so a future central store can detect and
// migrate older records. The version travels with every exported trace.
const TraceVersion = 1

// Event is one parsed stream-json line from an agent process. Event is nested
// under Agent, so the agent id is implicit and slice order already gives the
// per-agent sequence; TaskID and Ts are additive ordering/linking aids.
type Event struct {
	T     string `json:"t"` // system|text|tool|test|result
	Text  string `json:"text,omitempty"`
	Name  string `json:"name,omitempty"`  // tool name
	Input string `json:"input,omitempty"` // tool input summary (compact, for the live dashboard)
	// InputFull/TextFull carry the COMPLETE untruncated payload for a tool event's
	// input and a result event's text. Input/Text stay truncated so the realtime
	// dashboard renders a compact summary; the full fields are persisted alongside
	// them and served verbatim by the run snapshot/trace API so the whole output is
	// retrievable with no truncation. Populated only when the payload was truncated
	// (otherwise Input/Text already hold it in full) — hence omitempty.
	InputFull string `json:"inputFull,omitempty"` // complete tool input when Input was truncated
	TextFull  string `json:"textFull,omitempty"`  // complete result text when Text was truncated
	Pass      int    `json:"pass,omitempty"`
	Fail      int    `json:"fail,omitempty"`
	TaskID    string `json:"taskId,omitempty"` // task this event belongs to, when known (best-effort)
	Ts        string `json:"ts,omitempty"`     // RFC3339 timestamp set when the event is appended
}

// Agent is one spawned worker (a headless claude process).
type Agent struct {
	ID       string  `json:"id"`
	Role     string  `json:"role"`
	Emoji    string  `json:"emoji"`
	Task     string  `json:"task"`
	State    string  `json:"state"` // idle|working|retrying|blocked|integrating|green|done
	Activity string  `json:"activity"`
	Tokens   int     `json:"tokens"`
	Budget   int     `json:"budget"`
	Worktree string  `json:"worktree"`
	Model    string  `json:"model"`
	Events   []Event `json:"events"`
}

// Task is one fork-safe slice of the partition.
type Task struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Files []string `json:"files"`
	Test  string   `json:"test"`
	Owner string   `json:"owner"` // agent id, "" when unassigned
	State string   `json:"state"`
	Deps  []string `json:"deps"`
}

// PR is one opened (or attempted) pull request. A run that spans multiple repos
// opens one per impacted repo; Err is set instead of URL when that repo's push or
// PR failed (partial-failure isolation — one repo's failure doesn't fail the rest).
type PR struct {
	Repo string `json:"repo"`          // the repo folder this PR belongs to
	URL  string `json:"url,omitempty"` // set when the PR opened
	Err  string `json:"err,omitempty"` // set when push/PR failed for this repo
}

// Run is the full state of a run — the object stored at ooo key runs/<id>.
type Run struct {
	ID    string `json:"id"`
	Title string `json:"title"` // optional; UI derives a label when empty
	// QuestID/CampaignID are parent links for later quest/campaign grouping. They
	// stay empty for standalone runs today; the fields exist now so a later phase
	// can populate them without a schema migration.
	QuestID    string `json:"questId,omitempty"`
	CampaignID string `json:"campaignId,omitempty"`
	// Deliver is how the run ships its work: "pr" (the default — open one PR per
	// impacted repo) or "branch" (a campaign/quest-owned child run — commit + push
	// onto the campaign branch (campaign/<id> — the same name in each impacted repo)
	// and open NO PR; the parent opens the PR at the end after intent review). Empty ==
	// "pr" (a standalone run). A branch-delivered run's branch is that campaign branch
	// (set by the parent at launch).
	// (always serialized — no omitempty — so the frontend can key UI on r.deliver
	// even for a standalone "pr" run, where the field would otherwise be absent).
	Deliver Delivery `json:"deliver"`
	// TargetPR is the existing PR number a "feedback"/"review" run updates in place
	// (0 for "pr"/"branch", which open/own their own delivery). The run resolves the
	// PR's head branch, bases its work on it, and pushes back — never opening a new PR.
	TargetPR int    `json:"targetPr,omitempty"`
	Prompt   string `json:"prompt"` // the instruction actually sent to the agents
	// OriginalIntent is the launch prompt, set ONCE at run creation and never
	// rewritten (an Edit changes Prompt, not this). Final review compares output
	// against the original intent, not just task completion. For a standalone run
	// OriginalIntent == the first Prompt.
	OriginalIntent string   `json:"originalIntent,omitempty"`
	Branch         string   `json:"branch"`
	Folders        []string `json:"folders"`            // the run's working folders, passed at launch (folders[0] = the git repo it branches/PRs in); the rest are --add-dir context
	Status         string   `json:"status"`             // planning|running|paused|done|cancelled
	Archived       bool     `json:"archived,omitempty"` // cleared from the dashboard; still kept in the Tasks history
	Phase          int      `json:"phase"`              // index into Phases (Build..PR)
	Progress       float64  `json:"progress"`           // 0..1
	StatusLine     string   `json:"statusLine,omitempty"`
	Error          string   `json:"error,omitempty"` // set when a run hits an unrecoverable error
	PrURL          string   `json:"prUrl,omitempty"` // the primary PR (folders[0]); first opened — kept for back-compat
	PRs            []PR     `json:"prs,omitempty"`   // one per impacted repo (multi-repo runs); PrURL mirrors the first
	TokensUsed     int      `json:"tokensUsed"`
	TokensBudget   int      `json:"tokensBudget"`
	CostUsd        float64  `json:"costUsd"`
	TasksGreen     int      `json:"tasksGreen"`
	TasksTotal     int      `json:"tasksTotal"`
	HasDag         bool     `json:"hasDag"`
	Agents         []Agent  `json:"agents"`
	Tasks          []Task   `json:"tasks"`
	Executor       string   `json:"executor"` // always "claude" — runs are only ever driven by real headless Claude Code
}

// Audit is the queryable record of a completed run, derived from its final
// state and stored at ooo key audits/<id> — local-first, with a documented
// central-server sync seam (conductor.postAudit).
type Audit struct {
	RunID   string      `json:"runId"`
	Status  string      `json:"status"`
	Phase   int         `json:"phase"`
	Tasks   []TaskAudit `json:"tasks"`
	Tokens  int         `json:"tokens"`
	PrURL   string      `json:"prUrl,omitempty"`
	Error   string      `json:"error,omitempty"`
	EndedAt string      `json:"endedAt"`
}

// TaskAudit is one task's verification outcome in an Audit.
type TaskAudit struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Pass  int    `json:"pass"`
	Fail  int    `json:"fail"`
}

// Spec launches a run. Folders are the working folders supplied by the launcher
// (the VSCode session's cwd, via the candyland trigger MCP) — folders[0] is the
// git repo the run branches and opens its PR in; the rest are --add-dir context.
// There is no workspace abstraction: candyland tracks runs and their tasks, not
// a persisted set of folders.
type Spec struct {
	Folders []string `json:"folders"`
	Prompt  string   `json:"prompt"`
	Title   string   `json:"title"`
	// Deliver is how the run ships its work: "pr" (the default — open one PR per
	// impacted repo) or "feedback"/"review" (update an EXISTING PR in place —
	// base the work on that PR's head branch and push back, opening NO new PR).
	// Empty == "pr". Mirrors the same fields on CampaignSpec/QuestSpec so a
	// standalone run (POST /api/runs) can address PR feedback too, not only
	// quest/campaign children.
	Deliver Delivery `json:"deliver,omitempty"`
	// TargetPR is the existing PR number a "feedback"/"review" run updates in
	// place (0 for "pr"). Required (> 0) when Deliver is feedback/review.
	TargetPR int `json:"targetPr,omitempty"`
}

// Phases are the lifecycle stages shown in the stepper.
var Phases = []string{"Build", "Integrate", "Review", "PR"}

// Phase indices into Phases — named so phase-index sites read clearly instead of
// using magic literals or len(Phases)-N arithmetic.
const (
	PhaseBuild     = 0
	PhaseIntegrate = 1
	PhaseReview    = 2
	PhasePR        = 3
)

// AutonomyLevel is how much human gating a quest's child runs carry. The value
// rides on the QuestSpec at launch and is persisted on the Quest; the tick loop
// (a later phase) reads it to decide whether to report only, gate the PR, or run
// unattended. The three settled levels:
//   - L1 (report-only): discover and triage, but launch nothing — surface findings.
//   - L2 (assisted-gate-PR): launch child runs, but hold each PR for human gate.
//   - L3 (unattended): launch and deliver without a per-PR human gate.
type AutonomyLevel string

const (
	AutonomyReportOnly AutonomyLevel = "L1" // discover/triage only, launch nothing
	AutonomyGatePR     AutonomyLevel = "L2" // launch runs, gate each PR for a human
	AutonomyUnattended AutonomyLevel = "L3" // launch and deliver without a per-PR gate
)

// Delivery is how a quest's child runs ship their work. A standalone quest opens
// a PR per child run ("pr"); a campaign-owned quest commits onto the campaign branch
// ("branch") derived as campaign/<campaignID> — the same name in each impacted repo
// (NOT a scalar branch name — settled decision). The derivation lives in
// conductor.QuestBranch.
type Delivery string

const (
	DeliverPR     Delivery = "pr"     // standalone quest: one PR per child run
	DeliverBranch Delivery = "branch" // campaign-owned: commit onto campaign/<campaignID>
	// DeliverFeedback updates an EXISTING PR in place: the run bases its work on
	// that PR's head branch and pushes back onto it, opening NO new PR. The target
	// PR number rides on TargetPR. Multi-repo: each repo's findings land on that
	// repo's existing PR.
	DeliverFeedback Delivery = "feedback"
	// DeliverReview produces findings and opens NO PR. When it had findings to apply
	// it behaves like feedback (updates TargetPR); when it had none it ends as a
	// review-only no-op with an empty prUrl by design.
	DeliverReview Delivery = "review"
)

// QuestSpec is the launch input for a quest — a Candyland-native iterative loop
// (the generalized homologue of /janitor) that repeatedly discovers/triages work
// items and launches child runs, producing many PRs over time. It mirrors run.Spec
// (launch input) the way Quest mirrors Run (persisted state). The tick loop,
// discover/triage/launch logic, and delivery wiring are later phases — this spec
// only carries the settled launch parameters.
type QuestSpec struct {
	// Objective is the refined intent that drives discovery/triage each tick. Set
	// once at creation onto Quest.OriginalObjective and never rewritten, mirroring
	// how Run.OriginalIntent is captured once (see Quest.OriginalObjective).
	Objective string   `json:"objective"`
	Folders   []string `json:"folders,omitempty"` // target folders/repos (folders[0] = the git repo child runs branch/PR in)
	Scope     string   `json:"scope,omitempty"`   // human-readable bound on what work is in-scope
	// Safety is the safety boundary: the files/areas a quest's child runs must not
	// touch (the quest-level analogue of a coder's fork-safe boundary).
	Safety string   `json:"safety,omitempty"`
	Verify []string `json:"verify,omitempty"` // verification command(s) every child run must pass green
	Stop   string   `json:"stop,omitempty"`   // stop/pause criteria (when to halt the loop)
	// AutonomyLevel gates the child runs (L1 report-only | L2 assisted-gate-PR |
	// L3 unattended). Empty defaults to L1 at creation (report-only is the safe floor).
	AutonomyLevel AutonomyLevel `json:"autonomyLevel,omitempty"`
	TokenBudget   int           `json:"tokenBudget,omitempty"` // cap on total tokens across all ticks/child runs
	// Deliver is "pr" (standalone) or "branch" (campaign-owned). Empty defaults to
	// "pr" at creation. When "branch", the branch is campaign/<campaignID> — the same
	// name in each impacted repo.
	Deliver Delivery `json:"deliver,omitempty"`
	// TargetPR is the existing PR number a "feedback"/"review" quest's child runs
	// update in place (required >0 for those modes; 0 for "pr"/"branch").
	TargetPR int `json:"targetPr,omitempty"`
	// CampaignID is the parent campaign link, set when this quest is launched under a
	// campaign. Empty for a standalone quest.
	CampaignID string `json:"campaignId,omitempty"`
}

// WorkItem is one unit of work a quest's discovery surfaced and triage decided on.
// It links the originating tick, the evidence/classification/decision, the child
// run launched to do it (when one was), and the final disposition.
type WorkItem struct {
	ID             string `json:"id"`
	SourceTick     string `json:"sourceTick"`               // the Tick.ID that discovered this item
	Evidence       string `json:"evidence,omitempty"`       // why discovery flagged it
	Classification string `json:"classification,omitempty"` // discovery's category for the item
	Decision       string `json:"decision,omitempty"`       // triage's call (do now | skip | block)
	ChildRunID     string `json:"childRunId,omitempty"`     // the run launched for this item, when one was
	Disposition    string `json:"disposition,omitempty"`    // final outcome (completed | skipped | blocked)
}

// Tick is one iteration of the quest loop: a discovery pass, the triage decisions
// it produced, the child runs it launched, the PRs that resulted, any blockers,
// and what the loop will do next.
type Tick struct {
	ID               string   `json:"id"`
	StartedAt        string   `json:"startedAt"`         // RFC3339 set when the tick begins
	EndedAt          string   `json:"endedAt,omitempty"` // RFC3339 set when the tick completes
	DiscoverySummary string   `json:"discoverySummary,omitempty"`
	TriageDecisions  []string `json:"triageDecisions,omitempty"`
	LaunchedRunIDs   []string `json:"launchedRunIds,omitempty"`
	PRs              []PR     `json:"prs,omitempty"` // PRs opened during this tick
	Blockers         []string `json:"blockers,omitempty"`
	NextAction       string   `json:"nextAction,omitempty"`
}

// Quest is the full persisted state of a quest — the object stored at ooo key
// quests/<id>. It mirrors Run (the stored run object) for a quest's iterative loop:
// stable id + optional parent campaign link, the objective fields carried from the
// spec, lifecycle status, autonomy/budget/delivery, the work items and ticks the
// loop accumulates, rollup counters for the dashboard, and the schema version. The
// tick loop that populates Ticks/WorkItems is a later phase — this is the model and
// its persistence only.
type Quest struct {
	ID         string `json:"id"`
	CampaignID string `json:"campaignId,omitempty"` // parent campaign link; empty for a standalone quest
	// OriginalObjective is the launch objective, set ONCE at creation and never
	// rewritten — the quest analogue of Run.OriginalIntent. Final review compares
	// the quest's output against this, not against a mutated objective.
	OriginalObjective string   `json:"originalObjective"`
	Objective         string   `json:"objective"` // the working objective (may evolve; starts == OriginalObjective)
	Folders           []string `json:"folders,omitempty"`
	Scope             string   `json:"scope,omitempty"`
	Safety            string   `json:"safety,omitempty"`
	Verify            []string `json:"verify,omitempty"`
	Stop              string   `json:"stop,omitempty"`
	// Status is the lifecycle state: running|paused|stopped|blocked|done|surfaced-only.
	// "surfaced-only" is a distinct TERMINAL state (like done) for a quest that
	// delivered nothing in-scope — it discovered/surfaced or skipped items but
	// executed 0 and opened 0 PRs (and was NOT branch-delivery-by-design). A
	// branch-delivered quest with prsOpened:0 is legitimately done, not surfaced-only.
	// PauseReason carries the human-readable reason when paused/blocked.
	Status string `json:"status"`
	// Summary is a human-readable description of a terminal outcome (e.g. the
	// report-only no-op accounting, or the intent↔autonomy mismatch warning). It is
	// stamped when the quest reaches a terminal/blocked state so the dashboard and
	// CLI can name a no-op as such rather than show an undifferentiated "done".
	Summary       string        `json:"summary,omitempty"`
	PauseReason   string        `json:"pauseReason,omitempty"`
	AutonomyLevel AutonomyLevel `json:"autonomyLevel"`
	TokenBudget   int           `json:"tokenBudget,omitempty"`
	TokensUsed    int           `json:"tokensUsed"`
	Deliver       Delivery      `json:"deliver"`
	// TargetPR is the existing PR number "feedback"/"review" child runs update in
	// place (0 for "pr"/"branch"). Stamped from the spec at creation.
	TargetPR  int        `json:"targetPr,omitempty"`
	WorkItems []WorkItem `json:"workItems"`
	Ticks     []Tick     `json:"ticks"`
	// Rollup fields for the dashboard, recomputed from WorkItems/Ticks by the loop.
	PRsOpened      int `json:"prsOpened"`
	ItemsCompleted int `json:"itemsCompleted"`
	ItemsSkipped   int `json:"itemsSkipped"`
	ItemsBlocked   int `json:"itemsBlocked"`
	// Agents are the quest's OWN coordinating agents (the quest-lead that runs the
	// discovery/triage pass each tick) — distinct from the agents of its child runs.
	// The recording path routes a quest-lead's state+events here so the dashboard can
	// show what the quest itself is doing, beyond its child runs. Non-nil at creation
	// so it marshals to [] not null (matching Run.Agents).
	Agents       []Agent `json:"agents"`
	LastProgress string  `json:"lastProgress,omitempty"` // RFC3339 of the last forward step
	CreatedAt    string  `json:"createdAt"`              // RFC3339 set once at creation
	UpdatedAt    string  `json:"updatedAt"`              // RFC3339 set on every persisted mutation
	// TraceVersion is the schema version of this Quest record, mirroring how a Run's
	// exported trace carries TraceVersion so a future store can detect/migrate.
	TraceVersion int `json:"traceVersion"`
}

// Commitment is one checkable assertion the campaign commits to delivering. The
// intent-lead derives commitments from the original input during the brief phase;
// each is later judged by intent review (see CommitmentVerdict). Storing the
// assertion now lets a later phase attach a verdict without a schema migration.
type Commitment struct {
	ID        string `json:"id"`
	Statement string `json:"statement"` // one checkable assertion (the unit intent review judges)
}

// IntentBrief is the intent-lead's restatement of the campaign's original input
// into a structured plan: the goal as understood, scope split by domain, the
// questions resolved vs still open, a draft task list, dependencies, a rough
// sizing, suggested review routing, and the checkable commitments. It is built by
// the brief phase (a later task); this is the data shape it persists to.
type IntentBrief struct {
	RestatedGoal      string       `json:"restatedGoal,omitempty"`
	ScopeByDomain     []string     `json:"scopeByDomain,omitempty"`
	ResolvedQuestions []string     `json:"resolvedQuestions,omitempty"`
	OpenQuestions     []string     `json:"openQuestions,omitempty"`
	DraftTasks        []string     `json:"draftTasks,omitempty"`
	Dependencies      []string     `json:"dependencies,omitempty"`
	RoughSizing       string       `json:"roughSizing,omitempty"`
	ReviewRouting     []string     `json:"reviewRouting,omitempty"` // suggested human review areas/reviewers (suggestions only, not agents)
	Commitments       []Commitment `json:"commitments,omitempty"`
}

// GateResult is the outcome of a campaign gate (the post-brief BriefGate and the
// post-plan PlanGate). It records whether the gate passed, why, and when it was
// decided. The gates' execution is a later phase; this is the result shape they
// persist. DecidedAt is empty until the gate has run (Passed==false then means
// "not yet decided", not "failed").
type GateResult struct {
	Passed    bool   `json:"passed"`
	Reason    string `json:"reason,omitempty"`
	DecidedAt string `json:"decidedAt,omitempty"` // RFC3339 set when the gate decides
}

// CommitmentVerdict is intent review's judgment of one Commitment: whether the
// campaign's delivered work satisfied it, and the evidence. A "missed" verdict
// blocks that repo's PR; a "partial" annotates it (the gate logic is a later
// phase — this only carries the data). It is the core/intent-review output shape.
type CommitmentVerdict struct {
	CommitmentID string   `json:"commitmentId"`
	Verdict      string   `json:"verdict"`            // satisfied|partial|missed
	Evidence     []string `json:"evidence,omitempty"` // what backs the verdict
}

// IntentReview is the final per-commitment judgment of a campaign's delivered
// work against its original input, holding one CommitmentVerdict per commitment.
// A "missed" blocks the affected repo's PR; a "partial" annotates it. The review
// itself runs in a later phase; this is the persisted output shape.
type IntentReview struct {
	Verdicts   []CommitmentVerdict `json:"verdicts,omitempty"`
	ReviewedAt string              `json:"reviewedAt,omitempty"` // RFC3339 set when the review completes
}

// CampaignSpec is the launch input for a campaign — the program-level container
// above quests and runs. Candyland owns the full intent→delivery cycle for a
// campaign (validation, decomposition into child quests/runs, review, per-repo
// delivery). This spec carries only the settled launch parameters; the supervisor
// /intent-lead flow, gates, and intent review are later phases. It mirrors how
// run.Spec/QuestSpec carry launch input for their persisted-state counterparts.
type CampaignSpec struct {
	// Input is the original instruction. It is captured ONCE onto
	// Campaign.OriginalInput at creation and never rewritten (final intent review
	// compares delivered work against this).
	Input   string   `json:"input"`
	Folders []string `json:"folders,omitempty"` // target folders/repos (optional; folders[0] = the git repo children branch in)
	// AutonomyLevel gates the campaign's children. Campaigns default to L2 and are
	// NEVER L1: a report-only campaign would strand with no PR (settled decision).
	AutonomyLevel AutonomyLevel `json:"autonomyLevel,omitempty"`
	TokenBudget   int           `json:"tokenBudget,omitempty"` // cap on total tokens across the whole campaign
	// Deliver is how the campaign's child runs ship their work. Empty defaults to
	// "pr" (the campaign opens one PR per impacted repo at the end; children commit
	// onto the campaign branch). "feedback"/"review" land on an EXISTING PR
	// (TargetPR) instead of opening a new one — the child runs carry that mode.
	Deliver Delivery `json:"deliver,omitempty"`
	// TargetPR is the existing PR number a "feedback"/"review" campaign's child runs
	// update in place (required >0 for those modes; 0 for "pr").
	TargetPR int `json:"targetPr,omitempty"`
}

// Campaign is the full persisted state of a campaign — the object stored at ooo
// key campaigns/<id>. It is the program-level container above quests and runs:
// the immutable original input, the intent-lead's structured brief, the post-brief
// and post-plan gates, the child quests/runs, the final per-repo delivery (one PR
// per repo after intent review — children commit to the campaign branch
// (campaign/<id> — the same name in each impacted repo) and open no PR), the
// suggested human review routing, the final intent review,
// lifecycle status, autonomy/budget, timestamps, and the schema version. The
// supervisor/intent-lead flow, gate execution, and intent review that populate
// these fields are later phases — this is the model and its persistence only.
type Campaign struct {
	ID string `json:"id"`
	// OriginalInput is the launch input, set ONCE at creation and never rewritten —
	// the campaign analogue of Run.OriginalIntent. Final intent review compares the
	// campaign's delivered work against this, not a mutated input.
	OriginalInput string `json:"originalInput"`
	// Folders are the campaign's target folders/repos, carried from the spec
	// (folders[0] = the git repo children branch in). The supervisor runs its
	// intent-lead/reviewer agents and launches child runs against these.
	Folders []string `json:"folders,omitempty"`
	// IntentBrief is the intent-lead's structured restatement of OriginalInput.
	// Empty until the brief phase (a later task) populates it.
	IntentBrief IntentBrief `json:"intentBrief"`
	// BriefGate (post-brief) and PlanGate (post-plan) are the campaign gates. The
	// gate execution is a later phase; these hold the results.
	BriefGate GateResult `json:"briefGate"`
	PlanGate  GateResult `json:"planGate"`
	// QuestIDs/RunIDs are the campaign's children, linked as they are launched (a
	// later phase). Children commit onto the campaign branch (campaign/<id> — the same
	// name in each impacted repo) and open no PR.
	QuestIDs []string `json:"questIds"`
	RunIDs   []string `json:"runIds"`
	// PRs is the final delivery: one PR per impacted repo, opened at the end after
	// intent review (reusing the run PR type). The branch the children commit to —
	// campaign/<id>, the same name in each impacted repo — is derived by
	// conductor.CampaignBranch.
	PRs []PR `json:"prs,omitempty"`
	// ReviewRouting is the suggested human review areas/reviewers (suggestions only,
	// not agents) — mirrors IntentBrief.ReviewRouting at the campaign level.
	ReviewRouting []string `json:"reviewRouting,omitempty"`
	// IntentReview is the final per-commitment judgment of delivered work. Empty
	// until the intent-review phase (a later task) populates it.
	IntentReview IntentReview `json:"intentReview"`
	// Status is the lifecycle state: running|paused|stopped|blocked|done.
	// PauseReason carries the transient human-readable reason when paused/blocked
	// (delivery/block overwrite or clear it). Notes carries DURABLE non-blocking
	// notes (e.g. a token-cap degrade-to-partial) that delivery/block never clear,
	// so an operator still learns the campaign delivered partial after a clean PR.
	Status        string        `json:"status"`
	PauseReason   string        `json:"pauseReason,omitempty"`
	Notes         []string      `json:"notes,omitempty"`
	AutonomyLevel AutonomyLevel `json:"autonomyLevel"`
	TokenBudget   int           `json:"tokenBudget,omitempty"`
	TokensUsed    int           `json:"tokensUsed"`
	// Deliver is how the campaign's child runs ship their work: "pr" (the default —
	// children commit onto the campaign branch, the campaign opens one PR per impacted
	// repo at the end) or "feedback"/"review" (children land on the EXISTING TargetPR
	// instead of the campaign branch — see launchCampaignChild's propagation). Set at
	// creation, defaulted to "pr" when empty. Always serialized (no omitempty) so the
	// frontend can key UI on cam.deliver even for a default "pr" campaign.
	Deliver Delivery `json:"deliver"`
	// TargetPR is the existing PR number "feedback"/"review" child runs update in
	// place (0 for "pr"). Stamped from the spec at creation.
	TargetPR int `json:"targetPr,omitempty"`
	// Agents are the campaign's OWN coordinating agents (the supervisor's intent-lead
	// and intent-reviewer) — distinct from the agents of its child quests/runs. The
	// recording path routes their state+events here so the dashboard can show what the
	// campaign itself is doing, beyond its children. Non-nil at creation so it marshals
	// to [] not null (matching Run.Agents).
	Agents    []Agent `json:"agents"`
	CreatedAt string  `json:"createdAt"` // RFC3339 set once at creation
	UpdatedAt string  `json:"updatedAt"` // RFC3339 set on every persisted mutation
	// TraceVersion is the schema version of this Campaign record, mirroring how a
	// Run's exported trace and a Quest carry TraceVersion for future migration.
	TraceVersion int `json:"traceVersion"`
}

// RunTrace is the normalized, exportable trace of a single run: the stored Run
// plus its Audit (when present) and the schema version, in a stable JSONL-friendly
// shape. It is shape-readiness for a later central store — it embeds the existing
// Run (stable IDs, parent links, agents, task graph, events, PRs, token/cost) and
// the Audit verbatim, adding nothing the UI doesn't already see except TraceVersion.
//
// REDACTION SEAM: before any future sync to a central store, sensitive payloads
// (e.g. Event.Text/Input, Run.Prompt/OriginalIntent) must be redacted here. This
// is local export only today — no redaction is applied. Do NOT add a central
// store/sync from this struct; that is a separate, later phase.
type RunTrace struct {
	TraceVersion int    `json:"traceVersion"`
	Run          *Run   `json:"run"`
	Audit        *Audit `json:"audit,omitempty"`
}
