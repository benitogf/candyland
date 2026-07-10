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
	ID       string `json:"id"`
	Role     string `json:"role"`
	Emoji    string `json:"emoji"`
	Task     string `json:"task"`
	State    string `json:"state"` // idle|working|retrying|blocked|integrating|green|done
	Activity string `json:"activity"`
	Tokens   int    `json:"tokens"`
	Budget   int    `json:"budget"`
	Worktree string `json:"worktree"`
	// Model is the ACTUAL configured model the process was spawned with (the
	// effective --model), and Thinking the effective --effort level — the L2
	// telemetry fields /learn mines to correlate outcome with model/effort. Both
	// are wired from the fresh per-spawn agentConfig(role), never a decoupled literal.
	Model    string `json:"model"`
	Thinking string `json:"thinking,omitempty"`
	// ToolCalls counts the tool_use events this agent emitted (L2 telemetry:
	// tool-call count), stamped as they stream in.
	ToolCalls int `json:"toolCalls,omitempty"`
	// Raw token usage accumulated from the agent's result lines — UNSCALED counts
	// (Tokens above keeps its /1000 display scaling). The input vs cache-read vs
	// cache-creation split makes per-agent cost and cache efficiency derivable,
	// e.g. how much a forked template session saved versus a cold bootstrap.
	InputTokens         int     `json:"inputTokens,omitempty"`
	CacheReadTokens     int     `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int     `json:"cacheCreationTokens,omitempty"`
	Events              []Event `json:"events"`
}

// Postmortem is the schema a terminal `blocked` (a capability failure — the "last
// breath" of §1/§3) MUST carry to be valid. A blocked write without ALL of these
// fields is incomplete and is rejected/bounced back to the agent (see
// conductor.validatePostmortem). It is persisted on the run/quest record
// and rendered in the detail view so a blocker is fully explained, never a bare stop.
type Postmortem struct {
	Attempts           []string `json:"attempts"`           // each attempt made and its result
	FailingCapability  string   `json:"failingCapability"`  // the exact capability that failed (one line)
	Evidence           string   `json:"evidence"`           // verbatim commands + error output proving the failure
	RootCauseSoFar     string   `json:"rootCauseSoFar"`     // root-cause analysis as far as it got
	HumanUnblockAction string   `json:"humanUnblockAction"` // precisely what a human must provide to unblock
	PartialWorkState   string   `json:"partialWorkState"`   // branch, commits, ledger refs for work already done
}

// Escalation is one recorded upward DECISION escalation (§2): a decision a lower
// tier could not resolve alone travelled exactly one tier up, was decided at the
// lowest tier with authority (never by a human), and both the question and its
// resolution are recorded here — the audit trail the dashboard shows read-only and
// the L2 telemetry ledger /learn mines for escalation/decision events.
type Escalation struct {
	From     string `json:"from"`         // the tier that escalated (e.g. run tech-lead)
	To       string `json:"to"`           // the tier it escalated to (one tier up)
	Question string `json:"question"`     // the decision that needed authority
	Decider  string `json:"decider"`      // the tier that actually decided (lowest with authority)
	Answer   string `json:"answer"`       // the decision made (+ recorded, never sent to a human)
	At       string `json:"at,omitempty"` // RFC3339 when resolved
}

// IncidentNote is one self-acknowledged incident an agent voluntarily reported
// during its run: something that went wrong and was recovered from or worked around
// (a flaky dependency, a retried external call, a surprising state it had to repair)
// — distinct from a terminal Postmortem (a capability failure that stops the flow)
// and from an Escalation (a decision handed up a tier). It is a NON-TERMINAL
// self-report: the agent emits a `INCIDENT <json>` line and keeps working. It is
// persisted on the run/quest record and retrievable via the same
// data-access path /learn mines (see conductor.captureIncidents).
type IncidentNote struct {
	Agent    string `json:"agent,omitempty"`    // the agent id that self-reported the incident
	Summary  string `json:"summary"`            // one-line what happened
	Detail   string `json:"detail,omitempty"`   // fuller description / what was done about it
	Severity string `json:"severity,omitempty"` // the agent's own severity call (info|warn|error)
	At       string `json:"at,omitempty"`       // RFC3339 when recorded
}

// IntentConflictNote is one contradiction a reviewer flagged between the diff it
// reviewed and the verbatim ROOT INTENT it was shown (context only — a reviewer
// judges contradiction with the root, never its completeness). Unlike a blocker,
// a conflict does not enter the local fix loop: it pauses the unit and requests a
// ruling ONE tier up (Ruling is the decider's disposition — "proceed" or "fix").
// It is persisted on the run/quest record (see conductor.captureIntentConflicts).
type IntentConflictNote struct {
	Agent  string `json:"agent,omitempty"`  // the reviewer id that flagged the conflict
	Issue  string `json:"issue"`            // one line describing the contradiction with the root intent
	Ruling string `json:"ruling,omitempty"` // the decider's disposition one tier up (proceed|fix)
	At     string `json:"at,omitempty"`     // RFC3339 when recorded
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
	// QuestID is the parent link for quest grouping. It stays empty for standalone
	// runs; a quest-owned child run carries its owning quest's id.
	QuestID string `json:"questId,omitempty"`
	// Deliver is how the run ships its work: "pr" (the default — open one PR per
	// impacted repo) or "branch" (a quest-owned child run — commit + push onto the
	// shared quest branch (quest/<id> — the same name in each impacted repo) and open
	// NO PR; the quest opens the PR at the end at terminal). Empty == "pr" (a
	// standalone run). A branch-delivered run's branch is that quest branch (set by
	// the quest at launch).
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
	// ResumeAt is set when the run auto-paused on a Claude usage limit: the RFC3339
	// time the limit resets, after which the run re-arms and continues on its own. It
	// is the marker that distinguishes a NON-TERMINAL limit pause (auto-resumes, no
	// postmortem) from a user stop (also "paused", but with no ResumeAt). Cleared the
	// moment the run resumes. Persisted so a restart can re-arm the conductor-wide gate
	// from a paused run's reset time (see conductor.tracked).
	ResumeAt string `json:"resumeAt,omitempty"`
	// PauseReason is the human-readable cause of a non-terminal auto-pause, mirroring
	// Quest. Two forms the UI distinguishes: "usage limit — auto-resume at <t>"
	// (a usage/session limit) and "connection lost — retrying" (a network/infra death).
	// Cleared when the run resumes. Empty for a user stop.
	PauseReason string `json:"pauseReason,omitempty"`
	// RePauses counts how many times this run auto-paused on a usage limit OR a
	// connection loss. Such a pause is UNBOUNDED — it never counts against an agent's
	// bounded attempt budget — so this is telemetry, not a cap.
	RePauses     int     `json:"rePauses,omitempty"`
	PrURL        string  `json:"prUrl,omitempty"` // the primary PR (folders[0]); first opened — kept for back-compat
	PRs          []PR    `json:"prs,omitempty"`   // one per impacted repo (multi-repo runs); PrURL mirrors the first
	TokensUsed   int     `json:"tokensUsed"`
	TokensBudget int     `json:"tokensBudget"`
	CostUsd      float64 `json:"costUsd"`
	TasksGreen   int     `json:"tasksGreen"`
	TasksTotal   int     `json:"tasksTotal"`
	HasDag       bool    `json:"hasDag"`
	Agents       []Agent `json:"agents"`
	Tasks        []Task  `json:"tasks"`
	Executor     string  `json:"executor"` // always "claude" — runs are only ever driven by real headless Claude Code
	// L2 telemetry (persisted, API-readable so /learn mines structure not logs):
	// ReviewRounds is how many review→fix→re-review rounds the run ran; Escalations
	// is the recorded upward decision-escalation audit trail; Postmortem is the
	// schema-valid explanation attached when the run terminates blocked (§3).
	ReviewRounds int          `json:"reviewRounds,omitempty"`
	Escalations  []Escalation `json:"escalations,omitempty"`
	Postmortem   *Postmortem  `json:"postmortem,omitempty"`
	// Incidents is the self-acknowledged-incident audit trail: the non-terminal
	// incidents this run's agents voluntarily reported (INCIDENT lines). Persisted so
	// the mining data-access path surfaces them alongside escalations/postmortems.
	Incidents []IncidentNote `json:"incidents,omitempty"`
	// IntentConflicts is the audit trail of contradictions a reviewer flagged
	// against the verbatim root intent (two-layer intent briefing). A standalone
	// run cannot emit conflicts (its task and root layers coincide → the channel is
	// disarmed by the render rule), so this is empty for standalone runs.
	IntentConflicts []IntentConflictNote `json:"intentConflicts,omitempty"`
	// Watch is the babysit post-delivery watch-phase state, present only on a
	// "babysit"-delivery run and only once its PR is open. It carries the watched
	// PR, the watch lifecycle state, the terminal outcome, and the tick log.
	Watch *WatchState `json:"watch,omitempty"`
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
	// Empty == "pr". Mirrors the same fields on QuestSpec so a standalone run
	// (POST /api/runs) can address PR feedback too, not only quest children.
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

// Delivery is how a quest's child runs ship their work. A perFinding quest opens
// a PR per child run ("pr"); a converge quest commits onto the shared quest branch
// ("branch") derived as quest/<id> — the same name in each impacted repo
// (NOT a scalar branch name — settled decision). The derivation lives in
// conductor.QuestBranch.
type Delivery string

const (
	DeliverPR     Delivery = "pr"     // perFinding quest: one PR per child run
	DeliverBranch Delivery = "branch" // converge quest: commit onto quest/<id>
	// DeliverFeedback updates an EXISTING PR in place: the run bases its work on
	// that PR's head branch and pushes back onto it, opening NO new PR. The target
	// PR number rides on TargetPR. Multi-repo: each repo's findings land on that
	// repo's existing PR.
	DeliverFeedback Delivery = "feedback"
	// DeliverReview produces findings and opens NO PR. When it had findings to apply
	// it behaves like feedback (updates TargetPR); when it had none it ends as a
	// review-only no-op with an empty prUrl by design.
	DeliverReview Delivery = "review"
	// DeliverBabysit opens a PR exactly like "pr" and THEN enters a post-delivery
	// watch phase: the run watches the PR it opened on an interval, dispatches a
	// feedback fix when changes are requested, and merges the PR once an approval
	// lands on its latest commit — a terminating loop whose exit is the merge. The
	// watch-phase state rides on Run.Watch. It requires no TargetPR (the run opens
	// and then owns the PR it watches).
	DeliverBabysit Delivery = "babysit"
)

// WatchDecision is the action a single babysit watch tick concluded to take on the
// PR it is watching, derived purely from the PR's current review state.
type WatchDecision string

const (
	// WatchWait — nothing actionable yet (review still pending, or the only approval
	// is on a stale commit). Keep watching.
	WatchWait WatchDecision = "wait"
	// WatchFeedback — changes were requested. Dispatch a feedback fix run against the
	// PR's head (once per head commit — not re-dispatched while that fix is in flight).
	WatchFeedback WatchDecision = "feedback"
	// WatchMerge — an approval landed on the PR's latest commit. Merge and exit.
	WatchMerge WatchDecision = "merge"
	// WatchDone — the PR is already merged or closed upstream. Exit.
	WatchDone WatchDecision = "done"
)

// PRReview is the snapshot of a watched PR's review state a watch tick reasons over.
// It is the small, provider-agnostic shape ghPRReview populates from gh (and tests
// supply directly), so the decision logic (decideWatch) is pure and unit-testable.
type PRReview struct {
	State          string `json:"state"`          // OPEN|MERGED|CLOSED (upstream PR state)
	ReviewDecision string `json:"reviewDecision"` // APPROVED|CHANGES_REQUESTED|REVIEW_REQUIRED|"" (none yet)
	HeadSHA        string `json:"headSha"`        // the PR's latest commit
	ApprovedSHA    string `json:"approvedSha"`    // the commit the latest APPROVED review was on ("" if none)
}

// WatchTick is one iteration of the babysit watch loop: the PR head it observed,
// the decision it reached, a human-readable detail, and the feedback child run it
// launched (when the decision was feedback). Accumulated on WatchState.Ticks so the
// dashboard can render the watch-phase tick log.
type WatchTick struct {
	ID         string        `json:"id"`
	At         string        `json:"at"` // RFC3339 when the tick ran
	HeadSHA    string        `json:"headSha,omitempty"`
	Decision   WatchDecision `json:"decision"`
	Detail     string        `json:"detail,omitempty"`
	ChildRunID string        `json:"childRunId,omitempty"` // the feedback run launched, when one was
}

// WatchState is the babysit post-delivery watch phase's persisted state, attached to
// Run.Watch once the run's PR is open. It carries the PR being watched, the current
// lifecycle State (watching|merged|stopped|blocked), the terminal Outcome, and the
// tick log. LastFeedbackSHA records the head commit a feedback fix was last
// dispatched for, so a changes-requested review isn't re-dispatched every tick while
// that fix is still in flight.
type WatchState struct {
	PR              int         `json:"pr"`
	Repo            string      `json:"repo,omitempty"`
	PRUrl           string      `json:"prUrl,omitempty"`
	State           string      `json:"state"`             // watching|merged|stopped|blocked
	Outcome         string      `json:"outcome,omitempty"` // human-readable terminal outcome
	LastFeedbackSHA string      `json:"lastFeedbackSha,omitempty"`
	Ticks           []WatchTick `json:"ticks,omitempty"`
	StartedAt       string      `json:"startedAt,omitempty"`
	UpdatedAt       string      `json:"updatedAt,omitempty"`
}

// Convergence is a quest's delivery policy — orthogonal to Delivery. It decides
// how a quest's accepted findings become PRs:
//   - "converge" (the default, a BOUNDED quest): child runs commit onto a shared
//     per-quest branch quest/<id> (deliver: branch, no child PRs); when the quest
//     meets its objective (terminal) it opens ONE PR per impacted repo from that
//     branch.
//   - "perFinding" (an ADVENTURE, open-ended freeseeking): each accepted finding
//     is its own child run with deliver: pr — its own PR — and the loop is perpetual.
//
// EXCEPTION (convergence does not apply): a quest with a target PR
// (feedback/review) works that PR's head branch and opens no new PR.
type Convergence string

const (
	ConvergeConverge   Convergence = "converge"   // bounded: accumulate on quest/<id>, one PR per repo at terminal
	ConvergePerFinding Convergence = "perFinding" // adventure: a PR per accepted finding, perpetual
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
	Objective string `json:"objective"`
	// Title is an optional short display label. Empty at launch → CreateQuest
	// derives one from the objective (mirrors run.Spec.Title). The UI renders this
	// in title slots instead of the full multi-paragraph objective.
	Title   string   `json:"title,omitempty"`
	Folders []string `json:"folders,omitempty"` // target folders/repos (folders[0] = the git repo child runs branch/PR in)
	Scope   string   `json:"scope,omitempty"`   // human-readable bound on what work is in-scope
	// Convergence is the delivery policy: "converge" (bounded — the default; child
	// runs accumulate on quest/<id>, one PR per repo at terminal) or "perFinding"
	// (adventure — a PR per accepted finding, perpetual). Empty defaults to
	// "converge" at creation. It does not apply to feedback/review quests (see the
	// exception on Convergence).
	Convergence Convergence `json:"convergence,omitempty"`
	// Safety is the safety boundary: the files/areas a quest's child runs must not
	// touch (the quest-level analogue of a coder's fork-safe boundary).
	Safety      string   `json:"safety,omitempty"`
	Verify      []string `json:"verify,omitempty"`      // verification command(s) every child run must pass green
	Stop        string   `json:"stop,omitempty"`        // stop/pause criteria (when to halt the loop)
	TokenBudget int      `json:"tokenBudget,omitempty"` // cap on total tokens across all ticks/child runs
	// Deliver is "pr" (standalone) or "branch" (converge quest). Empty defaults to
	// "pr" at creation. When "branch", the branch is quest/<id> — the same name in
	// each impacted repo.
	Deliver Delivery `json:"deliver,omitempty"`
	// TargetPR is the existing PR number a "feedback"/"review" quest's child runs
	// update in place (required >0 for those modes; 0 for "pr"/"branch").
	TargetPR int `json:"targetPr,omitempty"`
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
	// closed by objective-met dedup (already delivered on the shared branch) — not a freshly executed completion
	Deduped bool `json:"deduped,omitempty"`
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
// stable id, the objective fields carried from the
// spec, lifecycle status, budget/delivery, the work items and ticks the
// loop accumulates, rollup counters for the dashboard, and the schema version. The
// tick loop that populates Ticks/WorkItems is a later phase — this is the model and
// its persistence only.
type Quest struct {
	ID string `json:"id"`
	// Title is a short display label the UI renders instead of the full objective.
	// Stamped at creation (spec.Title, else derived from the objective).
	Title string `json:"title,omitempty"`
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
	// surfaced-only no-op accounting). It is stamped when the quest reaches a
	// terminal/blocked state so the dashboard and CLI can name a no-op as such
	// rather than show an undifferentiated "done".
	Summary     string   `json:"summary,omitempty"`
	PauseReason string   `json:"pauseReason,omitempty"`
	Archived    bool     `json:"archived,omitempty"` // cleared from the dashboard; still kept in the Work history
	TokenBudget int      `json:"tokenBudget,omitempty"`
	TokensUsed  int      `json:"tokensUsed"`
	Deliver     Delivery `json:"deliver"`
	// Convergence is the quest's delivery policy: "converge" (bounded — child runs
	// accumulate on quest/<id>, one PR per repo opens at terminal) or "perFinding"
	// (adventure — a PR per accepted finding, perpetual). Stamped from the spec at
	// creation (defaulted to "converge"). Always serialized so the UI can key on it.
	Convergence Convergence `json:"convergence"`
	// TargetPR is the existing PR number "feedback"/"review" child runs update in
	// place (0 for "pr"/"branch"). Stamped from the spec at creation.
	TargetPR int `json:"targetPr,omitempty"`
	// StopReason records why/who stopped the quest (e.g. "manual stop from
	// dashboard"), persisted by StopQuest so a terminal quest names its stop cause
	// (q4 fix: q4 recorded none). Empty until the quest is stopped.
	StopReason string     `json:"stopReason,omitempty"`
	WorkItems  []WorkItem `json:"workItems"`
	Ticks      []Tick     `json:"ticks"`
	// PRs is a converge quest's TERMINAL delivery: one PR per impacted repo, opened
	// from quest/<id> when the quest meets its objective. A perFinding quest opens
	// PRs per child run (recorded on ticks), not here.
	PRs []PR `json:"prs,omitempty"`
	// Rollup fields for the dashboard, recomputed from WorkItems/Ticks by the loop.
	PRsOpened      int `json:"prsOpened"`
	ItemsCompleted int `json:"itemsCompleted"`
	ItemsDeduped   int `json:"itemsDeduped,omitempty"`
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
	// L2 telemetry: the recorded upward decision-escalation audit trail, and the
	// schema-valid postmortem attached when the quest terminates blocked (§3).
	Escalations []Escalation `json:"escalations,omitempty"`
	Postmortem  *Postmortem  `json:"postmortem,omitempty"`
	// Incidents is the self-acknowledged-incident audit trail this quest's agents
	// (its quest-lead and, routed by host id, its child runs) voluntarily reported.
	Incidents []IncidentNote `json:"incidents,omitempty"`
	// IntentConflicts is the audit trail of contradictions a reviewer flagged at the
	// quest delivery gate against the verbatim root intent (two-layer briefing).
	IntentConflicts []IntentConflictNote `json:"intentConflicts,omitempty"`
	// GateRounds is how many review→fix→re-review rounds the quest DELIVERY gate
	// consumed (cumulative across re-entries), the quest analogue of Run.ReviewRounds.
	GateRounds int `json:"gateRounds,omitempty"`
	// TraceVersion is the schema version of this Quest record, mirroring how a Run's
	// exported trace carries TraceVersion so a future store can detect/migrate.
	TraceVersion int `json:"traceVersion"`
}

// RunTrace is the normalized, exportable trace of a single run: the stored Run
// plus its Audit (when present) and the schema version, in a stable JSONL-friendly
// shape. It is shape-readiness for a later central store — it embeds the existing
// Run (stable IDs, parent links, agents, task graph, events, PRs, token/cost) and
// the Audit verbatim, adding nothing the UI doesn't already see except TraceVersion.
//
// REDACTION SEAM: before any future sync to a central store, sensitive payloads
// (e.g. Event.Text/Input, Run.Prompt/OriginalIntent, IntentConflictNote.Issue) must be redacted here. This
// is local export only today — no redaction is applied. Do NOT add a central
// store/sync from this struct; that is a separate, later phase.
type RunTrace struct {
	TraceVersion int    `json:"traceVersion"`
	Run          *Run   `json:"run"`
	Audit        *Audit `json:"audit,omitempty"`
}
