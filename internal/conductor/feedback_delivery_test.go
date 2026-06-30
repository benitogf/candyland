package conductor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// === V3: verdict-integrity gate ===========================================

// cleanVerdictContradictsNarration is a pure detector — a REVIEW_CLEAN whose own
// narration admits a blocker-class defect or hedges (no proof the change works)
// must be rejected, never accepted as clean.
func TestCleanVerdictContradictsNarration(t *testing.T) {
	clean := []string{
		"I ran the binary and traced the feature reachable from main; the consumer calls it.",
		"REVIEW_CLEAN",
	}
	for _, s := range clean {
		if bad, reason := cleanVerdictContradictsNarration(s); bad {
			t.Errorf("clean narration %q wrongly flagged: %s", s, reason)
		}
	}
	bad := []string{
		"the new handler is not wired into the router",
		"this is dead code with no consumer",
		"the path is unreachable from the entrypoint",
		"this plausibly works",
		"it should be fine since the sibling branch has it",
		"presumably the consumer calls it",
		"it probably works, seems correct",
	}
	for _, s := range bad {
		if flagged, _ := cleanVerdictContradictsNarration(s); !flagged {
			t.Errorf("self-contradicting/hedged narration %q must be flagged, was not", s)
		}
	}
}

// hedgedCleanReviewerClaude: the reviewer NARRATES a hedge ("plausibly … sibling
// branch") then stamps REVIEW_CLEAN on its FIRST spawn; on its SECOND spawn it
// returns a genuine REVIEW_CLEAN (no hedge). The V3 gate must REJECT the first
// (consuming a round, driving a fix pass) and accept the second.
const hedgedThenCleanReviewerClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"code reviewer"* ]]; then
  n=$(cat "$CANDYLAND_REVIEW_COUNT" 2>/dev/null || echo 0)
  n=$((n+1)); echo "$n" > "$CANDYLAND_REVIEW_COUNT"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  if [[ "$n" -le 1 ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"the feature plausibly works since the sibling branch wires it"}]}}'
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  else
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"I assembled and ran the binary; the router registers the handler and it is reachable from main"}]}}'
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  fi
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"review findings"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file":"a.txt"}}]}}'
  printf 'wired in per review\n' >> "a.txt"
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

// A hedged/self-contradicting REVIEW_CLEAN must NOT be accepted: it bounces back as
// a synthesized blocker, drives a fix→re-review round, and only the un-hedged
// REVIEW_CLEAN on the next round lets the PR open.
func TestHedgedCleanVerdictRejected(t *testing.T) {
	c, repo := deliveryConductor(t, hedgedThenCleanReviewerClaude)
	t.Setenv("CANDYLAND_REVIEW_COUNT", t.TempDir()+"/n")
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "3")
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("run did not finish clean: status=%q error=%q", r.Status, r.Error)
	}
	// The reviewer spawned MORE THAN ONCE: the hedged round-1 CLEAN was rejected and
	// a second review round ran (an accepted round-1 CLEAN would never re-spawn).
	count, _ := os.ReadFile(os.Getenv("CANDYLAND_REVIEW_COUNT"))
	if strings.TrimSpace(string(count)) == "1" {
		t.Fatalf("hedged REVIEW_CLEAN was accepted on round 1 (review count=%s) — V3 gate did not reject it", strings.TrimSpace(string(count)))
	}
	// The fix pass committed onto the run branch (the bounce drove a real fix).
	out, err := exec.Command("git", "-C", repo, "show", r.Branch+":a.txt").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "wired in per review") {
		t.Fatalf("fix pass did not land on the run branch: %v\n%s", err, out)
	}
}

// === D1/D2: feedback + review delivery ====================================

// writeFeedbackGh drops a stub `gh` that records every invocation to $CANDYLAND_GH_LOG
// and answers the PR-view queries a feedback/review run makes:
//   - `pr view N --json headRefName --jq .headRefName` → the head branch name
//   - `pr view N --json url --jq .url`                 → a PR URL
//   - `pr create …`                                    → recorded (and would print a NEW url)
func writeFeedbackGh(t *testing.T, headBranch string) {
	t.Helper()
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	log := filepath.Join(dir, "gh.log")
	script := "#!/usr/bin/env bash\n" +
		"echo \"$@\" >> \"" + log + "\"\n" +
		"if [[ \"$*\" == *headRefName* ]]; then echo '" + headBranch + "'; exit 0; fi\n" +
		"if [[ \"$*\" == *\"--json url\"* ]]; then echo 'https://github.com/example/repo/pull/42'; exit 0; fi\n" +
		"if [[ \"$1\" == pr && \"$2\" == create ]]; then echo 'https://github.com/example/repo/pull/99'; exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_GH", gh)
	t.Setenv("CANDYLAND_GH_LOG", log)
}

// pushHeadBranch creates `head` on the repo's origin (the PR's head branch a
// feedback run bases its work on and pushes back onto).
func pushHeadBranch(t *testing.T, repo, head string) {
	t.Helper()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("branch", head)
	runGit("push", "origin", head)
}

// seedHeadBranch creates `head` on origin carrying file=content (committed), so a
// run that re-writes the same content produces no diff against the PR head.
func seedHeadBranch(t *testing.T, repo, head, file, content string) {
	t.Helper()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("checkout", "-b", head)
	if err := os.WriteFile(filepath.Join(repo, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "seed PR head")
	runGit("push", "origin", head)
	runGit("checkout", "main")
}

// ghCreateInvoked reports whether the stub gh ever ran `pr create`.
func ghCreateInvoked(t *testing.T) bool {
	t.Helper()
	b, err := os.ReadFile(os.Getenv("CANDYLAND_GH_LOG"))
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "pr create") {
			return true
		}
	}
	return false
}

// feedbackClaude drives a full run (tech lead → coder → clean review) so there are
// real commits to push onto the target PR's head branch.
var feedbackClaude = stubClaude(
	roleCleanReviewer,
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

// D1: a feedback run updates the EXISTING PR (pushes onto its head branch) and
// opens NO new PR.
func TestFeedbackDeliveryUpdatesExistingPR(t *testing.T) {
	const head = "feature/existing-pr"
	c, repo := deliveryConductor(t, feedbackClaude)
	writeFeedbackGh(t, head)
	pushHeadBranch(t, repo, head)

	id := c.Create(run.Spec{Prompt: "address the review"})
	c.Update(id, func(r *run.Run) { r.Deliver = run.DeliverFeedback; r.TargetPR = 42 })
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("feedback run did not finish clean: status=%q error=%q", r.Status, r.Error)
	}
	if ghCreateInvoked(t) {
		t.Fatal("feedback delivery must NOT open a new PR (gh pr create was invoked)")
	}
	if r.PrURL != "https://github.com/example/repo/pull/42" {
		t.Fatalf("feedback run must record the EXISTING PR url, got %q", r.PrURL)
	}
	// The run's commits landed on the target PR's head branch on origin.
	out, err := exec.Command("git", "-C", repo, "ls-tree", "--name-only", "origin/"+head).CombinedOutput()
	if err != nil {
		// origin is a bare repo in the same root; fetch then inspect the ref.
		_ = exec.Command("git", "-C", repo, "fetch", "origin").Run()
		out, err = exec.Command("git", "-C", repo, "ls-tree", "--name-only", "origin/"+head).CombinedOutput()
	}
	if err != nil || !strings.Contains(string(out), "a.txt") {
		t.Fatalf("feedback work did not land on PR head %q: %v\n%s", head, err, out)
	}
}

// reviewNoFindingsClaude drives a run whose reviewer returns REVIEW_CLEAN with no
// findings — and (critically) the tech lead emits NO partition work, so nothing is
// applied. Actually the simplest no-findings review: a clean reviewer + a partition
// that produces a no-op… instead we model "no actionable findings" as a run that
// integrates nothing onto the PR head. We reuse feedbackClaude but assert review
// terminal semantics via the helper below.

// D2: a review run with no applied findings ends "reviewed (no actionable findings)"
// and opens NO PR (empty prUrl by design).
func TestReviewDeliveryNoFindings(t *testing.T) {
	const head = "feature/existing-pr"
	// The coder writes a.txt with content the PR head ALREADY contains, so the diff
	// between the PR head and the integrated branch is empty — no actionable findings
	// applied. (The coder must make an edit, else the resilience layer fails the run
	// for taking no action; an identical edit is a no-op delta.)
	const sameContent = "already on the PR head\n"
	writeSame := emitText("reviewing") +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Write\",\"input\":{\"file\":\"a.txt\"}}]}}'\n" +
		"printf '" + sameContent + "' > a.txt\n"
	noEditClaude := stubClaude(
		roleCleanReviewer,
		role("tech lead", emitPartition(`[{"id":"a","title":"look only","files":["a.txt"],"test":"t"}]`)),
		coder(writeSame, emitTest(1, 0)),
	)
	c, repo := deliveryConductor(t, noEditClaude)
	writeFeedbackGh(t, head)
	// Seed the PR head with the SAME a.txt the coder will write, so the integrated
	// branch carries no content delta against it.
	seedHeadBranch(t, repo, head, "a.txt", sameContent)

	id := c.Create(run.Spec{Prompt: "just review"})
	c.Update(id, func(r *run.Run) { r.Deliver = run.DeliverReview; r.TargetPR = 42 })
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("review run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	if ghCreateInvoked(t) {
		t.Fatal("review delivery must NOT open a PR (gh pr create was invoked)")
	}
	if r.PrURL != "" {
		t.Fatalf("review-only run must have empty prUrl, got %q", r.PrURL)
	}
	if r.StatusLine != "reviewed (no actionable findings)" {
		t.Fatalf("review-only terminal statusLine = %q, want %q", r.StatusLine, "reviewed (no actionable findings)")
	}
}
