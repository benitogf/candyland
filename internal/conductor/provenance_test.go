package conductor

import (
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

func TestProvenanceFooterNamesKindAndID(t *testing.T) {
	footer := provenanceFooter("quest", "q-42")
	if !strings.Contains(footer, "quest") {
		t.Errorf("footer omits kind: %q", footer)
	}
	if !strings.Contains(footer, "q-42") {
		t.Errorf("footer omits id: %q", footer)
	}
	if !strings.Contains(footer, "https://github.com/benitogf/candyland") {
		t.Errorf("footer omits attribution link: %q", footer)
	}
}

func TestProvenanceFooterDefaultsKindAndOmitsBlankID(t *testing.T) {
	footer := provenanceFooter("", "")
	if !strings.Contains(footer, "run") {
		t.Errorf("blank kind must default to run: %q", footer)
	}
	if strings.Contains(footer, "``") {
		t.Errorf("blank id must not emit an empty id clause: %q", footer)
	}
}

func TestProvenanceFooterTrimsWhitespace(t *testing.T) {
	footer := provenanceFooter("  quest  ", "  q-1  ")
	if strings.Contains(footer, "  ") {
		t.Errorf("footer must trim whitespace: %q", footer)
	}
	if !strings.Contains(footer, "q-1") || !strings.Contains(footer, "quest") {
		t.Errorf("footer lost trimmed values: %q", footer)
	}
}

func TestRunPRBodyCarriesProvenance(t *testing.T) {
	body := prBody(run.Run{ID: "r-7", Prompt: "do the thing"})
	if !strings.Contains(body, provenanceFooter("run", "r-7")) {
		t.Errorf("run PR body missing provenance footer: %q", body)
	}
}

func TestQuestPRBodyCarriesProvenance(t *testing.T) {
	body := questPRBody(run.Quest{ID: "q-7", OriginalObjective: "converge"})
	if !strings.Contains(body, provenanceFooter("quest", "q-7")) {
		t.Errorf("quest PR body missing provenance footer: %q", body)
	}
}

// A backticked `Closes #53` in the source text is invisible to GitHub's auto-close
// parser; the trailer re-emits it as a bare line so the merge still closes #53.
func TestClosingTrailerNormalizesBacktickedRef(t *testing.T) {
	got := closingTrailer("one PR, extending issue #53 (`Closes #53`).")
	if got != "\n\nCloses #53" {
		t.Errorf("backticked close ref not normalized: %q", got)
	}
}

func TestClosingTrailerAllKeywordFormsAndDedup(t *testing.T) {
	// Every one of GitHub's nine forms normalizes to `Closes`, and repeats of the
	// same number collapse to one line in first-seen order.
	got := closingTrailer("Fixes #7. Also resolved #12. And close #7 again. fixed #12.")
	if got != "\n\nCloses #7\nCloses #12" {
		t.Errorf("keyword-form / dedup handling wrong: %q", got)
	}
}

func TestClosingTrailerIgnoresBarePoundAndPlainProse(t *testing.T) {
	// A `#N` with no closing keyword is not a close directive (GitHub's own rule),
	// so the trailer must stay empty — no spurious close.
	if got := closingTrailer("relates to #99, see issue #100 for context"); got != "" {
		t.Errorf("bare #N must not produce a close trailer: %q", got)
	}
}

func TestRunPRBodyClosesReferencedIssueParseably(t *testing.T) {
	// End-to-end: a plan that only ever wrote `Closes #53` inside backticks still
	// yields a bare, parseable `Closes #53` in the delivered PR body.
	body := prBody(run.Run{ID: "r-9", Prompt: "## Delivery\n\nOne PR (`Closes #53`)."})
	if !strings.Contains(body, "\nCloses #53") {
		t.Errorf("run PR body missing parseable close line: %q", body)
	}
}
