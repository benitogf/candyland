package conductor

import (
	"os"
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

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "blocked" }, 40*time.Second)
	if r.Status != "blocked" {
		t.Fatalf("an un-clearable review must terminate blocked: status=%q", r.Status)
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

// C3: the reviewer/fix identity's per-pass budget is clamped to a hard ceiling so a
// context-blind fix agent can't blow up (the incident saw ~84 tool calls). The
// default 400 spawn budget must be clamped down, and an env override never raises
// it above the ceiling.
func TestReviewFixBudgetClampedToCeiling(t *testing.T) {
	if got := clampReviewBudget(400); got != reviewFixCeiling {
		t.Errorf("a 400 budget must clamp to the ceiling %d, got %d", reviewFixCeiling, got)
	}
	if got := clampReviewBudget(5); got != 5 {
		t.Errorf("a budget already under the ceiling must pass through, got %d", got)
	}
	t.Setenv("CANDYLAND_REVIEW_BUDGET", "10")
	if got := clampReviewBudget(400); got != 10 {
		t.Errorf("an env override below the ceiling must apply, got %d", got)
	}
	t.Setenv("CANDYLAND_REVIEW_BUDGET", "9999")
	if got := clampReviewBudget(400); got != reviewFixCeiling {
		t.Errorf("an env override above the ceiling must NOT exceed it (%d), got %d", reviewFixCeiling, got)
	}
}

// C2: a fix pass invoked with NO findings must fail fast (explicit error, no PR),
// never silently run an unconstrained agent that re-derives its own task list.
func TestFixReviewFindingsFailsFastOnEmptyFindings(t *testing.T) {
	c, _ := deliveryConductor(t, reviewThenCleanClaude)
	id := c.Create(run.Spec{Prompt: "do the thing"})
	// Call the fix pass directly with empty blockers — it must abort fast.
	if ok, _, _ := c.fixReviewFindings(t.Context(), id, "repo", t.TempDir(), "br", nil, nil, 1, "", "", ""); ok {
		t.Fatal("a fix pass with no findings must return false (fail fast), not true")
	}
	r, _ := c.Get(id)
	if r.Error == "" {
		t.Fatal("an empty-findings fix pass must record an explicit error")
	}
	if !strings.Contains(strings.ToLower(r.Error), "no review findings") {
		t.Errorf("the error should name the empty findings, got %q", r.Error)
	}
}

// C5: when the reviewer cannot prove a wiring-dependent feature is reachable from
// the entrypoint, it emits a blocker (not REVIEW_CLEAN). That blocker drives a fix
// pass; once wiring is proven, the review goes clean and the PR opens. The stub
// reviewer reports a "wiring unproven" blocker on its first spawn and clean after.
const reviewWiringUnprovenClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"code reviewer"* ]]; then
  n=$(cat "$CANDYLAND_REVIEW_COUNT" 2>/dev/null || echo 0)
  n=$((n+1)); echo "$n" > "$CANDYLAND_REVIEW_COUNT"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go build ./... && ./bin"}}]}}'
  if [[ "$n" -le 1 ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_FINDINGS {\"blockers\":[{\"file\":\"main.go\",\"line\":1,\"issue\":\"wiring unproven: feature not shown reachable from entrypoint\"}]}"}]}}'
  else
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  fi
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"review findings"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file":"a.txt"}}]}}'
  printf 'wired in\n' >> "a.txt"
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

func TestReviewWiringUnprovenBlocksThenFixOpensPR(t *testing.T) {
	c, repo := deliveryConductor(t, reviewWiringUnprovenClaude)
	t.Setenv("CANDYLAND_REVIEW_COUNT", t.TempDir()+"/n")
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "3")
	id := c.Create(run.Spec{Prompt: "wire a feature"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error != "" {
		t.Fatalf("review should clear once wiring is proven, but errored: %q", r.Error)
	}
	// The fix pass committed the wiring fix onto the run branch.
	out, err := exec.Command("git", "-C", repo, "show", r.Branch+":a.txt").CombinedOutput()
	if err != nil {
		t.Fatalf("reading a.txt on the run branch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "wired in") {
		t.Errorf("the wiring fix must be committed before the PR:\n%s", out)
	}
	if r.PrURL == "" {
		t.Error("once wiring is proven clean, the run must open a PR")
	}
}

// A template-forked reviewer starts with core/review-rigor + truthseeker already
// in context, so its slim bootstrap must not re-load doctrine (no kb_get) nor
// assume a prior load in THIS conversation ("ALREADY loaded") — while keeping the
// role discriminator, the brief fetch, and the verdict-line contract intact. The
// full reviewBootstrap stays the cold/fallback prompt, kb_get load included.
func TestReviewerSlimBootstrap(t *testing.T) {
	if strings.Contains(reviewBootstrapSlim, "kb_get") {
		t.Error("the slim bootstrap must not instruct a kb_get doctrine load")
	}
	if strings.Contains(reviewBootstrapSlim, "ALREADY loaded") {
		t.Error("the slim bootstrap must not assume a prior in-conversation load")
	}
	for _, keep := range []string{"code reviewer", "brief_get", "REVIEW_CLEAN", "REVIEW_FINDINGS", "wiring unproven"} {
		if !strings.Contains(reviewBootstrapSlim, keep) {
			t.Errorf("the slim bootstrap must keep %q", keep)
		}
	}
	if !strings.Contains(reviewBootstrap, "kb_get") {
		t.Error("the full (cold/fallback) bootstrap must keep the kb_get doctrine load")
	}
}

// The reviewer judges intent fidelity, so reviewUntilClean threads the run's
// driving intent (OriginalIntent + the partitioned task titles) into BOTH the
// reviewer's brief and, when a fix round runs, the fix pass's brief. The stub
// captures each brief it is spawned with (via brief_get over the bus) to a file so
// the test can assert the intent text and a task title rode along.
const reviewCapturesIntentClaude = `#!/usr/bin/env bash
prompt="$2"
brief=$(curl -s "http://$CANDYLAND_BUS_ADDR/brief/$CANDYLAND_AGENT_ID" 2>/dev/null)
if [[ "$prompt" == *"code reviewer"* ]]; then
  printf '%s' "$brief" > "$CANDYLAND_REVIEW_BRIEF"
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
  printf '%s' "$brief" > "$CANDYLAND_FIX_BRIEF"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file":"a.txt"}}]}}'
  printf 'fixed per review\n' >> "a.txt"
  echo '{"type":"result","subtype":"success","result":"fixed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"wire the widget\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "content" > "a.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

func TestReviewBriefCarriesDrivingIntent(t *testing.T) {
	c, _ := deliveryConductor(t, reviewCapturesIntentClaude)
	dir := t.TempDir()
	t.Setenv("CANDYLAND_REVIEW_COUNT", dir+"/n")
	t.Setenv("CANDYLAND_REVIEW_BRIEF", dir+"/review-brief")
	t.Setenv("CANDYLAND_FIX_BRIEF", dir+"/fix-brief")
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "3")

	const intentMarker = "INTENTMARKER-ship-the-lever"
	id := c.Create(run.Spec{Prompt: intentMarker})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("run did not finish clean: status=%q error=%q", r.Status, r.Error)
	}

	reviewBrief, err := os.ReadFile(dir + "/review-brief")
	if err != nil {
		t.Fatalf("reviewer brief was not captured: %v", err)
	}
	if !strings.Contains(string(reviewBrief), intentMarker) {
		t.Errorf("the reviewer brief must carry the run's driving intent %q:\n%s", intentMarker, reviewBrief)
	}
	// The partitioned task title rides along as "what the loop set out to build".
	if !strings.Contains(string(reviewBrief), "wire the widget") {
		t.Errorf("the reviewer brief must carry the partitioned task titles:\n%s", reviewBrief)
	}

	fixBrief, err := os.ReadFile(dir + "/fix-brief")
	if err != nil {
		t.Fatalf("fix-pass brief was not captured (a fix round must have run): %v", err)
	}
	if !strings.Contains(string(fixBrief), intentMarker) {
		t.Errorf("the fix-pass brief must also carry the driving intent %q:\n%s", intentMarker, fixBrief)
	}
}

// The r123 shape: a fix pass whose resolving leg reports sawTool=false (a
// limit/connection pause resumed and emitted only a summary) but whose integDir
// actually holds the finished edits must commit and deliver — the worktree, not the
// stream, is the delivery ground truth.
const fixDirtyNoToolClaude = `#!/usr/bin/env bash
printf 'the real fix\n' >> fixed.txt
echo '{"type":"result","subtype":"success","result":"I already made the fixes before the pause; here is my summary.","usage":{"output_tokens":1}}'
`

func TestFixReviewFindingsDeliversOnDirtyWorktreeDespiteNoTool(t *testing.T) {
	c, repo := deliveryConductor(t, fixDirtyNoToolClaude)
	id := c.Create(run.Spec{Prompt: "do the thing"})
	blockers := []reviewFinding{{File: "a.go", Line: 1, Issue: "fix this"}}
	ok, _, _ := c.fixReviewFindings(t.Context(), id, "repo", repo, "main", blockers, nil, 1, "", "", "")
	if !ok {
		r, _ := c.Get(id)
		t.Fatalf("a dirty integDir must deliver even with sawTool=false; got ok=false error=%q", r.Error)
	}
	r, _ := c.Get(id)
	if r.Error != "" {
		t.Errorf("delivery must not record an error, got %q", r.Error)
	}
	// The edits were committed onto the run branch.
	out, err := exec.Command("git", "-C", repo, "show", "main:fixed.txt").CombinedOutput()
	if err != nil {
		t.Fatalf("reading fixed.txt on the branch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "the real fix") {
		t.Errorf("the fix must be committed before delivery:\n%s", out)
	}
}

// A fix pass that genuinely changes nothing (clean worktree, sawTool=false).
const fixCleanNoOpClaude = `#!/usr/bin/env bash
echo '{"type":"result","subtype":"success","result":"nothing to change","usage":{"output_tokens":1}}'
`

// core/completion (detritus#224): a no-op remediation is a delivery signal, not a
// block. A no-diff fix pass over a REAL (reviewer-cited) finding no longer refuses
// at the unit level — the finding may already be satisfied at the integrated HEAD
// (e.g. an integration merge landed the fix after the review was computed), and
// maker≠checker forbids the fixer from self-clearing it either way. It returns true
// WITHOUT creating a commit, deferring to the caller's confirming reviewer round:
// reviewUntilClean re-reviews at HEAD next round and only a FRESH reviewer clears the
// real finding. The end-to-end refusal of a finding that KEEPS reproducing is
// preserved at the caller level (see TestReviewRealNoDiffNeverClearsBlocksWithoutPR).
func TestFixReviewFindingsDefersRealNoDiffToReReview(t *testing.T) {
	c, repo := deliveryConductor(t, fixCleanNoOpClaude)
	id := c.Create(run.Spec{Prompt: "do the thing"})
	tipBefore, err := exec.Command("git", "-C", repo, "rev-parse", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse main: %v\n%s", err, tipBefore)
	}
	blockers := []reviewFinding{{File: "a.go", Line: 1, Issue: "fix this"}}
	ok, _, _ := c.fixReviewFindings(t.Context(), id, "repo", repo, "main", blockers, nil, 1, "", "", "")
	if !ok {
		r, _ := c.Get(id)
		t.Fatalf("a no-diff pass over a real finding must DEFER (ok=true) to the confirming reviewer, not refuse; error=%q", r.Error)
	}
	r, _ := c.Get(id)
	if r.Error != "" {
		t.Errorf("the deferral must not record a failure error, got %q", r.Error)
	}
	tipAfter, err := exec.Command("git", "-C", repo, "rev-parse", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse main after: %v\n%s", err, tipAfter)
	}
	if string(tipBefore) != string(tipAfter) {
		t.Errorf("a no-diff deferral must not create a commit: tip moved %s -> %s", tipBefore, tipAfter)
	}
}

// The r138 shape (#71): every blocker is a conductor-synthesized narration bounce,
// the fix pass correctly concludes false-positive, cites concrete evidence, and
// re-stamps REVIEW_CLEAN with no diff — by design. That resolution must be
// ACCEPTED (ok=true, nothing committed), not counted as a failed attempt.
const fixNoDiffCleanResolutionClaude = `#!/usr/bin/env bash
echo '{"type":"result","subtype":"success","result":"I verified the finding is a false positive: the handler is registered in router.go line 12 and TestHandlerRoundTrip exercises it end to end and passes. The change is wired and works.\nREVIEW_CLEAN","usage":{"output_tokens":1}}'
`

func TestFixReviewFindingsAcceptsNoDiffCleanForSynthesized(t *testing.T) {
	c, repo := deliveryConductor(t, fixNoDiffCleanResolutionClaude)
	id := c.Create(run.Spec{Prompt: "do the thing"})
	tipBefore, err := exec.Command("git", "-C", repo, "rev-parse", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse main: %v\n%s", err, tipBefore)
	}
	blockers := []reviewFinding{{Issue: "REVIEW_CLEAN contradicts its own narration — cite mitigating evidence", synthesized: true}}
	ok, _, _ := c.fixReviewFindings(t.Context(), id, "repo", repo, "main", blockers, nil, 1, "", "", "")
	if !ok {
		r, _ := c.Get(id)
		t.Fatalf("an evidence-cited, detector-clean no-diff resolution of an all-synthesized finding set must be accepted; got ok=false error=%q", r.Error)
	}
	r, _ := c.Get(id)
	if r.Error != "" {
		t.Errorf("the accept path must not record an error, got %q", r.Error)
	}
	tipAfter, err := exec.Command("git", "-C", repo, "rev-parse", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse main after: %v\n%s", err, tipAfter)
	}
	if string(tipBefore) != string(tipAfter) {
		t.Errorf("a no-diff acceptance must not create a commit: tip moved %s -> %s", tipBefore, tipAfter)
	}
}

// The maker≠checker case: even when the no-diff pass RE-STAMPS REVIEW_CLEAN, the
// fixer's own verdict must NOT self-clear a REAL reviewer-cited finding (only the
// #71 all-synthesized accept path may act on a bounce-authorized re-stamp). It still
// returns true WITHOUT committing — deferring confirmation to a FRESH reviewer round
// at HEAD, never trusting the fixer's own REVIEW_CLEAN as the clearing authority.
func TestFixReviewFindingsDefersRealNoDiffCleanToReReview(t *testing.T) {
	c, repo := deliveryConductor(t, fixNoDiffCleanResolutionClaude)
	id := c.Create(run.Spec{Prompt: "do the thing"})
	tipBefore, err := exec.Command("git", "-C", repo, "rev-parse", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse main: %v\n%s", err, tipBefore)
	}
	blockers := []reviewFinding{{File: "a.go", Line: 1, Issue: "fix this"}}
	ok, _, _ := c.fixReviewFindings(t.Context(), id, "repo", repo, "main", blockers, nil, 1, "", "", "")
	if !ok {
		r, _ := c.Get(id)
		t.Fatalf("a no-diff pass over a real finding must DEFER (ok=true), not refuse, even with a re-stamped REVIEW_CLEAN; error=%q", r.Error)
	}
	tipAfter, err := exec.Command("git", "-C", repo, "rev-parse", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse main after: %v\n%s", err, tipAfter)
	}
	if string(tipBefore) != string(tipAfter) {
		t.Errorf("the fixer's own REVIEW_CLEAN must NOT self-clear a real finding into a commit: tip moved %s -> %s", tipBefore, tipAfter)
	}
}

// Guard: a synthesized finding resolved with no diff but NO re-stamped verdict
// still refuses — prose alone is not an acceptable resolution.
func TestFixReviewFindingsNoVerdictNoDiffStillRefusesForSynthesized(t *testing.T) {
	c, repo := deliveryConductor(t, fixCleanNoOpClaude) // "nothing to change", no verdict line
	id := c.Create(run.Spec{Prompt: "do the thing"})
	blockers := []reviewFinding{{Issue: "REVIEW_CLEAN contradicts its own narration", synthesized: true}}
	if ok, _, _ := c.fixReviewFindings(t.Context(), id, "repo", repo, "main", blockers, nil, 1, "", "", ""); ok {
		t.Fatal("a no-diff pass without a re-stamped REVIEW_CLEAN must refuse")
	}
	r, _ := c.Get(id)
	if !strings.Contains(strings.ToLower(r.Error), "made no changes") {
		t.Errorf("the refusal must name the empty change set, got %q", r.Error)
	}
}

// Guard: a re-stamped REVIEW_CLEAN whose own prose hedges must fail the re-run
// verdict-integrity detector and still refuse.
const fixNoDiffHedgedCleanClaude = `#!/usr/bin/env bash
echo '{"type":"result","subtype":"success","result":"the code is probably fine, I did not find anything concrete.\nREVIEW_CLEAN","usage":{"output_tokens":1}}'
`

func TestFixReviewFindingsHedgedCleanStillRefusesForSynthesized(t *testing.T) {
	c, repo := deliveryConductor(t, fixNoDiffHedgedCleanClaude)
	id := c.Create(run.Spec{Prompt: "do the thing"})
	blockers := []reviewFinding{{Issue: "REVIEW_CLEAN contradicts its own narration", synthesized: true}}
	if ok, _, _ := c.fixReviewFindings(t.Context(), id, "repo", repo, "main", blockers, nil, 1, "", "", ""); ok {
		t.Fatal("a hedged no-diff REVIEW_CLEAN must fail the detector re-run and refuse")
	}
	r, _ := c.Get(id)
	if !strings.Contains(strings.ToLower(r.Error), "made no changes") {
		t.Errorf("the refusal must name the empty change set, got %q", r.Error)
	}
}

// Guard: a DIRTY worktree keeps the existing commit+deliver path even for a
// synthesized finding set — the accept path only applies when there is no diff.
const fixDirtySynthesizedClaude = `#!/usr/bin/env bash
printf 'hardened\n' >> hardened.txt
echo '{"type":"result","subtype":"success","result":"I hardened the cited path and verified it.\nREVIEW_CLEAN","usage":{"output_tokens":1}}'
`

func TestFixReviewFindingsDirtyWorktreeStillCommitsForSynthesized(t *testing.T) {
	c, repo := deliveryConductor(t, fixDirtySynthesizedClaude)
	id := c.Create(run.Spec{Prompt: "do the thing"})
	blockers := []reviewFinding{{Issue: "REVIEW_CLEAN contradicts its own narration", synthesized: true}}
	ok, _, _ := c.fixReviewFindings(t.Context(), id, "repo", repo, "main", blockers, nil, 1, "", "", "")
	if !ok {
		r, _ := c.Get(id)
		t.Fatalf("a dirty worktree must commit+deliver for synthesized findings too; got ok=false error=%q", r.Error)
	}
	out, err := exec.Command("git", "-C", repo, "show", "main:hardened.txt").CombinedOutput()
	if err != nil {
		t.Fatalf("reading hardened.txt on the branch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "hardened") {
		t.Errorf("the fix must be committed before delivery:\n%s", out)
	}
}

// Caller-level regression guard: a REAL finding that KEEPS reproducing (the reviewer
// flags it EVERY round, the fixer NEVER produces a diff — a lazy/incapable fixer)
// must still ultimately terminate the run blocked WITHOUT opening a PR. The unit-level
// refusal moved to a deferral (fixReviewFindings returns true on a no-diff real
// finding), so the safety property the refusal used to guarantee is now enforced at
// the caller: reviewUntilClean defers to a confirming reviewer round each time, and
// the round cap terminates blocked ("still has N unresolved after N rounds"). This is
// the critical guard that the no-op-is-a-delivery-signal change does NOT let
// deliverable-looking-but-unresolved work through — maker≠checker holds end to end.
//
// The reviewer stub emits a session_id init line so round 2 RESUMES the reviewer
// session (production's real path) and is driven by the DEFERRAL reverify prompt — it
// dispatches on "code reviewer" (cold round 1) OR "Re-verify" (the resumed round). The
// still-reproducing finding must slip through neither.
const reviewRealNoDiffNeverCleanClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"code reviewer"* || "$prompt" == *"Re-verify"* ]]; then
  echo '{"type":"system","subtype":"init","session_id":"sess-rev"}'
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_FINDINGS {\"blockers\":[{\"file\":\"a.txt\",\"line\":1,\"issue\":\"still wrong\"}]}"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"review findings"* ]]; then
  echo '{"type":"result","subtype":"success","result":"nothing to change","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "content" > "a.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

func TestReviewRealNoDiffNeverClearsBlocksWithoutPR(t *testing.T) {
	c, _ := deliveryConductor(t, reviewRealNoDiffNeverCleanClaude)
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "2") // round 1 defers, round 2 hits the cap
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "blocked" }, 40*time.Second)
	if r.Status != "blocked" {
		t.Fatalf("a real finding the fixer never resolves must terminate blocked at the round cap: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error == "" {
		t.Fatal("an un-clearable review must record an honest error, not finish clean")
	}
	if !strings.Contains(strings.ToLower(r.Error), "unresolved") {
		t.Errorf("the error should name the unresolved findings at the round cap, got %q", r.Error)
	}
	// The defining safety property: no PR on a real finding never cleared.
	if r.PrURL != "" {
		t.Errorf("a never-cleared real finding must not open a PR, got %q", r.PrURL)
	}
	if len(r.PRs) != 0 {
		t.Errorf("no PR record should exist for a blocked review, got %+v", r.PRs)
	}
}

// The fix itself: a REAL finding that is STALE — the reviewer flags it in round 1,
// the fixer produces no diff (it is already satisfied at the integrated HEAD, e.g. an
// integration merge landed the fix after the review was computed), then the round-2
// CONFIRMING reviewer re-reviews at HEAD and returns REVIEW_CLEAN — must DELIVER, not
// false-block. This is exactly the telemetry incident (detritus#224): correct no-op
// remediation of an already-satisfied finding is a delivery signal.
//
// The reviewer stub emits a session_id init line so round 2 RESUMES (production's real
// path) and is driven by the DEFERRAL reverify prompt. That resumed round captures the
// exact prompt it received to $CANDYLAND_REVERIFY_PROMPT so the test can assert it is
// TRUTHFUL — it must NOT claim a fix was committed (which would bias the confirming
// reviewer toward clean), and must ask whether the finding still reproduces at HEAD.
const reviewStaleFindingThenCleanClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"code reviewer"* || "$prompt" == *"Re-verify"* ]]; then
  if [[ "$prompt" == *"Re-verify"* && -n "$CANDYLAND_REVERIFY_PROMPT" ]]; then
    printf '%s' "$prompt" > "$CANDYLAND_REVERIFY_PROMPT"
  fi
  echo '{"type":"system","subtype":"init","session_id":"sess-rev"}'
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
  echo '{"type":"result","subtype":"success","result":"already satisfied at the integrated HEAD; nothing to change","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "content" > "a.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

func TestReviewStaleRealFindingNoDiffThenReReviewCleanDelivers(t *testing.T) {
	c, _ := deliveryConductor(t, reviewStaleFindingThenCleanClaude)
	dir := t.TempDir()
	t.Setenv("CANDYLAND_REVIEW_COUNT", dir+"/n")
	t.Setenv("CANDYLAND_REVERIFY_PROMPT", dir+"/reverify-prompt")
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "3")
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("a stale real finding cleared by the confirming reviewer must DELIVER: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error != "" {
		t.Fatalf("a no-op remediation of an already-satisfied finding must not error, got %q", r.Error)
	}
	if r.PrURL == "" {
		t.Error("once the confirming reviewer re-verifies clean at HEAD, the run must open a PR")
	}
	// The confirming (resumed) reviewer round actually ran — proving production's
	// resume path, not a cold fork, cleared the finding.
	reverify, err := os.ReadFile(dir + "/reverify-prompt")
	if err != nil {
		t.Fatalf("the resumed confirming reviewer round did not run (no reverify prompt captured): %v", err)
	}
	rv := string(reverify)
	// Blocker 1 regression guard: the deferral reverify prompt must be TRUTHFUL — no
	// "fixes are committed / diff the new commits" lie when nothing was committed.
	for _, lie := range []string{"are committed", "diff the new commits"} {
		if strings.Contains(rv, lie) {
			t.Errorf("the deferral reverify prompt must not claim a fix was committed (found %q):\n%s", lie, rv)
		}
	}
	if !strings.Contains(rv, "made NO changes") || !strings.Contains(rv, "STILL reproduce") {
		t.Errorf("the deferral reverify prompt must truthfully state no fix landed and ask whether the finding still reproduces at HEAD:\n%s", rv)
	}
}
