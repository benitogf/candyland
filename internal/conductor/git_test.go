package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The integration flow must survive the collision that used to hard-block every
// child run after the first: the run branch is held by another worktree (a
// sibling's leftover integration dir, a stale/foreign checkout) — even a DIRTY
// one that must not be force-removed. A DETACHED worktree at the base SHA never
// claims the branch, so the add always succeeds; setBranchRef then (re)points the
// branch at the integrated tip without a checkout, so nothing collides.
func TestDetachedWorktreeIntegratesPastBranchHolder(t *testing.T) {
	repo := newGitRepo(t)
	ctx := context.Background()
	branch := "quest/c1"

	// A DIRTY holder of the branch — the case addWorktree refuses to force-remove.
	held := filepath.Join(t.TempDir(), "held")
	if err := addWorktree(ctx, repo, held, branch, "main"); err != nil {
		t.Fatalf("setup holder failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(held, "dirty.txt"), []byte("unsaved\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	base, err := git(ctx, repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	integ := filepath.Join(t.TempDir(), "integrate")
	if err := addDetachedWorktree(ctx, repo, integ, base); err != nil {
		t.Fatalf("detached integration worktree must succeed past a branch holder: %v", err)
	}
	// The dirty holder is left intact (never force-removed).
	if _, err := os.Stat(filepath.Join(held, "dirty.txt")); err != nil {
		t.Fatalf("dirty holder's unsaved work must be preserved: %v", err)
	}
	// Produce an integrated commit and publish it under the branch.
	if err := os.WriteFile(filepath.Join(integ, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := git(ctx, integ, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(ctx, integ, "commit", "-m", "integrate"); err != nil {
		t.Fatal(err)
	}
	if err := syncBranchRef(ctx, integ, branch); err != nil {
		t.Fatalf("syncBranchRef must move a branch held elsewhere: %v", err)
	}
	tip, err := git(ctx, integ, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got, err := git(ctx, repo, "rev-parse", branch)
	if err != nil {
		t.Fatalf("resolve branch: %v", err)
	}
	if got != tip {
		t.Fatalf("branch %q should point at the integrated tip %q, got %q", branch, tip, got)
	}
}

// A real "already used by worktree" refusal is classified retryable, ordinary
// errors are not — so the run re-plans on a collision instead of hard-blocking.
func TestIsWorktreeCollision(t *testing.T) {
	if !isWorktreeCollision(errors.New("fatal: 'quest/c1' is already used by worktree at '/x'")) {
		t.Fatal("git's branch-checkout collision must classify as retryable")
	}
	if isWorktreeCollision(errors.New("fatal: not a git repository")) {
		t.Fatal("an unrelated error must not classify as a worktree collision")
	}
	if isWorktreeCollision(nil) {
		t.Fatal("nil must not classify as a worktree collision")
	}
}

// A Restart re-runs delivery from a clean slate, which means re-adding a worktree
// on a branch a prior attempt already created — including the run branch, which
// cleanup intentionally doesn't delete. addWorktree must therefore reset (not
// fail on) an existing branch, or a restarted run errors at integration.
func TestAddWorktreeRestartable(t *testing.T) {
	repo := newGitRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "wt")

	if err := addWorktree(ctx, repo, wt, "feat/x-r1", "main"); err != nil {
		t.Fatalf("first worktree add failed: %v", err)
	}
	removeWorktree(ctx, repo, wt)

	// Re-add the SAME branch after its worktree is gone (the restart case). With
	// `git worktree add -b` this fails ("branch already exists"); -B resets it.
	if err := addWorktree(ctx, repo, wt, "feat/x-r1", "main"); err != nil {
		t.Fatalf("restart re-add of an existing branch must succeed: %v", err)
	}
	removeWorktree(ctx, repo, wt)
}

// Quest children share ONE branch (quest/<id>) and integrate
// sequentially, each via its own integration worktree. If a sibling's worktree
// (or any stale/foreign checkout) still holds the shared branch at a different
// path, a plain `worktree add -B` fails with "already used by worktree" — the
// bug that blocked every child run after the first. addWorktree must detach the
// other (clean) holder and succeed.
func TestAddWorktreeSharedBranchOtherHolder(t *testing.T) {
	repo := newGitRepo(t)
	ctx := context.Background()
	shared := "quest/c1"

	first := filepath.Join(t.TempDir(), "r1", "integrate")
	if err := addWorktree(ctx, repo, first, shared, "main"); err != nil {
		t.Fatalf("first child's integration worktree failed: %v", err)
	}
	// The first worktree is still registered on `shared` (not yet removed) when
	// the next sibling integrates into its OWN dir on the SAME branch.
	second := filepath.Join(t.TempDir(), "r2", "integrate")
	if err := addWorktree(ctx, repo, second, shared, "main"); err != nil {
		t.Fatalf("sibling re-add of the shared branch must succeed (the collision bug): %v", err)
	}
	if got := worktreesForBranch(ctx, repo, shared); len(got) != 1 || got[0] != second {
		t.Fatalf("shared branch should be held by exactly the new worktree %q, got %v", second, got)
	}
	removeWorktree(ctx, repo, second)
}

// A holder with uncommitted changes must NOT be force-removed — addWorktree
// leaves it and fails honestly rather than nuking unsaved work.
func TestAddWorktreeSharedBranchSpareDirtyHolder(t *testing.T) {
	repo := newGitRepo(t)
	ctx := context.Background()
	shared := "quest/c1"

	held := filepath.Join(t.TempDir(), "held")
	if err := addWorktree(ctx, repo, held, shared, "main"); err != nil {
		t.Fatalf("setup worktree failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(held, "dirty.txt"), []byte("unsaved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	if err := addWorktree(ctx, repo, other, shared, "main"); err == nil {
		t.Fatal("expected failure: a dirty holder of the shared branch must be spared, not force-removed")
	}
	if got := worktreesForBranch(ctx, repo, shared); len(got) != 1 || got[0] != held {
		t.Fatalf("dirty holder %q must be left intact, got %v", held, got)
	}
}
