package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// A standalone run (POST /api/runs) can address feedback on an existing PR, just
// like quests/campaigns: feedback/review delivery requires a targetPr, and the
// created run carries Deliver + TargetPR through so the executor updates the PR
// in place instead of opening a new one.
func TestRunCreateFeedbackRequiresTargetPR(t *testing.T) {
	c, srv := questServer(t)
	base := "http://" + srv.Address

	// Missing targetPr → 400 for feedback and review.
	for _, d := range []run.Delivery{run.DeliverFeedback, run.DeliverReview} {
		resp := post(t, base+"/api/runs", run.Spec{Prompt: "fix the PR", Folders: []string{"/repo"}, Deliver: d})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("deliver %q without targetPr: status = %d, want 400", d, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// With targetPr → 200, and the run carries Deliver + TargetPR.
	resp := post(t, base+"/api/runs", run.Spec{Prompt: "fix the PR", Folders: []string{"/repo"}, Deliver: run.DeliverFeedback, TargetPR: 42})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feedback with targetPr: status = %d, want 200", resp.StatusCode)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	r, ok := c.Get(created.ID)
	if !ok {
		t.Fatal("created run not found")
	}
	if r.Deliver != run.DeliverFeedback || r.TargetPR != 42 {
		t.Fatalf("run deliver/targetPr = %q/%d, want feedback/42", r.Deliver, r.TargetPR)
	}
}

// A plain run (no deliver) still defaults to the standard new-PR-per-repo
// delivery — the feedback wiring must not change the default path.
func TestRunCreateDefaultsToPR(t *testing.T) {
	c, srv := questServer(t)
	base := "http://" + srv.Address

	resp := post(t, base+"/api/runs", run.Spec{Prompt: "build a thing", Folders: []string{"/repo"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plain run: status = %d, want 200", resp.StatusCode)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	r, ok := c.Get(created.ID)
	if !ok {
		t.Fatal("created run not found")
	}
	if r.Deliver != "" && r.Deliver != run.DeliverPR {
		t.Fatalf("plain run deliver = %q, want empty/pr", r.Deliver)
	}
	if r.TargetPR != 0 {
		t.Fatalf("plain run targetPr = %d, want 0", r.TargetPR)
	}
}
