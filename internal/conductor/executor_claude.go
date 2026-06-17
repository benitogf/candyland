package conductor

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"github.com/benitogf/candyland/internal/run"
)

// ClaudeExecutor runs a REAL headless claude process for the run's prompt and
// streams its actual stream-json output into ooo. This is the production path
// (used whenever the `claude` CLI is on PATH). Stop kills the process and keeps
// the run controllable; Resume re-spawns with --resume <session_id> (captured
// from the init event); Restart kills and re-spawns fresh.
type ClaudeExecutor struct{}

func (e *ClaudeExecutor) Name() string { return "claude" }

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

type claudeProc struct {
	cancel  context.CancelFunc
	done    chan struct{}
	session *string
}

func (e *ClaudeExecutor) Execute(c *Conductor, id string, control <-chan string) {
	r, ok := c.Get(id)
	if !ok {
		return
	}
	prompt := r.Prompt
	session := "" // captured from the init event, used for --resume

	c.Update(id, func(r *run.Run) {
		r.Status = "running"
		r.Phase = 1
		r.Agents = []run.Agent{{
			ID: "tl", Role: "Agent", Emoji: "🧭", Task: "work the request",
			State: "working", Activity: "starting headless session", Budget: 800,
			Worktree: "(workspace)", Model: "opus-4-8",
			Events: []run.Event{{T: "system", Text: "claude -p · --output-format stream-json"}},
		}}
	})

	// start spawns a process; its own done channel avoids close-of-closed races.
	start := func(resume string) *claudeProc {
		ctx, cancel := context.WithCancel(context.Background())
		d := make(chan struct{})
		sess := &session
		go func() {
			runProcess(ctx, c, id, prompt, resume, sess)
			close(d)
		}()
		return &claudeProc{cancel: cancel, done: d, session: sess}
	}

	cur := start("")
	for {
		var doneCh <-chan struct{}
		if cur != nil {
			doneCh = cur.done
		}
		select {
		case cmd := <-control:
			switch cmd {
			case "stop":
				if cur != nil {
					cur.cancel()
				}
				c.Update(id, func(r *run.Run) { r.Status = "paused"; setAgentState(r, "blocked", "stopped") })
			case "resume":
				if cur == nil {
					c.Update(id, func(r *run.Run) { r.Status = "running"; setAgentState(r, "working", "resuming") })
					cur = start(session) // --resume <session_id> when we have one
				}
			case "restart":
				if cur != nil {
					cur.cancel()
				}
				c.Update(id, func(r *run.Run) { r.Status = "running"; setAgentState(r, "working", "restarting") })
				cur = start("")
			}
		case <-doneCh:
			cur = nil
			cr, _ := c.Get(id)
			if cr.Status == "paused" {
				continue // stay alive, controllable, awaiting resume/restart
			}
			c.Update(id, func(r *run.Run) {
				r.Status = "done"
				r.Phase = len(run.Phases) - 1
				r.Progress = 1
				setAgentState(r, "done", "finished")
			})
			return
		}
	}
}

func setAgentState(r *run.Run, state, activity string) {
	if len(r.Agents) > 0 {
		r.Agents[0].State = state
		r.Agents[0].Activity = activity
	}
}

func appendEvent(r *run.Run, e run.Event, addTokens int) {
	if len(r.Agents) == 0 {
		return
	}
	r.Agents[0].Events = append(r.Agents[0].Events, e)
	r.Agents[0].Tokens += addTokens
}

// runProcess spawns claude headless and maps each stream-json line into ooo.
func runProcess(ctx context.Context, c *Conductor, id, prompt, resume string, sess *string) {
	if strings.TrimSpace(prompt) == "" {
		prompt = "Describe the requested change."
	}
	args := []string{}
	if resume != "" {
		args = append(args, "--resume", resume)
	}
	args = append(args, "-p", prompt, "--output-format", "stream-json", "--verbose", "--model", "claude-opus-4-8")
	cmd := exec.CommandContext(ctx, "claude", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.Update(id, func(r *run.Run) { appendEvent(r, run.Event{T: "text", Text: "failed to start: " + err.Error()}, 0) })
		return
	}
	if err := cmd.Start(); err != nil {
		c.Update(id, func(r *run.Run) { appendEvent(r, run.Event{T: "text", Text: "failed to start: " + err.Error()}, 0) })
		return
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		var line streamLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.SessionID != "" {
			*sess = line.SessionID
		}
		mapLine(c, id, line)
	}
	_ = cmd.Wait()
}

func mapLine(c *Conductor, id string, line streamLine) {
	switch line.Type {
	case "assistant":
		for _, blk := range line.Message.Content {
			b := blk
			if b.Type == "text" && b.Text != "" {
				c.Update(id, func(r *run.Run) { appendEvent(r, run.Event{T: "text", Text: b.Text}, 0) })
			}
			if b.Type == "tool_use" {
				c.Update(id, func(r *run.Run) {
					appendEvent(r, run.Event{T: "tool", Name: b.Name, Input: truncate(string(b.Input), 200)}, 0)
				})
			}
		}
	case "user":
		c.Update(id, func(r *run.Run) { appendEvent(r, run.Event{T: "result", Text: "tool result"}, 0) })
	case "result":
		l := line
		c.Update(id, func(r *run.Run) {
			appendEvent(r, run.Event{T: "result", Text: truncate(l.Result, 300)}, l.Usage.OutputTokens/1000)
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
