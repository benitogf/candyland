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

// GET /api/runs/{id}/watch: 404 for an unknown run, 204 while a run has no watch
// phase, and 200 with the WatchState once the watch phase is present.
func TestRunWatchEndpoint(t *testing.T) {
	c, srv := questServer(t)
	base := "http://" + srv.Address

	// Unknown run → 404.
	resp, err := http.Get(base + "/api/runs/nope/watch")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run watch: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	id := c.Create(run.Spec{Prompt: "ship and babysit", Folders: []string{"/repo"}, Deliver: run.DeliverBabysit})

	// No watch phase yet → 204.
	resp, err = http.Get(base + "/api/runs/" + id + "/watch")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("pre-watch: status = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()

	// Attach a watch state (as watchPR would), then it's served as 200 + JSON.
	c.Update(id, func(r *run.Run) {
		r.Watch = &run.WatchState{
			PR: 7, Repo: "repo", PRUrl: "https://x/pull/7", State: "watching",
			Ticks: []run.WatchTick{{ID: "w1", Decision: run.WatchWait, Detail: "no actionable review yet — waiting"}},
		}
	})
	resp, err = http.Get(base + "/api/runs/" + id + "/watch")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch: status = %d, want 200", resp.StatusCode)
	}
	var w run.WatchState
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if w.PR != 7 || w.State != "watching" || len(w.Ticks) != 1 {
		t.Fatalf("watch payload = %+v, want PR 7 / watching / 1 tick", w)
	}
}
