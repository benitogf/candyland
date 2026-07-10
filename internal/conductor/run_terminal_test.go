package conductor

import (
	"os/exec"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// c-terminal-fidelity: branchDeliveryPushed counts only landed (empty-Err) branch
// records — zero means the child pushed nothing onto the quest branch.
func TestBranchDeliveryPushed(t *testing.T) {
	if n := branchDeliveryPushed(nil); n != 0 {
		t.Errorf("no records → 0 pushed, got %d", n)
	}
	allFailed := []run.PR{{Repo: "a", Err: "push failed"}, {Repo: "b", Err: "push failed"}}
	if n := branchDeliveryPushed(allFailed); n != 0 {
		t.Errorf("every push failed → 0 pushed, got %d", n)
	}
	partial := []run.PR{{Repo: "a"}, {Repo: "b", Err: "push failed"}}
	if n := branchDeliveryPushed(partial); n != 1 {
		t.Errorf("one landed branch → 1 pushed, got %d", n)
	}
}

// removeOrigin drops the repo's 'origin' remote so any push fails — the way to
// force a delivery-stage capability failure in a test.
func removeOrigin(t *testing.T, repo string) {
	t.Helper()
	cmd := exec.Command("git", "remote", "remove", "origin")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote remove origin: %v\n%s", err, out)
	}
}

// c-terminal-fidelity: a branch-delivered (quest-owned) child whose push
// onto the quest branch fails for EVERY repo must NOT be a silent success — it
// records an honest delivery error, blocks the tech lead, never claims the terminal
// PR phase, and carries a schema-valid postmortem (E2). The push fails because the
// repo's 'origin' remote is removed before the run delivers.
func TestBranchDeliveryPushFailureNeverSilentSuccess(t *testing.T) {
	c, repo := deliveryConductor(t, fanOutClaude)
	removeOrigin(t, repo)

	id := c.Create(run.Spec{Prompt: "do the thing", Folders: []string{repo}})
	c.Update(id, func(r *run.Run) {
		r.Deliver = run.DeliverBranch
		r.Branch = "quest/x"
	})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("run never terminated: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error == "" {
		t.Fatal("a branch push that failed for every repo must record an honest error, not finish clean")
	}
	if r.Phase == run.PhasePR {
		t.Errorf("a run that pushed nothing must not claim the terminal PR phase (phase=%d)", r.Phase)
	}
	if ok, why := validatePostmortem(r.Postmortem); !ok {
		t.Fatalf("a blocked branch-delivery run must carry a schema-valid postmortem: %+v (%s)", r.Postmortem, why)
	}
}
