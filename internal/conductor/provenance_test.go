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
	footer := provenanceFooter("  campaign  ", "  c-1  ")
	if strings.Contains(footer, "  ") {
		t.Errorf("footer must trim whitespace: %q", footer)
	}
	if !strings.Contains(footer, "c-1") || !strings.Contains(footer, "campaign") {
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

func TestCampaignPRBodyCarriesProvenance(t *testing.T) {
	body := campaignPRBody(run.Campaign{ID: "c-7", OriginalInput: "ship it"}, run.IntentBrief{}, nil)
	if !strings.Contains(body, provenanceFooter("campaign", "c-7")) {
		t.Errorf("campaign PR body missing provenance footer: %q", body)
	}
}
