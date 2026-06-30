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
	T      string `json:"t"` // system|text|tool|test|result
	Text   string `json:"text,omitempty"`
	Name   string `json:"name,omitempty"`  // tool name
	Input  string `json:"input,omitempty"` // tool input summary
	Pass   int    `json:"pass,omitempty"`
	Fail   int    `json:"fail,omitempty"`
	TaskID string `json:"taskId,omitempty"` // task this event belongs to, when known (best-effort)
	Ts     string `json:"ts,omitempty"`     // RFC3339 timestamp set when the event is appended
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
	Prompt     string `json:"prompt"` // the instruction actually sent to the agents
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
