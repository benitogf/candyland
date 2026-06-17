package conductor

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/benitogf/candyland/internal/run"
)

// ClaudeExecutor runs REAL headless claude processes and streams their actual
// stream-json output into ooo. The tech lead runs first and emits a structured
// PARTITION (per the detritus roles/tech-lead convention); the conductor parses
// it, writes the task DAG, and spawns ONE coder process per fork-safe task in
// parallel — each coder's stream-json aggregates into its own agent. Stop kills
// every process (and keeps the run controllable); Restart re-runs.
type ClaudeExecutor struct{}

func (e *ClaudeExecutor) Name() string { return "claude" }

// claudeBin is the binary spawned; overridable for tests via CANDYLAND_CLAUDE.
func claudeBin() string {
	if b := os.Getenv("CANDYLAND_CLAUDE"); b != "" {
		return b
	}
	return "claude"
}

// streamLine is the subset of Claude Code's --output-format stream-json we map.
type streamLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Message   struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	Result string `json:"result"`
	Usage  struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// partitionTask is the shape the tech lead emits on a `PARTITION <json>` line.
type partitionTask struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Role  string   `json:"role"`
	Emoji string   `json:"emoji"`
	Files []string `json:"files"`
	Test  string   `json:"test"`
	Deps  []string `json:"deps"`
}

func (e *ClaudeExecutor) Execute(c *Conductor, id string, control <-chan string) {
	r, ok := c.Get(id)
	if !ok {
		return
	}
	prompt := r.Prompt

	run1 := func(ctx context.Context) chan struct{} {
		done := make(chan struct{})
		go func() { fanOut(ctx, c, id, prompt); close(done) }()
		return done
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := run1(ctx)
	for {
		select {
		case cmd := <-control:
			switch cmd {
			case "stop":
				cancel()
				c.Update(id, func(r *run.Run) { r.Status = "paused" })
			case "restart":
				cancel()
				ctx, cancel = context.WithCancel(context.Background())
				c.Update(id, func(r *run.Run) { r.Status = "running" })
				done = run1(ctx)
			}
		case <-done:
			cr, _ := c.Get(id)
			if cr.Status == "paused" {
				continue // controllable; await resume/restart
			}
			c.Update(id, func(r *run.Run) {
				r.Status = "done"
				r.Phase = len(run.Phases) - 1
				r.Progress = 1
			})
			cancel()
			return
		}
	}
}

// fanOut runs the tech lead, parses its partition, then runs coders in parallel.
func fanOut(ctx context.Context, c *Conductor, id, prompt string) {
	c.Update(id, func(r *run.Run) {
		r.Status = "running"
		r.Phase = 1
		r.Agents = []run.Agent{{ID: "tl", Role: "Tech lead", Emoji: "🧭", Task: "partition · integrate · deliver",
			State: "working", Activity: "planning the partition", Budget: 800, Worktree: "(main checkout)", Model: "opus-4-8",
			Events: []run.Event{{T: "system", Text: "tech-lead · claude -p --output-format stream-json"}}}}
		r.Tasks = []run.Task{}
	})

	tasks := runAgentProcess(ctx, c, id, "tl", techLeadPrompt(prompt))
	if len(tasks) == 0 || ctx.Err() != nil {
		return // no partition emitted (or stopped) — single-agent run
	}

	// Write the partition DAG and spawn one coder per task, in parallel.
	c.Update(id, func(r *run.Run) {
		r.HasDag = true
		r.Tasks = make([]run.Task, 0, len(tasks))
		for _, t := range tasks {
			r.Tasks = append(r.Tasks, run.Task{ID: t.ID, Title: t.Title, Files: t.Files, Test: t.Test, Owner: t.ID, State: "working", Deps: t.Deps})
			r.Agents = append(r.Agents, run.Agent{ID: t.ID, Role: orDefault(t.Role, "Coder"), Emoji: orDefault(t.Emoji, "⚙️"), Task: t.Title,
				State: "working", Activity: "implementing " + t.Title, Budget: 200, Worktree: "wt/" + t.ID, Model: "opus-4-8"})
		}
		setAgentState(r, "tl", "integrating", "coordinating coders")
	})

	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func(t partitionTask) {
			defer wg.Done()
			runAgentProcess(ctx, c, id, t.ID, coderPrompt(t))
			c.Update(id, func(r *run.Run) {
				setAgentState(r, t.ID, "green", "done")
				setTaskState(r, t.ID, "green")
			})
		}(t)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	c.Update(id, func(r *run.Run) {
		r.Phase = len(run.Phases) - 2 // Review
		setAgentState(r, "tl", "done", "integrated")
	})
}

func techLeadPrompt(prompt string) string {
	return "You are the tech lead. First, emit exactly one line beginning with `PARTITION ` " +
		"followed by a JSON array of fork-safe tasks: " +
		`[{"id","title","role","emoji","files":[],"test","deps":[]}]. ` +
		"Then stop. Request:\n\n" + prompt
}

func coderPrompt(t partitionTask) string {
	return "Implement this fork-safe task until its defining test is green: " + t.Title +
		". Files: " + strings.Join(t.Files, ", ") + ". Test: " + t.Test
}

// runAgentProcess spawns a claude process, maps each stream-json line to the
// given agent, and returns any partition parsed from a `PARTITION <json>` line.
func runAgentProcess(ctx context.Context, c *Conductor, id, agentID, prompt string) []partitionTask {
	cmd := exec.CommandContext(ctx, claudeBin(), "-p", prompt, "--output-format", "stream-json", "--verbose", "--model", "claude-opus-4-8")
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		c.Update(id, func(r *run.Run) { appendToAgent(r, agentID, run.Event{T: "text", Text: "failed to start"}, 0) })
		return nil
	}
	var partition []partitionTask
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		var line streamLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if p := mapAgentLine(c, id, agentID, line); p != nil {
			partition = p
		}
	}
	_ = cmd.Wait()
	return partition
}

func mapAgentLine(c *Conductor, id, agentID string, line streamLine) []partitionTask {
	var parsed []partitionTask
	switch line.Type {
	case "assistant":
		for _, blk := range line.Message.Content {
			b := blk
			if b.Type == "text" && b.Text != "" {
				if p := parsePartition(b.Text); p != nil {
					parsed = p
				}
				c.Update(id, func(r *run.Run) { appendToAgent(r, agentID, run.Event{T: "text", Text: b.Text}, 0) })
			}
			if b.Type == "tool_use" {
				c.Update(id, func(r *run.Run) {
					appendToAgent(r, agentID, run.Event{T: "tool", Name: b.Name, Input: truncate(string(b.Input), 200)}, 0)
				})
			}
		}
	case "result":
		l := line
		c.Update(id, func(r *run.Run) {
			appendToAgent(r, agentID, run.Event{T: "result", Text: truncate(l.Result, 300)}, l.Usage.OutputTokens/1000)
		})
	}
	return parsed
}

// parsePartition extracts the task array from a `PARTITION <json>` line.
func parsePartition(text string) []partitionTask {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "PARTITION ") {
			continue
		}
		var tasks []partitionTask
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "PARTITION ")), &tasks) == nil && len(tasks) > 0 {
			return tasks
		}
	}
	return nil
}

func setAgentState(r *run.Run, agentID, state, activity string) {
	for i := range r.Agents {
		if r.Agents[i].ID == agentID {
			r.Agents[i].State = state
			r.Agents[i].Activity = activity
			return
		}
	}
}

func setTaskState(r *run.Run, taskID, state string) {
	for i := range r.Tasks {
		if r.Tasks[i].ID == taskID {
			r.Tasks[i].State = state
			return
		}
	}
}

func appendToAgent(r *run.Run, agentID string, e run.Event, addTokens int) {
	for i := range r.Agents {
		if r.Agents[i].ID == agentID {
			r.Agents[i].Events = append(r.Agents[i].Events, e)
			r.Agents[i].Tokens += addTokens
			return
		}
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
