package conductor

import (
	"regexp"
	"strings"

	"github.com/benitogf/candyland/internal/run"
)

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 32 {
		s = s[:32]
	}
	if s == "" {
		return "run"
	}
	return s
}

func isDone(state string) bool { return state == "green" || state == "done" }

// recompute derives the rollup fields from the agents/tasks so the UI never
// has to compute them — single source of truth on the server.
func recompute(r *run.Run) {
	tokens := 0
	for _, a := range r.Agents {
		tokens += a.Tokens
	}
	green := 0
	for _, t := range r.Tasks {
		if isDone(t.State) {
			green++
		}
	}
	r.TokensUsed = tokens
	r.TasksGreen = green
	r.TasksTotal = len(r.Tasks)
	r.CostUsd = float64(tokens) * 0.012
}
