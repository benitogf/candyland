package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/candyland/internal/winproc"
)

// Real git/gh plumbing the claude executor uses to turn agents' edits into a
// pull request. Everything shells out so the behavior matches what a developer
// would do by hand; the gh binary is overridable for tests via CANDYLAND_GH (and
// a local `origin` remote), so the whole branch → worktree → integrate → push →
// PR path is verifiable without touching GitHub or spending Claude tokens.

// ghBin is the GitHub CLI binary; overridable for tests via CANDYLAND_GH.
func ghBin() string {
	if b := os.Getenv("CANDYLAND_GH"); b != "" {
		return b
	}
	return "gh"
}

// expandHome expands a leading ~ to the user's home directory.
func expandHome(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// runCmd runs a command in dir and returns combined output (trimmed) + error.
// The error wraps the output so a caller logging it gets the actual git message.
func runCmd(ctx context.Context, dir, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	winproc.Configure(cmd) // windowless: no flashing console for git/gh on Windows
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, s)
	}
	return s, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return runCmd(ctx, dir, "git", args...)
}

// isGitRepo reports whether dir is inside a git work tree.
func isGitRepo(ctx context.Context, dir string) bool {
	out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// currentBranch returns the repo's checked-out branch (the run's PR base).
func currentBranch(ctx context.Context, dir string) (string, error) {
	return git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// defaultBranch resolves the repo's DEFAULT branch — the base every new PR must
// target, never the current checkout (q4 fix: PRs failed with
// `--base feat/candyland-ux-overhaul` because the base came from the checked-out
// branch; mirrors /gh convention 6). It asks gh for the repo's default branch
// first, then falls back to origin/HEAD's symbolic ref. An error is returned when
// neither resolves, so a caller can degrade to currentBranch rather than open a PR
// against a wrong base silently.
func defaultBranch(ctx context.Context, repo string) (string, error) {
	if out, err := runCmd(ctx, repo, ghBin(), "repo", "view", "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name"); err == nil {
		if b := strings.TrimSpace(out); b != "" {
			return b, nil
		}
	}
	if out, err := git(ctx, repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); b != "" {
			return b, nil
		}
	}
	return "", fmt.Errorf("could not resolve the default branch for %s", repoBase(repo))
}

// prBase resolves the base branch a new PR must target in repo: the repo's fetched
// default branch, falling back to the current checkout only if the default can't be
// resolved (better a PR than none). It is the single site every new-PR open uses.
func prBase(ctx context.Context, repo string) string {
	if b, err := defaultBranch(ctx, repo); err == nil {
		return b
	}
	b, _ := currentBranch(ctx, repo)
	return b
}

// addWorktree creates a worktree at wtDir on branch (off base), so a coder can
// work in isolation while its siblings run in parallel. It first clears any
// leftover worktree/branch/dir at the same path — a quick stop→edit→begin (or an
// id reused after a restart) can leave the prior generation's worktree registered,
// which would make a plain `worktree add` fail with "already used by worktree".
// -B (create OR reset) then makes the branch a clean slate.
//
// A branch can be checked out in only ONE worktree, so `worktree add -B` also
// fails with "already used by worktree" when `branch` is held at a DIFFERENT
// path — e.g. a sibling child run's leftover integration worktree (a quest's
// children share one branch: quest/<id>), or a stale/foreign checkout of that branch.
// Clearing only wtDir misses those, so addWorktree detaches every OTHER worktree
// on this branch first, making the add idempotent w.r.t. the branch. An OTHER-path
// holder with uncommitted changes is left untouched (the add then fails honestly
// rather than nuking unsaved work), and a detached clean holder keeps its commits;
// the leftover at wtDir itself is cleared unconditionally, and `branch -D` + `-B`
// reset the branch ref to base, so callers use addWorktree only for run-scoped
// task/tl branches; the shared parent branch is never added here — integration
// builds in a DETACHED worktree at the accumulated tip (addDetachedWorktree) and
// re-points the branch via setBranchRef, which this reset therefore never touches.
func addWorktree(ctx context.Context, repo, wtDir, branch, base string) error {
	_, _ = git(ctx, repo, "worktree", "remove", "--force", wtDir)
	for _, other := range worktreesForBranch(ctx, repo, branch) {
		if other != wtDir && !hasChanges(ctx, other) {
			_, _ = git(ctx, repo, "worktree", "remove", "--force", other)
		}
	}
	_, _ = git(ctx, repo, "worktree", "prune")
	_, _ = git(ctx, repo, "branch", "-D", branch)
	_ = os.RemoveAll(wtDir) // drop any orphan directory left by a crashed prior run
	_, err := git(ctx, repo, "worktree", "add", "-B", branch, wtDir, base)
	return err
}

// addDetachedWorktree creates a worktree at wtDir checked out DETACHED at ref (a
// SHA or branch name), so the integration flow can build on a base without ever
// holding `branch` in a worktree. A branch may be checked out in only ONE
// worktree, so a plain `worktree add -B branch` collides ("already used by
// worktree") when a sibling child run's leftover integration worktree — or a
// stale/foreign checkout — still holds the shared branch at a different path. A
// detached checkout sidesteps that whole class of collision: it never claims the
// branch. The run branch is (re)pointed at the integrated tip afterwards via
// setBranchRef, which needs no checkout and so never collides either.
//
// Like addWorktree it clears any leftover worktree/dir at the same path first, so
// a quick stop→edit→begin (or an id reused after a restart) starts clean.
func addDetachedWorktree(ctx context.Context, repo, wtDir, ref string) error {
	_, _ = git(ctx, repo, "worktree", "remove", "--force", wtDir)
	_, _ = git(ctx, repo, "worktree", "prune")
	_ = os.RemoveAll(wtDir) // drop any orphan directory left by a crashed prior run
	_, err := git(ctx, repo, "worktree", "add", "--detach", wtDir, ref)
	return err
}

// setBranchRef (re)points refs/heads/branch at sha using the low-level update-ref,
// which — unlike `git branch -f` — moves a branch even while it is checked out (or
// dirty) in ANOTHER worktree. That is exactly the stale/sibling holder that made
// `worktree add -B` collide; here the detached integration worktree owns the
// commits and update-ref just publishes them under the branch name, so downstream
// push/PR (which read the local branch ref) see the integrated tip.
func setBranchRef(ctx context.Context, dir, branch, sha string) error {
	_, err := git(ctx, dir, "update-ref", "refs/heads/"+branch, sha)
	return err
}

// syncBranchRef points branch at the worktree's current HEAD — called after each
// integration/review-fix commit lands on the detached integration worktree so the
// local branch ref (what push/PR resolve) tracks the accumulated work.
func syncBranchRef(ctx context.Context, wtDir, branch string) error {
	sha, err := git(ctx, wtDir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	return setBranchRef(ctx, wtDir, branch, sha)
}

// isWorktreeCollision reports whether err is git's "already used by worktree"
// refusal — a branch held by another (stale/sibling/foreign) worktree. It is
// RETRYABLE, never terminal: a re-plan re-derives the base off the current branch
// tip, and the detached integration worktree avoids the collision entirely, so the
// run must never hard-block on it.
func isWorktreeCollision(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already used by worktree")
}

// worktreesForBranch returns the worktree directories currently checked out on
// branch (normally zero or one). It parses `git worktree list --porcelain`, whose
// records are blank-line separated with a "worktree <path>" line and, for a
// non-detached checkout, a "branch refs/heads/<name>" line.
func worktreesForBranch(ctx context.Context, repo, branch string) []string {
	out, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	want := "branch refs/heads/" + branch
	var dirs []string
	var cur string
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			cur = path
		} else if line == want && cur != "" {
			dirs = append(dirs, cur)
		}
	}
	return dirs
}

// removeWorktree tears a worktree down (best-effort; --force handles dirty trees).
func removeWorktree(ctx context.Context, repo, wtDir string) {
	_, _ = git(ctx, repo, "worktree", "remove", "--force", wtDir)
}

// hasChanges reports whether dir's work tree has uncommitted changes.
func hasChanges(ctx context.Context, dir string) bool {
	out, err := git(ctx, dir, "status", "--porcelain")
	return err == nil && out != ""
}

// commitAll stages and commits everything in dir. Returns false (no error) when
// there was nothing to commit — an agent that made no edits is not a failure here
// (the resilience layer already judged whether it did real work).
func commitAll(ctx context.Context, dir, msg string) (bool, error) {
	if !hasChanges(ctx, dir) {
		return false, nil
	}
	if _, err := git(ctx, dir, "add", "-A"); err != nil {
		return false, err
	}
	if _, err := git(ctx, dir, "commit", "-m", msg); err != nil {
		return false, err
	}
	return true, nil
}

// mergeBranch merges branch into the currently checked-out branch in repo and
// returns the conflicted files alongside the verdict (so the caller never has to
// re-derive them — a second read could transiently fail and let the resolver run
// against an empty list, committing markers).
//   - clean merge → (false, nil, nil)
//   - merge conflict → (true, files, nil): the conflict is LEFT in the work tree so
//     the tech lead can reconcile it (a real integrator resolves conflicts; it
//     doesn't abandon the run). The caller resolves + completeMerge, or aborts.
//   - any other failure (including a conflict git won't enumerate) → (false, nil,
//     err): aborted, so the tree is never left dirty and the run fails honestly.
func mergeBranch(ctx context.Context, repo, branch string) (conflicted bool, files []string, err error) {
	if _, mErr := git(ctx, repo, "merge", "--no-ff", "--no-edit", branch); mErr == nil {
		return false, nil, nil
	} else if f := conflictedFiles(ctx, repo); len(f) > 0 {
		return true, f, nil // conflict — leave it in the tree for the resolver
	} else {
		_, _ = git(ctx, repo, "merge", "--abort")
		return false, nil, mErr
	}
}

// conflictedFiles lists the paths git left unmerged (relative to repo root).
func conflictedFiles(ctx context.Context, repo string) []string {
	out, err := git(ctx, repo, "diff", "--name-only", "--diff-filter=U")
	if err != nil || strings.TrimSpace(out) == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(out), "\n")
}

// unresolvedMarkers returns, of the given files, those that still contain git
// conflict markers — i.e. the resolver didn't actually reconcile them. A file the
// resolver deleted (a valid resolution) reads as resolved.
func unresolvedMarkers(repo string, files []string) []string {
	var bad []string
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(repo, f))
		if err != nil {
			continue // gone = resolved (deleted as the resolution)
		}
		s := string(b)
		if strings.Contains(s, "<<<<<<<") || strings.Contains(s, ">>>>>>>") {
			bad = append(bad, f)
		}
	}
	return bad
}

// completeMerge stages the resolved work tree and commits the in-progress merge.
func completeMerge(ctx context.Context, repo, msg string) error {
	if _, err := git(ctx, repo, "add", "-A"); err != nil {
		return err
	}
	if _, err := git(ctx, repo, "commit", "-m", msg); err != nil {
		return err
	}
	return nil
}

// abortMerge unwinds an in-progress merge (best-effort), used when a conflict
// couldn't be resolved so the run fails on a clean tree rather than a half-merge.
func abortMerge(repo string) {
	_, _ = git(context.Background(), repo, "merge", "--abort")
}

// pushBranch pushes branch to origin, setting upstream.
func pushBranch(ctx context.Context, repo, branch string) error {
	_, err := git(ctx, repo, "push", "-u", "origin", branch)
	return err
}

// openPR opens a pull request for the pushed branch via gh and returns its URL
// (gh prints the URL on stdout). base is the branch the run started from.
// commentPR adds a comment to an already-open PR (used to cross-link the sibling
// PRs of a multi-repo run). The cwd repo is the integration worktree the PR was
// opened from. Best-effort: the caller treats a failure as non-fatal.
func commentPR(ctx context.Context, repo, prURL, body string) error {
	_, err := runCmd(ctx, repo, ghBin(), "pr", "comment", prURL, "--body", body)
	return err
}

// prHeadBranch resolves an existing PR's head branch name via gh, so a feedback
// run can base its work on (and push back onto) that branch — updating the PR in
// place rather than opening a new one.
func prHeadBranch(ctx context.Context, repo string, n int) (string, error) {
	out, err := runCmd(ctx, repo, ghBin(), "pr", "view", strconv.Itoa(n), "--json", "headRefName", "--jq", ".headRefName")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(out)
	if head == "" {
		return "", fmt.Errorf("gh pr view %d produced no head branch", n)
	}
	return head, nil
}

// prURL resolves an existing PR's web URL via gh, so a feedback/review run records
// the PR it UPDATED as its delivery result (never opening a new one).
func prURL(ctx context.Context, repo string, n int) (string, error) {
	out, err := runCmd(ctx, repo, ghBin(), "pr", "view", strconv.Itoa(n), "--json", "url", "--jq", ".url")
	if err != nil {
		return "", err
	}
	url := strings.TrimSpace(out)
	if url == "" {
		return "", fmt.Errorf("gh pr view %d produced no URL", n)
	}
	return url, nil
}

// ghPRReview reads a watched PR's current review state via gh so the babysit watch
// loop can decide whether to merge, dispatch a feedback fix, or keep waiting. It
// resolves the PR's latest commit (headRefOid), the aggregate reviewDecision, and —
// from the individual reviews — the commit the latest APPROVED review was on (so an
// approval on a stale commit doesn't trigger a merge). Best-effort on the
// per-review commit: an absent oid leaves ApprovedSHA empty (→ treated as no
// current approval). Overridable for tests via Conductor.prReview.
func ghPRReview(ctx context.Context, repo string, n int) (run.PRReview, error) {
	out, err := runCmd(ctx, repo, ghBin(), "pr", "view", strconv.Itoa(n),
		"--json", "state,reviewDecision,headRefOid,reviews")
	if err != nil {
		return run.PRReview{}, err
	}
	var raw struct {
		State          string `json:"state"`
		ReviewDecision string `json:"reviewDecision"`
		HeadRefOid     string `json:"headRefOid"`
		Reviews        []struct {
			State  string `json:"state"`
			Commit struct {
				Oid string `json:"oid"`
			} `json:"commit"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return run.PRReview{}, fmt.Errorf("gh pr view %d produced unreadable JSON: %v", n, err)
	}
	pr := run.PRReview{State: raw.State, ReviewDecision: raw.ReviewDecision, HeadSHA: raw.HeadRefOid}
	// The LAST APPROVED review in gh's chronological list is the current approval.
	for _, rv := range raw.Reviews {
		if strings.EqualFold(rv.State, "APPROVED") {
			pr.ApprovedSHA = rv.Commit.Oid
		}
	}
	return pr, nil
}

// ghMergePR merges a watched PR via gh (squash-merge, deleting the head branch).
// Overridable for tests via Conductor.mergePR.
func ghMergePR(ctx context.Context, repo string, n int) error {
	_, err := runCmd(ctx, repo, ghBin(), "pr", "merge", strconv.Itoa(n), "--squash", "--delete-branch")
	return err
}

func openPR(ctx context.Context, repo, base, head, title, body string) (string, error) {
	out, err := runCmd(ctx, repo, ghBin(), "pr", "create",
		"--base", base, "--head", head, "--title", title, "--body", body)
	if err != nil {
		return "", err
	}
	// gh prints the PR URL as the last line of its output. Guard against an empty
	// result so a run never records "done" with a blank PR link.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	url := strings.TrimSpace(lines[len(lines)-1])
	if url == "" {
		return "", fmt.Errorf("gh pr create produced no URL")
	}
	return url, nil
}

// existingOpenPR returns the URL of an already-open PR whose head is `head`, if
// any — re-finishing a quest delivery must reuse it, never error on a
// duplicate `gh pr create`. A gh/list error or no match returns ("", false).
func existingOpenPR(ctx context.Context, repo, head string) (string, bool) {
	out, err := runCmd(ctx, repo, ghBin(), "pr", "list", "--head", head, "--state", "open", "--json", "url")
	if err != nil {
		return "", false
	}
	var prs []struct {
		URL string `json:"url"`
	}
	if json.Unmarshal([]byte(out), &prs) != nil || len(prs) == 0 {
		return "", false
	}
	url := strings.TrimSpace(prs[0].URL)
	return url, url != ""
}
