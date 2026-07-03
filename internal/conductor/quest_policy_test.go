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

// basicRunClaude drives a minimal run: tech lead → one coder → clean review → PR.
var basicRunClaude = stubClaude(
	roleCleanReviewer,
	role("tech lead", emitPartition(`[{"id":"a","title":"do it","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

// writeLoggingGh installs a stub gh that records every invocation to a log file and
// answers defaultBranchRef with `defaultBranch`. Returns the log path.
func writeLoggingGh(t *testing.T, defaultBranch string) string {
	t.Helper()
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	log := filepath.Join(dir, "gh.log")
	script := "#!/usr/bin/env bash\n" +
		"echo \"$@\" >> \"" + log + "\"\n" +
		"if [[ \"$*\" == *defaultBranchRef* ]]; then echo '" + defaultBranch + "'; exit 0; fi\n" +
		"echo 'https://github.com/example/repo/pull/7'\n"
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_GH", gh)
	return log
}

// q4 fix 1: a new PR's --base is the repo's FETCHED DEFAULT branch, never the
// current checkout. The repo is checked out on a feature branch (as q4's was:
// `--base feat/candyland-ux-overhaul`), yet the opened PR must target `main`.
func TestPRBaseIsDefaultBranchNotCheckout(t *testing.T) {
	c, repo := deliveryConductor(t, basicRunClaude)
	ghLog := writeLoggingGh(t, "main")

	// Put the repo on a NON-default checkout branch, the q4 failure condition.
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("checkout", "-q", "-b", "feat/candyland-ux-overhaul")

	id := c.Create(run.Spec{Prompt: "ship a change"})
	c.Begin(id)
	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" || r.Error != "" }, 60*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("run did not finish cleanly: status=%q error=%q", r.Status, r.Error)
	}

	b, err := os.ReadFile(ghLog)
	if err != nil {
		t.Fatalf("gh log unreadable: %v", err)
	}
	var createLine string
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "pr create") {
			createLine = ln
		}
	}
	if createLine == "" {
		t.Fatalf("gh pr create was never invoked; log:\n%s", b)
	}
	if !strings.Contains(createLine, "--base main") {
		t.Errorf("PR base must be the default branch: got %q, want --base main", createLine)
	}
	if strings.Contains(createLine, "feat/candyland-ux-overhaul") {
		t.Errorf("PR base must NOT be the checkout branch (q4 regression): %q", createLine)
	}
}

// q4 fix 2: stopping a quest/campaign persists a stopReason. An empty request
// reason defaults to "manual stop"; an explicit reason is kept.
func TestStopPersistsReason(t *testing.T) {
	c, _ := newQuestServer(t)

	q1 := c.CreateQuest(run.QuestSpec{Objective: "x", Folders: []string{"/repo"}})
	c.StopQuest(q1, "")
	if q, _ := c.GetQuest(q1); q.StopReason == "" {
		t.Error("StopQuest with no reason must default a stopReason (q4: recorded none)")
	}

	q2 := c.CreateQuest(run.QuestSpec{Objective: "y", Folders: []string{"/repo"}})
	c.StopQuest(q2, "manual stop from dashboard")
	if q, _ := c.GetQuest(q2); q.StopReason != "manual stop from dashboard" {
		t.Errorf("quest stopReason = %q, want %q", q.StopReason, "manual stop from dashboard")
	}

	cc, _ := newCampaignServer(t)
	c1 := cc.CreateCampaign(run.CampaignSpec{Input: "x", Folders: []string{"/repo"}})
	cc.StopCampaign(c1, "manual stop from dashboard")
	if cam, _ := cc.GetCampaign(c1); cam.StopReason != "manual stop from dashboard" {
		t.Errorf("campaign stopReason = %q, want %q", cam.StopReason, "manual stop from dashboard")
	}
}

// q4 fix 3: the own-artifacts triage guard drops any surfaced item that references
// the quest's own branch or an already-opened PR, so the loop can't feed on its own
// output (q4 ticks 3–10 reconcile/supersede loop).
func TestDropOwnArtifacts(t *testing.T) {
	q := run.Quest{
		ID:          "q9",
		Convergence: run.ConvergeConverge, // owns branch quest/q9
		Ticks:       []run.Tick{{ID: "t1", PRs: []run.PR{{Repo: "repo", URL: "https://github.com/x/repo/pull/42"}}}},
	}
	items := []questWorkItem{
		{Title: "genuine cleanup", Evidence: "a stale import in util.go"},
		{Title: "reconcile PR", Evidence: "supersede https://github.com/x/repo/pull/42 with a fresh one"},
		{Title: "rebase quest/q9", Evidence: "the branch quest/q9 has drifted"},
	}
	got := dropOwnArtifacts(q, items)
	if len(got) != 1 || got[0].Title != "genuine cleanup" {
		t.Fatalf("guard must keep only the genuine item, got %+v", got)
	}
}

// A tick whose discovery surfaces ONLY the quest's own open PR yields no accepted
// work — the guard drops it, so the loop terminates instead of reconciling itself.
func TestOwnArtifactTickSurfacesNothing(t *testing.T) {
	q := run.Quest{
		ID:          "q9",
		Convergence: run.ConvergePerFinding, // no owned branch; only the PR token
		Ticks:       []run.Tick{{ID: "t1", PRs: []run.PR{{Repo: "repo", URL: "https://github.com/x/repo/pull/7"}}}},
	}
	items := []questWorkItem{
		{Title: "address feedback", Evidence: "comments on https://github.com/x/repo/pull/7", Decision: "do"},
	}
	if acc := acceptedItems(dropOwnArtifacts(q, items)); len(acc) != 0 {
		t.Errorf("a tick that rediscovers only its own PR must surface nothing, got %+v", acc)
	}
}

// Title is stamped at creation: an explicit spec.Title wins; otherwise it is derived
// from the objective's first line (heading/prefix stripped, truncated).
func TestQuestTitleStamped(t *testing.T) {
	c, _ := newQuestServer(t)

	explicit, _ := c.GetQuest(c.CreateQuest(run.QuestSpec{Objective: "long objective text", Title: "Tidy imports"}))
	if explicit.Title != "Tidy imports" {
		t.Errorf("explicit title = %q, want %q", explicit.Title, "Tidy imports")
	}

	derived, _ := c.GetQuest(c.CreateQuest(run.QuestSpec{
		Objective: "# Objective: refactor the payment pipeline\n\nlots of detail follows across many paragraphs",
	}))
	if derived.Title == "" || strings.Contains(derived.Title, "\n") {
		t.Errorf("derived title must be a single short line, got %q", derived.Title)
	}
	if strings.HasPrefix(derived.Title, "#") || strings.HasPrefix(strings.ToLower(derived.Title), "objective:") {
		t.Errorf("derived title must strip heading/Objective prefix, got %q", derived.Title)
	}
	if derived.Title != "refactor the payment pipeline" {
		t.Errorf("derived title = %q, want %q", derived.Title, "refactor the payment pipeline")
	}
}

// Campaign Title is stamped the same way (explicit wins, else derived from input).
func TestCampaignTitleStamped(t *testing.T) {
	c, _ := newCampaignServer(t)
	explicit, _ := c.GetCampaign(c.CreateCampaign(run.CampaignSpec{Input: "big input", Title: "Payments epic"}))
	if explicit.Title != "Payments epic" {
		t.Errorf("explicit campaign title = %q, want %q", explicit.Title, "Payments epic")
	}
	derived, _ := c.GetCampaign(c.CreateCampaign(run.CampaignSpec{Input: "Goal: overhaul billing\n\ndetail"}))
	if derived.Title != "overhaul billing" {
		t.Errorf("derived campaign title = %q, want %q", derived.Title, "overhaul billing")
	}
}
