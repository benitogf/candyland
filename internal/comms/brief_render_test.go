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
