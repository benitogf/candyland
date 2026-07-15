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

// "regression" immediately followed by a QA-activity noun (sweep/test/suite/check…)
// is the reviewer DESCRIBING verification work, not admitting a defect — it must not
// bounce a clean verdict. Genuine defect shapes and the negation guard are unchanged.
func TestCleanVerdictRegressionQAActivityNotAdmission(t *testing.T) {
	notFlagged := []string{
		// r138's exact shapes.
		"Final regression sweep:\n\nAll checks pass.\nREVIEW_CLEAN",
		"Regression sweep: `go vet` clean, `go build` green.\n\nVerified end to end.\nREVIEW_CLEAN",
		// Other QA-activity collocations, incl. hyphenated.
		"The regression tests pass on this branch.\n\nConfirmed.\nREVIEW_CLEAN",
		"The regression suite is green.\n\nConfirmed.\nREVIEW_CLEAN",
		"I ran the regression checks and they pass.\n\nConfirmed.\nREVIEW_CLEAN",
		"Added a regression-test for the fix.\n\nConfirmed.\nREVIEW_CLEAN",
		// Negation guard must keep working.
		"There is no regression here.\n\nConfirmed.\nREVIEW_CLEAN",
	}
	for _, text := range notFlagged {
		if bad, reason := cleanVerdictContradictsNarration(text); bad {
			t.Errorf("QA-activity 'regression' collocation must not bounce: %q -> reason %q", text, reason)
		}
	}

	flagged := []string{
		"This introduces a regression.\n\nOtherwise fine.\nREVIEW_CLEAN",
		"There is a regression in the retry path.\n\nOtherwise fine.\nREVIEW_CLEAN",
		"Bare regression mentioned here.\n\nOtherwise fine.\nREVIEW_CLEAN",
		// Clause boundaries: punctuation between "regression" and a QA noun is an
		// admission followed by a NEW clause, not the QA-activity collocation — the
		// suppression window must not trim past it.
		"This introduces a regression. Tests were not updated.\n\nREVIEW_CLEAN",
		"This introduces a regression; tests TestRetry and TestBackoff now fail.\n\nREVIEW_CLEAN",
		"I found a regression, tests fail on the handler.\n\nREVIEW_CLEAN",
		"There is a regression — tests fail.\n\nREVIEW_CLEAN",
		"Regression: tests fail on main.\n\nREVIEW_CLEAN",
	}
	for _, text := range flagged {
		if bad, _ := cleanVerdictContradictsNarration(text); !bad {
			t.Errorf("defect-shaped 'regression' must still bounce: %q", text)
		}
	}
}

// The ground-truth delivery gate counts a PR as delivered ONLY on a real URL with no
// recorded error — never an optimistic or half-written record.
func TestDeliveredPRs(t *testing.T) {
	prs := []run.PR{
		{Repo: "a", URL: "https://github.com/x/a/pull/1"}, // delivered
		{Repo: "b", Err: "push failed"},                   // failed — no URL
		{Repo: "c"},                                       // neither — not delivered
		{Repo: "d", URL: "https://github.com/x/d/pull/2", Err: "half-written"}, // URL but errored — rejected
	}
	if got := deliveredPRs(prs); got != 1 {
		t.Fatalf("deliveredPRs = %d, want 1 (only the clean URL counts)", got)
	}
	if got := deliveredPRs(nil); got != 0 {
		t.Fatalf("deliveredPRs(nil) = %d, want 0", got)
	}
}

// budgetExceeded gates at-or-over the budget; a zero/negative budget never gates.
func TestBudgetExceeded(t *testing.T) {
	if budgetExceeded(1_000_000, 0) {
		t.Error("a zero budget must be treated as no cap, never tripped")
	}
	if budgetExceeded(1_000_000, -1) {
		t.Error("a negative budget must be treated as no cap")
	}
	if budgetExceeded(99, 100) {
		t.Error("usage under budget must not trip the gate")
	}
	if !budgetExceeded(100, 100) {
		t.Error("usage exactly at budget must trip the gate")
	}
	if !budgetExceeded(101, 100) {
		t.Error("usage over budget must trip the gate")
	}
}

// weightedBudgetExceeded weighs the run's per-agent usage split and gates on it —
// the weighted total is in output-basis ktokens, the same unit as the budget.
func TestWeightedBudgetExceeded(t *testing.T) {
	r := run.Run{
		TokensBudget: 1,
		Agents: []run.Agent{
			{InputTokens: 1000, CacheReadTokens: 5000, CacheCreationTokens: 200, Tokens: 1}, // Tokens=1 → 1000 raw output
		},
	}
	// weighted ktok = (1000*0.2 + 5000*0.02 + 200*0.25 + 1000*1.0)/1000 = (200+100+50+1000)/1000 = 1
	if got := runWeightedTokens(r); got != 1 {
		t.Fatalf("runWeightedTokens = %d, want 1", got)
	}
	if !weightedBudgetExceeded(r) {
		t.Error("weighted usage (1) at/over budget (1) must trip the gate")
	}

	// No budget → never gated, however large the usage.
	r.TokensBudget = 0
	if weightedBudgetExceeded(r) {
		t.Error("a run with no budget must never trip the weighted-budget gate")
	}
}
