package conductor

import (
	"fmt"
	"strings"
	"testing"
)

// EnrichPushErr must recognize GitHub's verbatim workflow-scope rejection (the
// r140 incident string) and wrap it with the remedy + the local branch, while
// leaving any unrelated error untouched.
func TestEnrichPushErr(t *testing.T) {
	rejection := fmt.Errorf("! [remote rejected] feat/x -> feat/y (refusing to allow an OAuth App to create or update workflow `.github/workflows/release.yml` without 'workflow' scope)")

	got := EnrichPushErr(rejection, "feat/gh-capability-launch-gate")
	msg := got.Error()
	if !strings.Contains(msg, "gh auth refresh -h github.com -s workflow") {
		t.Errorf("enriched message missing remedy: %q", msg)
	}
	if !strings.Contains(msg, "feat/gh-capability-launch-gate") {
		t.Errorf("enriched message missing branch name: %q", msg)
	}

	unrelated := fmt.Errorf("network down")
	if EnrichPushErr(unrelated, "feat/x") != unrelated {
		t.Errorf("unrelated error was not returned unchanged")
	}
}
