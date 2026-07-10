package comms

import (
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/bus"
)

// Two-layer intent (Task 5): formatBrief renders a ROOT INTENT section ONLY when the
// root intent is present AND differs from the task intent. When they coincide (a
// standalone run) the section is omitted, which disarms the conflict channel.
func TestFormatBriefRootIntentRenderRule(t *testing.T) {
	// Different → rendered.
	got := formatBrief(bus.Brief{Role: "reviewer", Intent: "add csv export", RootIntent: "overhaul the reports program"})
	if !strings.Contains(got, "ROOT INTENT (context only") {
		t.Errorf("a differing root intent must render a ROOT INTENT section, got:\n%s", got)
	}
	if !strings.Contains(got, "overhaul the reports program") {
		t.Errorf("the root intent text must render, got:\n%s", got)
	}

	// Coinciding → omitted (channel disarmed).
	same := formatBrief(bus.Brief{Role: "reviewer", Intent: "add csv export", RootIntent: "add csv export"})
	if strings.Contains(same, "ROOT INTENT") {
		t.Errorf("a root intent equal to the task intent must NOT render (disarm), got:\n%s", same)
	}

	// Absent → omitted.
	none := formatBrief(bus.Brief{Role: "reviewer", Intent: "add csv export"})
	if strings.Contains(none, "ROOT INTENT") {
		t.Errorf("no root intent must render no section, got:\n%s", none)
	}
}

// The prompt rendering is gated on what the context-only label claims — a files
// boundary. A coder task brief (Files set) gets the labeled form; a reviewer
// brief (the Prompt is the diff command to RUN) and a quest-lead brief (the
// Prompt is the tick directive) render it bare, with no boundary language that
// would contradict their bootstraps.
func TestFormatBriefPromptLabelGatedOnFilesBoundary(t *testing.T) {
	const label = "run prompt (context only"

	coder := formatBrief(bus.Brief{Role: "backend", Title: "task a", Files: []string{"a.txt"}, Test: "a_test", Prompt: "add csv export end to end"})
	if !strings.Contains(coder, label) {
		t.Errorf("a coder brief (Files set) must label the prompt as context, got %q", coder)
	}
	if !strings.Contains(coder, "add csv export end to end") {
		t.Errorf("the labeled form must still carry the prompt text, got %q", coder)
	}

	for name, b := range map[string]bus.Brief{
		"reviewer":   {Role: "reviewer", Prompt: "git diff main..feat", Intent: "add csv export"},
		"quest-lead": {Role: "quest-lead", Prompt: "tick: discover, triage, launch"},
	} {
		got := formatBrief(b)
		if strings.Contains(got, label) || strings.Contains(got, "files boundary") {
			t.Errorf("%s brief (no Files) must render the prompt bare, got %q", name, got)
		}
		if !strings.Contains(got, b.Prompt) {
			t.Errorf("%s brief dropped the prompt, got %q", name, got)
		}
	}
}
