// Package conductor owns run lifecycle: it creates runs, drives them with an
// executor (real headless claude, or a deterministic scripted one when claude
// isn't available), and publishes every state change into ooo so the UI reads
// live data. There is no client-side mock — this is the single source of truth.
package conductor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
)

// Executor drives a run from planning to PR, calling Update to publish state.
type Executor interface {
	// Execute blocks until the run is done or the control channel says stop.
	// It must honor commands ("stop"|"resume"|"restart") on control.
	Execute(c *Conductor, id string, control <-chan string)
	Name() string
}

type runtime struct {
	mu      sync.Mutex
	r       run.Run
	control chan string
}

// Conductor is the orchestrator. Safe for concurrent use.
type Conductor struct {
	server    *ooo.Server
	mu        sync.Mutex
	runs      map[string]*runtime
	seq       int
	hasClaude bool
}

// New builds a conductor bound to an ooo server. The executor is chosen by the
// CANDYLAND_EXECUTOR env var ("claude" | "scripted"); otherwise it auto-detects
// whether the real headless `claude` CLI is on PATH and falls back to scripted.
func New(server *ooo.Server) *Conductor {
	hasClaude := false
	switch os.Getenv("CANDYLAND_EXECUTOR") {
	case "scripted":
		hasClaude = false
	case "claude":
		hasClaude = true
	default:
		_, err := exec.LookPath("claude")
		hasClaude = err == nil
	}
	return &Conductor{
		server:    server,
		runs:      map[string]*runtime{},
		hasClaude: hasClaude,
	}
}

// publish writes the run object to ooo (key runs/<id>); subscribers update live.
func (c *Conductor) publish(r run.Run) {
	if c.server == nil {
		return // tests construct a serverless conductor and read state via Get
	}
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	_, _ = c.server.Storage.Set("runs/"+r.ID, json.RawMessage(b))
}

// Update mutates a run under lock, recomputes derived fields, and publishes it.
// Executors call this to stream progress — it's the only write path.
func (c *Conductor) Update(id string, mutate func(*run.Run)) {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt == nil {
		return
	}
	rt.mu.Lock()
	mutate(&rt.r)
	recompute(&rt.r)
	snapshot := rt.r
	rt.mu.Unlock()
	c.publish(snapshot)
}

// Get returns a copy of the current run state.
func (c *Conductor) Get(id string) (run.Run, bool) {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt == nil {
		return run.Run{}, false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.r, true
}

// Create registers a new run (status: planning) and publishes it. The build
// executor is started later via Begin, after the UI's planning Q&A completes.
func (c *Conductor) Create(spec run.Spec) string {
	c.mu.Lock()
	c.seq++
	id := fmt.Sprintf("r%d", c.seq)
	c.mu.Unlock()

	r := run.Run{
		ID:           id,
		Title:        spec.Title,
		Prompt:       spec.Prompt,
		Mode:         spec.Mode,
		Workspace:    spec.Workspace,
		Branch:       "feat/" + slug(firstNonEmpty(spec.Title, spec.Prompt, "run")),
		Status:       "planning",
		Phase:        0,
		TokensBudget: 900,
		Tasks:        []run.Task{},
		Agents:       []run.Agent{},
		Executor:     "scripted",
	}
	if c.hasClaude {
		r.Executor = "claude"
	}
	rt := &runtime{r: r, control: make(chan string, 8)}
	c.mu.Lock()
	c.runs[id] = rt
	c.mu.Unlock()
	c.publish(r)
	return id
}

// Begin starts the build executor for a run once planning is done. The planning
// answers (if any) are folded into the prompt that drives the agents.
func (c *Conductor) Begin(id string, answers map[string]any) {
	c.mu.Lock()
	rt := c.runs[id]
	hasClaude := c.hasClaude
	c.mu.Unlock()
	if rt == nil {
		return
	}
	var ex Executor
	if hasClaude {
		ex = &ClaudeExecutor{}
	} else {
		ex = &ScriptedExecutor{}
	}
	extra := formatAnswers(answers)
	c.Update(id, func(r *run.Run) {
		r.Status = "running"
		r.Executor = ex.Name()
		if extra != "" {
			r.Prompt = strings.TrimSpace(r.Prompt + "\n\n" + extra)
		}
	})
	go ex.Execute(c, id, rt.control)
}

// formatAnswers renders planning answers as a readable addendum to the prompt.
func formatAnswers(answers map[string]any) string {
	if len(answers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(answers))
	for k, v := range answers {
		parts = append(parts, fmt.Sprintf("- %s: %v", k, v))
	}
	return "Planning answers:\n" + strings.Join(parts, "\n")
}

// Command forwards stop|resume|restart to the run's executor.
func (c *Conductor) Command(id, cmd string) bool {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt == nil {
		return false
	}
	select {
	case rt.control <- cmd:
		return true
	default:
		return false // executor not listening (e.g. already finished)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
