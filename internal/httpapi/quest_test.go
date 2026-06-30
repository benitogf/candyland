package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

func questServer(t *testing.T) (*conductor.Conductor, *ooo.Server) {
	t.Helper()
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := conductor.New(srv)
	Register(srv, c)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(os.Interrupt) })
	return c, srv
}

func post(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	resp, err := http.Post(url, "application/json", &buf)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// The quest REST surface mirrors the run endpoints: create returns {id}, the
// snapshot is served from storage, and pause/resume/stop drive the lifecycle.
func TestQuestEndpointsLifecycle(t *testing.T) {
	_, srv := questServer(t)
	base := "http://" + srv.Address

	// Create.
	resp := post(t, base+"/api/quests", run.QuestSpec{Objective: "tidy up", Folders: []string{"/repo"}, AutonomyLevel: run.AutonomyReportOnly})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d, want 200", resp.StatusCode)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("create did not return an id")
	}

	// Read snapshot.
	get, err := http.Get(base + "/api/quests/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var q run.Quest
	_ = json.NewDecoder(get.Body).Decode(&q)
	get.Body.Close()
	if q.ID != created.ID || q.Objective != "tidy up" || q.Status != "running" {
		t.Fatalf("snapshot wrong: %+v", q)
	}

	// Pause with a reason.
	if r := post(t, base+"/api/quests/"+created.ID+"/pause", map[string]string{"reason": "hold"}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("pause status = %d, want 204", r.StatusCode)
	}
	get, _ = http.Get(base + "/api/quests/" + created.ID)
	_ = json.NewDecoder(get.Body).Decode(&q)
	get.Body.Close()
	if q.Status != "paused" || q.PauseReason != "hold" {
		t.Fatalf("pause not applied: status=%q reason=%q", q.Status, q.PauseReason)
	}

	// Resume (a paused quest with no real claude will resume then settle — we only
	// assert the endpoint accepts it).
	if r := post(t, base+"/api/quests/"+created.ID+"/resume", nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("resume status = %d, want 204", r.StatusCode)
	}

	// Stop is terminal.
	if r := post(t, base+"/api/quests/"+created.ID+"/stop", map[string]string{"reason": "fin"}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("stop status = %d, want 204", r.StatusCode)
	}
	// A stopped quest can't begin.
	if r := post(t, base+"/api/quests/"+created.ID+"/begin", nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("begin on stopped quest = %d, want 409", r.StatusCode)
	}

	// Findings + child runs endpoints return arrays (empty here).
	for _, path := range []string{"/findings", "/runs"} {
		g, _ := http.Get(base + "/api/quests/" + created.ID + path)
		if g.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, g.StatusCode)
		}
		g.Body.Close()
	}
}

// Create validation: an objective and at least one folder are required (mirrors the
// run-create validation).
func TestQuestCreateValidation(t *testing.T) {
	_, srv := questServer(t)
	base := "http://" + srv.Address
	if r := post(t, base+"/api/quests", run.QuestSpec{Folders: []string{"/repo"}}); r.StatusCode != http.StatusBadRequest {
		t.Errorf("missing objective = %d, want 400", r.StatusCode)
	}
	if r := post(t, base+"/api/quests", run.QuestSpec{Objective: "x"}); r.StatusCode != http.StatusBadRequest {
		t.Errorf("missing folders = %d, want 400", r.StatusCode)
	}
}

// Unknown-quest reads/commands 404.
func TestQuestEndpointsNotFound(t *testing.T) {
	_, srv := questServer(t)
	base := "http://" + srv.Address
	if g, _ := http.Get(base + "/api/quests/nope"); g.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown quest = %d, want 404", g.StatusCode)
	}
	if r := post(t, base+"/api/quests/nope/pause", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("pause unknown quest = %d, want 404", r.StatusCode)
	}
}
