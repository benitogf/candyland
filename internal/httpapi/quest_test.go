package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// writeGhAuthStub drops an executable gh stub that prints statusOut for
// `gh auth status`, and points CANDYLAND_GH at it. Used to give the launch gate
// a delivery-capable (or deliberately incapable) gh independent of the host's.
func writeGhAuthStub(t *testing.T, statusOut string) {
	t.Helper()
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	script := "#!/usr/bin/env bash\n" +
		"if [[ \"$1\" == auth && \"$2\" == status ]]; then\n" +
		"cat <<'EOF'\n" + statusOut + "\nEOF\n" +
		"exit 0\nfi\nexit 0\n"
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_GH", gh)
}

func questServer(t *testing.T) (*conductor.Conductor, *ooo.Server) {
	t.Helper()
	// A delivery-capable gh by default, so the launch gate admits the
	// success-path tests regardless of the host's real gh scopes.
	writeGhAuthStub(t, "Logged in to github.com account tester\n- Token scopes: 'repo', 'workflow'")
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := conductor.New(srv)
	Register(srv, c, nil)
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

// feedback/review delivery requires an existing PR number; the create handler
// rejects them when targetPr is missing and accepts them (stamping Deliver+TargetPR
// onto the quest) when present.
func TestQuestCreateFeedbackRequiresTargetPR(t *testing.T) {
	c, srv := questServer(t)
	base := "http://" + srv.Address

	// Missing targetPr → 400 for feedback and review.
	for _, d := range []run.Delivery{run.DeliverFeedback, run.DeliverReview} {
		resp := post(t, base+"/api/quests", run.QuestSpec{Objective: "fix the PR", Folders: []string{"/repo"}, Deliver: d})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("deliver %q without targetPr: status = %d, want 400", d, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// With targetPr → 200, and the quest carries Deliver + TargetPR.
	resp := post(t, base+"/api/quests", run.QuestSpec{Objective: "fix the PR", Folders: []string{"/repo"}, Deliver: run.DeliverFeedback, TargetPR: 42})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feedback with targetPr: status = %d, want 200", resp.StatusCode)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	q, ok := c.GetQuest(created.ID)
	if !ok {
		t.Fatal("created quest not found")
	}
	if q.Deliver != run.DeliverFeedback || q.TargetPR != 42 {
		t.Fatalf("quest deliver/targetPr = %q/%d, want feedback/42", q.Deliver, q.TargetPR)
	}
}

// The quest REST surface mirrors the run endpoints: create returns {id}, the
// snapshot is served from storage, and stop is the terminal lifecycle control.
func TestQuestEndpointsLifecycle(t *testing.T) {
	_, srv := questServer(t)
	base := "http://" + srv.Address

	// Create.
	resp := post(t, base+"/api/quests", run.QuestSpec{Objective: "tidy up", Folders: []string{"/repo"}})
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

	// Stop is terminal, with a reason.
	if r := post(t, base+"/api/quests/"+created.ID+"/stop", map[string]string{"reason": "fin"}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("stop status = %d, want 204", r.StatusCode)
	}
	get, _ = http.Get(base + "/api/quests/" + created.ID)
	_ = json.NewDecoder(get.Body).Decode(&q)
	get.Body.Close()
	if q.Status != "stopped" || q.PauseReason != "fin" {
		t.Fatalf("stop not applied: status=%q reason=%q", q.Status, q.PauseReason)
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
	if r := post(t, base+"/api/quests/nope/stop", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("stop unknown quest = %d, want 404", r.StatusCode)
	}
}
