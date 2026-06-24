package conductor

import (
	"context"
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
