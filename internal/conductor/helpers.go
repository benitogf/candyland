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

	// Progress tracks the run's advance so the UI bar actually MOVES — derived from
	// the phase, plus (during Build) how many coder tasks have gone green. Go's zero
	// value is 0, so without this the bar sits at 0 from start to finish — the
	// "stale, no feedback" a long run shows. A failed run keeps the phase it stalled
	// at, so its bar honestly stops partway rather than implying completion.
	span := len(run.Phases) - 1 // 3 transitions across 4 phases
	prog := float64(r.Phase) / float64(span)
	if r.Phase == run.PhaseBuild && r.TasksTotal > 0 { // Build: fill toward Integrate as coders finish
		prog += (float64(green) / float64(r.TasksTotal)) / float64(span)
	}
	if prog > 1 {
		prog = 1
	}
	// An errored run is never 100% complete, whatever phase it stalled at — the bar
	// must not imply a finish the run never reached (e.g. a push/PR-open failure).
	if r.Error != "" && prog >= 1 {
		prog = float64(span-1) / float64(span)
	}
	r.Progress = prog
}
