package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// A babysit run needs no targetPr (it opens and then owns the PR it watches), so the
// create handler accepts it and stamps Deliver=babysit onto the run.
func TestRunCreateBabysitNoTargetPR(t *testing.T) {
	c, srv := questServer(t)
	base := "http://" + srv.Address

	resp := post(t, base+"/api/runs", run.Spec{Prompt: "ship and babysit", Folders: []string{"/repo"}, Deliver: run.DeliverBabysit})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("babysit create: status = %d, want 200", resp.StatusCode)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	r, ok := c.Get(created.ID)
	if !ok {
		t.Fatal("created run not found")
	}
	if r.Deliver != run.DeliverBabysit {
		t.Fatalf("run deliver = %q, want babysit", r.Deliver)
	}
	if r.TargetPR != 0 {
		t.Fatalf("babysit targetPr = %d, want 0", r.TargetPR)
	}
}
