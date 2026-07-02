package httpapi

import (
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

// GET /api/runs/{id}/trace returns the run's normalized trace in a stable shape:
// the schema version, the stored Run (with its parent-link fields present), and
// no audit when the run hasn't been audited.
func TestTraceEndpointStableShape(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := conductor.New(srv)
	Register(srv, c)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(os.Interrupt)

	id := c.Create(run.Spec{Prompt: "build the thing", Folders: []string{"/tmp"}})

	resp, err := http.Get("http://" + srv.Address + "/api/runs/" + id + "/trace")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var trace run.RunTrace
	if err := json.NewDecoder(resp.Body).Decode(&trace); err != nil {
		t.Fatal(err)
	}
	if trace.TraceVersion != run.TraceVersion {
		t.Errorf("traceVersion = %d, want %d", trace.TraceVersion, run.TraceVersion)
	}
	if trace.Run == nil || trace.Run.ID != id {
		t.Fatalf("trace.Run not the created run: %+v", trace.Run)
	}
	// Original intent is preserved at creation and equals the launch prompt.
	if trace.Run.OriginalIntent != "build the thing" {
		t.Errorf("originalIntent = %q, want %q", trace.Run.OriginalIntent, "build the thing")
	}
	// Parent-link fields exist (empty for a standalone run) so a later phase can
	// populate them without a migration.
	if trace.Run.QuestID != "" || trace.Run.CampaignID != "" {
		t.Errorf("standalone run should have empty parent links, got quest=%q campaign=%q", trace.Run.QuestID, trace.Run.CampaignID)
	}
	// A freshly created, never-finished run has no audit attached.
	if trace.Audit != nil {
		t.Errorf("expected no audit on a never-finished run, got %+v", trace.Audit)
	}
}

// The trace endpoint 404s for a run that does not exist.
func TestTraceEndpointNotFound(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := conductor.New(srv)
	Register(srv, c)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(os.Interrupt)

	resp, err := http.Get("http://" + srv.Address + "/api/runs/nope/trace")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// OriginalIntent is set once at creation from the initial ask — review can compare
// output against the original request.
func TestOriginalIntentSetAtCreation(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := conductor.New(srv)

	id := c.Create(run.Spec{Prompt: "first ask", Folders: []string{"/tmp"}})
	r, ok := c.Get(id)
	if !ok {
		t.Fatal("run not found after create")
	}
	if r.OriginalIntent != "first ask" {
		t.Errorf("originalIntent = %q, want %q", r.OriginalIntent, "first ask")
	}
}
