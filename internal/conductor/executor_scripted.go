package conductor

import (
	"fmt"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// ScriptedExecutor drives a run through a deterministic timeline, publishing
// live state into ooo on a fixed clock. It's the fallback when headless claude
// isn't available — the UI still reads genuine, moving ooo state (no client mock).

const scriptDuration = 24.0 // seconds, planning-done → PR
const scriptTick = 250 * time.Millisecond

var scriptPhaseAt = []float64{0, 3, 16, 19, 22}

type frame struct {
	at        float64
	state     string
	activity  string
	addTokens int
	event     *run.Event
}

type agentScript struct {
	id, role, emoji, task, worktree, model string
	budget                                 int
	spawnAt                                float64
	frames                                 []frame
}

type taskScript struct {
	id, title, test, owner string
	files, deps            []string
	frames                 []frame
}

func ev(t, text string) *run.Event { return &run.Event{T: t, Text: text} }

var scriptAgents = []agentScript{
	{id: "tl", role: "Tech lead", emoji: "🧭", task: "partition · integrate · deliver", worktree: "(main checkout)", model: "opus-4-8", budget: 200, spawnAt: 0, frames: []frame{
		{at: 0, state: "working", activity: "planning the approach", addTokens: 8, event: ev("system", "tech-lead · main checkout · model opus-4-8")},
		{at: 3, activity: "partitioning into fork-safe tasks", addTokens: 10, event: &run.Event{T: "emit", Text: "partition: 4 tasks · disjoint files · 0 cross-deps", Detail: "tests · endpoint · button · review"}},
		{at: 16, state: "integrating", activity: "integrating the slices", addTokens: 9, event: ev("text", "Both slices green. Merging sequentially and re-running the full suite.")},
		{at: 17, addTokens: 0, event: &run.Event{T: "test", Text: "full suite", Pass: 41, Fail: 0}},
		{at: 19, state: "done", activity: "integrated · handed to review", addTokens: 11, event: ev("result", "integrated · 38.4k tokens · $0.46")},
	}},
	{id: "te", role: "Test eng", emoji: "🧪", task: "write the failing tests", worktree: "wt/tests", model: "opus-4-8", budget: 200, spawnAt: 3, frames: []frame{
		{at: 3, state: "working", activity: "writing the failing tests", addTokens: 6, event: ev("system", "coder-test-engineer · wt/tests · model opus-4-8")},
		{at: 4, addTokens: 9, event: &run.Event{T: "tool", Name: "Write", Input: "api/export.test.js"}},
		{at: 5, event: &run.Event{T: "test", Text: "npm test export", Pass: 0, Fail: 4, Note: "red as expected — the contract"}},
		{at: 6, state: "done", activity: "tests written — the contract", addTokens: 8, event: ev("result", "done · 31.2k tokens · $0.37")},
	}},
	{id: "be", role: "Backend", emoji: "⚙️", task: "export endpoint → CSV", worktree: "wt/export-endpoint", model: "opus-4-8", budget: 200, spawnAt: 5, frames: []frame{
		{at: 5, state: "working", activity: "implementing the export endpoint", addTokens: 7, event: ev("system", "coder-backend · wt/export-endpoint · model opus-4-8")},
		{at: 8, addTokens: 18, event: &run.Event{T: "tool", Name: "Edit", Input: "api/reports.go (+64 -1)"}},
		{at: 10, event: &run.Event{T: "test", Text: "go test ./api/...", Pass: 12, Fail: 0}},
		{at: 11, state: "green", activity: "endpoint complete — 12/12 green", addTokens: 16, event: ev("result", "green · 47.0k tokens · $0.58")},
	}},
	{id: "fe", role: "Frontend", emoji: "🎨", task: "export button → download", worktree: "wt/export-button", model: "opus-4-8", budget: 200, spawnAt: 5, frames: []frame{
		{at: 5, state: "working", activity: "wiring the export button", addTokens: 6, event: ev("system", "coder-frontend · wt/export-button · model opus-4-8")},
		{at: 6, addTokens: 10, event: ev("text", "Reading the failing test to understand the contract before touching component code.")},
		{at: 11, addTokens: 9, event: &run.Event{T: "tool", Name: "Edit", Input: "components/ReportsToolbar.js (+18 -2)"}},
		{at: 13, event: &run.Event{T: "test", Text: "npm test ReportsToolbar", Pass: 3, Fail: 1, Note: "filename assertion still red"}},
		{at: 16, state: "green", activity: "export button complete — 4/4 green", addTokens: 12, event: ev("result", "green · 52.0k tokens · $0.63")},
	}},
	{id: "rv", role: "Reviewer /gh", emoji: "🔎", task: "self-review · open one PR", worktree: "(main checkout)", model: "opus-4-8", budget: 120, spawnAt: 19, frames: []frame{
		{at: 19, state: "working", activity: "self-reviewing the integrated diff", addTokens: 5, event: ev("system", "gh-self-review · main checkout · model opus-4-8")},
		{at: 22, state: "done", activity: "review clean · PR opened", addTokens: 6, event: ev("result", "PR opened · 21.0k tokens · $0.25")},
	}},
}

var scriptTasks = []taskScript{
	{id: "tests", title: "Define failing tests", test: "—", owner: "te", files: []string{"api/export.test.js", "components/ReportsToolbar.test.js"}, frames: []frame{{at: 3, state: "working"}, {at: 6, state: "done"}}},
	{id: "endpoint", title: "Export endpoint → CSV", test: "api/export.test.js", owner: "be", files: []string{"api/reports.go"}, deps: []string{"tests"}, frames: []frame{{at: 5, state: "working"}, {at: 11, state: "green"}}},
	{id: "button", title: "Export button → download", test: "components/ReportsToolbar.test.js", owner: "fe", files: []string{"components/ReportsToolbar.js"}, deps: []string{"tests"}, frames: []frame{{at: 5, state: "working"}, {at: 16, state: "green"}}},
	{id: "integrate", title: "Integrate + self-review", test: "full suite", owner: "tl", files: []string{"(all slices)"}, deps: []string{"endpoint", "button"}, frames: []frame{{at: 16, state: "integrating"}, {at: 19, state: "done"}}},
	{id: "review", title: "Open one PR", test: "—", owner: "rv", files: []string{"—"}, deps: []string{"integrate"}, frames: []frame{{at: 19, state: "working"}, {at: 22, state: "done"}}},
}

var scriptStatusLines = []struct {
	at   float64
	text string
}{
	{0, "Understanding your request and confirming a couple of details."},
	{3, "Breaking the work into pieces and starting to build."},
	{6, "Building the export — the server part and the button — in parallel."},
	{16, "Fitting the pieces together and double-checking everything works."},
	{19, "Reviewing the finished change before handing it to you."},
	{22, "Done — your change is ready to review."},
}

func lastString(frames []frame, t float64, pick func(frame) string) string {
	v := ""
	for _, f := range frames {
		if f.at > t {
			break
		}
		if s := pick(f); s != "" {
			v = s
		}
	}
	return v
}

func fmtElapsed(s float64) string {
	sec := int(s)
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%dm %02ds", sec/60, sec%60)
}

func phaseAt(t float64) int {
	p := 0
	for i, at := range scriptPhaseAt {
		if t >= at {
			p = i
		}
	}
	return p
}

func buildState(r *run.Run, t float64) {
	agents := []run.Agent{}
	for _, a := range scriptAgents {
		if t < a.spawnAt {
			continue
		}
		ag := run.Agent{ID: a.id, Role: a.role, Emoji: a.emoji, Task: a.task, Budget: a.budget, Worktree: a.worktree, Model: a.model, Elapsed: fmtElapsed(t - a.spawnAt)}
		ag.State = lastString(a.frames, t, func(f frame) string { return f.state })
		if ag.State == "" {
			ag.State = "working"
		}
		ag.Activity = lastString(a.frames, t, func(f frame) string { return f.activity })
		for _, f := range a.frames {
			if f.at > t {
				break
			}
			ag.Tokens += f.addTokens
			if f.event != nil {
				ag.Events = append(ag.Events, *f.event)
			}
		}
		agents = append(agents, ag)
	}
	tasks := []run.Task{}
	for _, ts := range scriptTasks {
		st := lastString(ts.frames, t, func(f frame) string { return f.state })
		if st == "" {
			st = "idle"
		}
		tasks = append(tasks, run.Task{ID: ts.id, Title: ts.title, Files: ts.files, Test: ts.test, Owner: ts.owner, State: st, Deps: ts.deps})
	}
	r.Agents = agents
	r.Tasks = tasks
	r.HasDag = true // the scripted run has a real fork-safe partition DAG
	r.Phase = phaseAt(t)
	r.Progress = t / scriptDuration
	if r.Progress > 1 {
		r.Progress = 1
	}
	for _, sl := range scriptStatusLines {
		if sl.at <= t {
			r.StatusLine = sl.text
		}
	}
	if t >= 22 {
		r.PrURL = "acme/app/pull/231"
	}
}

func (e *ScriptedExecutor) Name() string { return "scripted" }

// ScriptedExecutor is the deterministic fallback driver.
type ScriptedExecutor struct{}

func (e *ScriptedExecutor) Execute(c *Conductor, id string, control <-chan string) {
	elapsed := 0.0
	running := true
	ticker := time.NewTicker(scriptTick)
	defer ticker.Stop()
	for {
		select {
		case cmd := <-control:
			switch cmd {
			case "stop":
				running = false
				c.Update(id, func(r *run.Run) { r.Status = "paused" })
			case "resume":
				running = true
				c.Update(id, func(r *run.Run) { r.Status = "running" })
			case "restart":
				elapsed = 0
				running = true
				c.Update(id, func(r *run.Run) { r.Status = "running" })
			}
		case <-ticker.C:
			if !running {
				continue
			}
			elapsed += scriptTick.Seconds()
			done := elapsed >= scriptDuration
			c.Update(id, func(r *run.Run) {
				buildState(r, elapsed)
				if done {
					r.Status = "done"
				}
			})
			if done {
				return // run complete — exit the goroutine (no leak)
			}
		}
	}
}
