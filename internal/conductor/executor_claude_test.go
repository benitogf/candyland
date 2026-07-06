package conductor

import "testing"

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
