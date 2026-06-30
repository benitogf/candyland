package conductor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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

// addWorktree creates a worktree at wtDir on branch (off base), so a coder can
// work in isolation while its siblings run in parallel. It first clears any
// leftover worktree/branch/dir at the same path — a quick stop→edit→begin (or an
// id reused after a restart) can leave the prior generation's worktree registered,
// which would make a plain `worktree add` fail with "already used by worktree".
// -B (create OR reset) then makes the branch a clean slate.
//
// A branch can be checked out in only ONE worktree, so `worktree add -B` also
// fails with "already used by worktree" when `branch` is held at a DIFFERENT
// path — e.g. a sibling child run's leftover integration worktree (campaign/quest
// children share one branch, campaign/<id>), or a stale/foreign checkout of that
// branch. Clearing only wtDir misses those, so addWorktree detaches every OTHER
// worktree on this branch first, making the add idempotent w.r.t. the branch. A
// holder with uncommitted changes is left untouched (the add then fails honestly
// rather than nuking unsaved work); the branch ref and its commits always survive
// — only the worktree registration is removed.
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
