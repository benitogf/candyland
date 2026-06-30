package conductor

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/candyland/internal/run"
)

// Planning questions are GENERATED from the run's actual prompt by a real Claude
// call — there is no canned set. If Claude is unavailable or returns nothing
// usable, GenerateQuestions returns an empty slice so the UI falls straight
// through to the build (never a fake question).

func questionTimeout() time.Duration { return envDur("CANDYLAND_QUESTION_MS", 60*1000) }

// GenerateQuestions asks Claude for a few clarifying questions tailored to the
// run's prompt, and caches them on the run — so a refresh or retry
// reuses the same questions (one Claude call per run) instead of regenerating a
// different set each time. Returns nil on any failure — the planning step then
// proceeds directly to the build.
func (c *Conductor) GenerateQuestions(id string) []run.Question {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	if rt.questionsDone {
		qs := rt.questions
		rt.mu.Unlock()
		return qs
	}
	prompt := strings.TrimSpace(rt.r.Prompt)
	rt.mu.Unlock()
	if prompt == "" {
		return c.cacheQuestions(rt, nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), questionTimeout())
	defer cancel()

	// The request rides in a brief (fetched via brief_get), not on argv — r.Prompt
	// can be the full settled plan, which would overflow the command line. The
	// prompt below is a constant bootstrap. Without a bus (serverless tests) there
	// is no brief; the planner then has only the bootstrap and returns no usable
	// questions, which the caller already treats as "proceed to build".
	const plannerID = "planner"
	c.putBrief(plannerID, bus.Brief{Role: "planner", Prompt: prompt})
	args := []string{"-p", plannerBootstrap(), "--output-format", "json", "--model", "claude-opus-4-8"}
	busCfg := c.busMCPConfig(id, plannerID)
	if busCfg != "" {
		args = append(args, "--mcp-config", busCfg)
	}
	cmd := exec.CommandContext(ctx, claudeBin(), args...)
	cmd.Env = claudeEnv()
	if busCfg != "" {
		cmd.Env = append(cmd.Env, "CANDYLAND_BUS_ADDR="+c.server.Address, "CANDYLAND_AGENT_ID="+plannerID)
	}
	configureProc(cmd) // no flashing console window on Windows
	out, err := cmd.Output()
	var qs []run.Question
	if err == nil {
		qs = parseQuestions(out)
	}
	return c.cacheQuestions(rt, qs)
}

// cacheQuestions stores the generated questions once; if a concurrent call cached
// first, its result wins so every caller sees the same set.
func (c *Conductor) cacheQuestions(rt *runtime, qs []run.Question) []run.Question {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.questionsDone {
		return rt.questions
	}
	rt.questions = qs
	rt.questionsDone = true
	return qs
}

// plannerBootstrap is the CONSTANT prompt for the clarifying-questions call. The
// request rides in the planner's brief (fetched via brief_get), not here — so a
// large settled plan never reaches argv. Keeps the "clarifying questions"
// discriminator the stub tests key on.
func plannerBootstrap() string {
	const audience = "a developer"
	const style = "Open-ended questions are fine — omit `options` and give a short `placeholder` example answer instead."
	return "You are planning a software task before any code is written. Call the brief_get tool FIRST to read the request (" + audience + " asked for it) — it is no longer on your command line. " +
		"Produce 2 to 4 brief clarifying questions that would most help decide what to build. " + style + " " +
		"Return ONLY a JSON array, no prose, no code fence: " +
		`[{"id":"short-kebab-key","question":"...","options":["..."],"multi":false,"placeholder":"..."}]. ` +
		"Each question must be specific to the request — not generic."
}

// parseQuestions pulls the question array out of `claude --output-format json`
// output: a JSON object whose `result` field holds the model's text, which holds
// the JSON array (possibly wrapped in prose or a code fence we tolerate).
func parseQuestions(out []byte) []run.Question {
	var wrap struct {
		Result string `json:"result"`
	}
	text := string(out)
	if json.Unmarshal(out, &wrap) == nil && wrap.Result != "" {
		text = wrap.Result
	}
	start := strings.IndexByte(text, '[')
	end := strings.LastIndexByte(text, ']')
	if start < 0 || end <= start {
		return nil
	}
	var qs []run.Question
	if json.Unmarshal([]byte(text[start:end+1]), &qs) != nil {
		return nil
	}
	out2 := make([]run.Question, 0, len(qs))
	for i, q := range qs {
		if strings.TrimSpace(q.Question) == "" {
			continue
		}
		if strings.TrimSpace(q.ID) == "" {
			q.ID = "q" + strconv.Itoa(i+1)
		}
		out2 = append(out2, q)
		if len(out2) == 4 { // keep the planning step short
			break
		}
	}
	return out2
}
