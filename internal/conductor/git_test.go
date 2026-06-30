package conductor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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

// Campaign/quest children share ONE branch (campaign/<id>) and integrate
// sequentially, each via its own integration worktree. If a sibling's worktree
// (or any stale/foreign checkout) still holds the shared branch at a different
// path, a plain `worktree add -B` fails with "already used by worktree" — the
// bug that blocked every child run after the first. addWorktree must detach the
// other (clean) holder and succeed.
func TestAddWorktreeSharedBranchOtherHolder(t *testing.T) {
	repo := newGitRepo(t)
	ctx := context.Background()
	shared := "campaign/c1"

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
	shared := "campaign/c1"

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
