package conductor

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// fixNeverChangesClaude drives a full delivery through a real review phase whose
// reviewer ALWAYS reports a blocker, and whose fix pass NEVER changes a file (it
// counts its invocations at $CANDYLAND_FIX_COUNT but writes nothing). #64.2: a single
// no-diff fix pass must NOT be terminal — the fix agent is retried up to maxAttempts()
// spawns before the review is failed.
const fixNeverChangesClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_FINDINGS {\"blockers\":[{\"file\":\"a.txt\",\"line\":1,\"issue\":\"still wrong\"}]}"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"review findings"* ]]; then
  n=$(cat "$CANDYLAND_FIX_COUNT" 2>/dev/null || echo 0)
  n=$((n+1)); echo "$n" > "$CANDYLAND_FIX_COUNT"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo looked but changed nothing"}}]}}'
  echo '{"type":"result","subtype":"success","result":"no change","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "content" > "a.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

// #64.2: a fix pass that produces no diff is retried up to maxAttempts() (3) spawns
// before failReview fires — a single no-diff pass is not treated as terminal.
func TestFixReviewFindingsRetriesBeforeFailing(t *testing.T) {
	c, _ := deliveryConductor(t, fixNeverChangesClaude)
	t.Setenv("CANDYLAND_FIX_COUNT", t.TempDir()+"/n")
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "2") // one review + one fix round, then fail
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "blocked" }, 40*time.Second)
	if r.Status != "blocked" {
		t.Fatalf("a never-fixed review must terminate blocked, got status=%q error=%q", r.Status, r.Error)
	}
	if r.Error == "" {
		t.Fatal("a fix pass that never lands a diff must record an honest error")
	}
	count, _ := os.ReadFile(os.Getenv("CANDYLAND_FIX_COUNT"))
	n, _ := strconv.Atoi(strings.TrimSpace(string(count)))
	if n != maxAttempts() {
		t.Fatalf("fix pass must be spawned maxAttempts()=%d times before failing, got %d", maxAttempts(), n)
	}
	if r.PrURL != "" {
		t.Errorf("a never-fixed review must not open a PR, got %q", r.PrURL)
	}
	// #64.3: a blocked terminal must drop any stale in-flight status line (e.g. the
	// "Addressing N review findings…" line the fix pass set).
	if r.StatusLine != "" {
		t.Errorf("a blocked terminal must clear the stale status line, got %q", r.StatusLine)
	}
}

// #64.4: a run parked on unresolved review findings persists those findings on its
// record (ReviewFindings), so post-hoc recovery need not re-derive them.
func TestUnresolvedReviewFindingsPersisted(t *testing.T) {
	c, _ := deliveryConductor(t, reviewNeverCleanClaude)
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "2")
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "blocked" }, 40*time.Second)
	if r.Status != "blocked" {
		t.Fatalf("an un-clearable review must terminate blocked, got status=%q error=%q", r.Status, r.Error)
	}
	if len(r.ReviewFindings) == 0 {
		t.Fatal("a run parked on unresolved findings must persist ReviewFindings on the record")
	}
}
