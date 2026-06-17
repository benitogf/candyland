// Package run defines the run/agent/task data model that flows through ooo to
// the React app. The JSON shape matches exactly what the dashboard panels
// consume, so the UI reads live ooo state with no client-side mock.
package run

// Event is one parsed stream-json line from an agent process.
type Event struct {
	T      string `json:"t"` // system|text|tool|emit|test|result|cursor
	Text   string `json:"text,omitempty"`
	Name   string `json:"name,omitempty"`   // tool name
	Input  string `json:"input,omitempty"`  // tool input summary
	Detail string `json:"detail,omitempty"` // emit detail
	Pass   int    `json:"pass,omitempty"`
	Fail   int    `json:"fail,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Agent is one spawned worker (a headless claude process).
type Agent struct {
	ID       string  `json:"id"`
	Role     string  `json:"role"`
	Emoji    string  `json:"emoji"`
	Task     string  `json:"task"`
	State    string  `json:"state"` // idle|working|blocked|integrating|green|done
	Activity string  `json:"activity"`
	Tokens   int     `json:"tokens"`
	Budget   int     `json:"budget"`
	Worktree string  `json:"worktree"`
	Model    string  `json:"model"`
	Elapsed  string  `json:"elapsed"`
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

// Run is the full state of a run — the object stored at ooo key runs/<id>.
type Run struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`  // optional; UI derives a label when empty
	Prompt       string  `json:"prompt"` // the instruction actually sent to the agents
	Branch       string  `json:"branch"`
	Mode         string  `json:"mode"`      // developer|non-developer
	Workspace    string  `json:"workspace"` // workspace id
	Status       string  `json:"status"`    // planning|running|paused|done
	Phase        int     `json:"phase"`     // index into Plan..PR
	Progress     float64 `json:"progress"`  // 0..1
	StatusLine   string  `json:"statusLine,omitempty"`
	PrURL        string  `json:"prUrl,omitempty"`
	TokensUsed   int     `json:"tokensUsed"`
	TokensBudget int     `json:"tokensBudget"`
	CostUsd      float64 `json:"costUsd"`
	TasksGreen   int     `json:"tasksGreen"`
	TasksTotal   int     `json:"tasksTotal"`
	HasDag       bool    `json:"hasDag"`
	Agents       []Agent `json:"agents"`
	Tasks        []Task  `json:"tasks"`
	Executor     string  `json:"executor"` // "claude" or "scripted" — honest about what's driving it
}

// Spec is the create-run request from the wizard.
type Spec struct {
	Mode      string `json:"mode"`
	Workspace string `json:"workspace"`
	Prompt    string `json:"prompt"`
	Title     string `json:"title"`
}

// Phases are the lifecycle stages shown in the stepper.
var Phases = []string{"Plan", "Build", "Integrate", "Review", "PR"}
