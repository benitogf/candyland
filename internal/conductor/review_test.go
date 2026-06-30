package conductor

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// parseReview turns a reviewer's structured verdict line into blockers. A
// `REVIEW_CLEAN` line is a clean pass; a `REVIEW_FINDINGS <json>` line carries the
// blockers; anything else is no verdict (the caller refuses to open a PR on it).
func TestParseReviewVerdict(t *testing.T) {
	if v, ok := parseReview("some prose\nREVIEW_CLEAN"); !ok || len(v.Blockers) != 0 {
		t.Errorf("REVIEW_CLEAN must parse as a clean verdict, got ok=%v blockers=%+v", ok, v.Blockers)
	}
	v, ok := parseReview(`looked at it
REVIEW_FINDINGS {"blockers":[{"file":"a.go","line":12,"issue":"nil deref"}]}`)
	if !ok || len(v.Blockers) != 1 || v.Blockers[0].File != "a.go" || v.Blockers[0].Line != 12 {
		t.Errorf("REVIEW_FINDINGS must parse its blockers, got ok=%v v=%+v", ok, v)
	}
	if _, ok := parseReview("I think it's probably fine"); ok {
		t.Error("text with no verdict line must not parse as a verdict (no silent pass)")
	}
}

// A stub claude that drives the full delivery AND a real review phase. The reviewer
// (agentID "review", spawned with the review-doctrine prompt) reports a blocker on
// its FIRST spawn and CLEAN thereafter; the fix pass (prompt names "review
// findings") writes a file so there's a real edit to commit. Per-round reviewer
// output is scripted via a counter file at $CANDYLAND_REVIEW_COUNT.
const reviewThenCleanClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"code reviewer"* ]]; then
  n=$(cat "$CANDYLAND_REVIEW_COUNT" 2>/dev/null || echo 0)
  n=$((n+1)); echo "$n" > "$CANDYLAND_REVIEW_COUNT"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  if [[ "$n" -le 1 ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_FINDINGS {\"blockers\":[{\"file\":\"a.txt\",\"line\":1,\"issue\":\"needs a guard\"}]}"}]}}'
  else
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  fi
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"review findings"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file":"a.txt"}}]}}'
  printf 'fixed per review\n' >> "a.txt"
  echo '{"type":"result","subtype":"success","result":"fixed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "content" > "a.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

func TestReviewFindingsDriveFixThenPR(t *testing.T) {
	c, repo := deliveryConductor(t, reviewThenCleanClaude)
	t.Setenv("CANDYLAND_REVIEW_COUNT", t.TempDir()+"/n")
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "3")
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error != "" {
		t.Fatalf("review should have gone clean on round 2, but the run errored: %q", r.Error)
	}
	// A SEPARATE reviewer agent ran in the Review phase.
	var reviewer *run.Agent
	for i := range r.Agents {
		if r.Agents[i].ID == reviewerID {
			reviewer = &r.Agents[i]
		}
	}
	if reviewer == nil {
		t.Fatal("no reviewer agent was spawned in the review phase")
	}
	if reviewer.State != "done" {
		t.Errorf("reviewer should end done (review clean), got %q", reviewer.State)
	}
	// The fix pass actually committed onto the run branch (real fix → re-review).
	out, err := exec.Command("git", "-C", repo, "show", r.Branch+":a.txt").CombinedOutput()
	if err != nil {
		t.Fatalf("reading a.txt on the run branch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "fixed per review") {
		t.Errorf("the fix pass's change must be committed on the run branch before the PR:\n%s", out)
	}
	// The PR opened only after the clean review.
	if r.PrURL == "" {
		t.Error("a clean-reviewed run must open a PR")
	}
}

// A reviewer that NEVER goes clean: the run exhausts its review-round budget and
// fails honestly WITHOUT opening a PR (no PR on un-reviewed/blocked work).
const reviewNeverCleanClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_FINDINGS {\"blockers\":[{\"file\":\"a.txt\",\"line\":1,\"issue\":\"still wrong\"}]}"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"review findings"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file":"a.txt"}}]}}'
  printf 'attempted fix\n' >> "a.txt"
  echo '{"type":"result","subtype":"success","result":"fixed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "content" > "a.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

func TestReviewNeverCleanFailsWithoutPR(t *testing.T) {
	c, _ := deliveryConductor(t, reviewNeverCleanClaude)
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "2") // one review + one fix-then-re-review, then fail
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q", r.Status)
	}
	if r.Error == "" {
		t.Fatal("an un-clearable review must record an honest error, not finish clean")
	}
	if !strings.Contains(strings.ToLower(r.Error), "review") {
		t.Errorf("the error should name the unresolved review, got %q", r.Error)
	}
	// The defining safety property: no PR on a change review never cleared.
	if r.PrURL != "" {
		t.Errorf("a never-clean review must not open a PR, got %q", r.PrURL)
	}
	if len(r.PRs) != 0 {
		t.Errorf("no PR record should exist for a blocked review, got %+v", r.PRs)
	}
	// It reached the Review phase but not PR.
	if r.Phase != run.PhaseReview {
		t.Errorf("a review-blocked run should rest in the Review phase, got phase=%d", r.Phase)
	}
}
