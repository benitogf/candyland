package conductor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// ensureAgent must seed a fresh agent with a non-nil Events slice so a
// newly-recorded agent serializes as an empty array, never `"events":null`.
func TestEnsureAgentInitializesEvents(t *testing.T) {
	var agents []run.Agent

	a := ensureAgent(&agents, "coder-1")
	if a.Events == nil {
		t.Fatal("ensureAgent: seeded agent has nil Events, want non-nil empty slice")
	}
	if len(a.Events) != 0 {
		t.Fatalf("ensureAgent: seeded agent Events len = %d, want 0", len(a.Events))
	}

	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal agent: %v", err)
	}
	if strings.Contains(string(b), `"events":null`) {
		t.Fatalf("agent JSON contains events:null: %s", b)
	}
}

// A returning ensureAgent lookup must yield the same agent, preserving events.
func TestEnsureAgentReturnsExisting(t *testing.T) {
	var agents []run.Agent
	ensureAgent(&agents, "coder-1").Events = append(ensureAgent(&agents, "coder-1").Events, run.Event{T: "system", Text: "hi"})

	a := ensureAgent(&agents, "coder-1")
	if len(a.Events) != 1 {
		t.Fatalf("ensureAgent: existing agent Events len = %d, want 1", len(a.Events))
	}
	if len(agents) != 1 {
		t.Fatalf("ensureAgent: agents len = %d, want 1", len(agents))
	}
}

// verdictBearingBlock isolates the verdict line plus the rationale paragraph that
// immediately precedes it, so the hedge-word scan sees the reviewer's REASON for the
// verdict rather than exploratory narration earlier in the transcript.
func TestVerdictBearingBlock(t *testing.T) {
	prose := "First I probably want to check the wiring.\n" +
		"\n" +
		"The consumer is wired and the test passes; I confirmed it.\n" +
		"REVIEW_CLEAN"
	block := verdictBearingBlock(prose)
	if want := "The consumer is wired and the test passes; I confirmed it.\nREVIEW_CLEAN"; block != want {
		t.Errorf("verdictBearingBlock = %q, want %q", block, want)
	}
	// No verdict line -> whole prose (caller's scan behaves as before).
	if got := verdictBearingBlock("just some prose"); got != "just some prose" {
		t.Errorf("verdictBearingBlock with no verdict = %q, want whole prose", got)
	}
}

// V3 hedge-word scan is confined to the verdict-bearing block: exploratory hedging the
// reviewer later RESOLVES before stamping CLEAN must not bounce a proven verdict, while
// a hedge in the rationale that carries the verdict still must.
func TestCleanVerdictHedgeScanConfinedToVerdictBlock(t *testing.T) {
	// Hedge appears only in early exploration, not in the verdict rationale -> clean.
	resolved := "This should be fine, but let me actually verify it.\n" +
		"\n" +
		"Confirmed: the handler is registered and the test exercises it.\n" +
		"REVIEW_CLEAN"
	if bad, reason := cleanVerdictContradictsNarration(resolved); bad {
		t.Errorf("exploratory hedging resolved before CLEAN must not bounce, got bad=%v reason=%q", bad, reason)
	}

	// Hedge sits in the verdict-bearing rationale -> still bounces.
	hedged := "I read the diff.\n" +
		"\n" +
		"The change should be wired correctly, so this is probably fine.\n" +
		"REVIEW_CLEAN"
	if bad, _ := cleanVerdictContradictsNarration(hedged); !bad {
		t.Error("a hedge in the verdict-bearing block must bounce the clean verdict")
	}

	// A blocker-class admission still bounces wherever it appears (not confined).
	admission := "The new path is dead code.\n" +
		"\n" +
		"Otherwise the diff looks correct.\n" +
		"REVIEW_CLEAN"
	if bad, _ := cleanVerdictContradictsNarration(admission); !bad {
		t.Error("a blocker-class admission anywhere in the prose must still bounce")
	}
}
