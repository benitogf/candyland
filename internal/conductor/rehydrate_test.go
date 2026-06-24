package conductor

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ko"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/monotonic"
	"github.com/benitogf/ooo/storage"
)

// After a backend restart the in-memory run map is empty, but runs persist in
// ooo. Controls (Restart/Edit) must still find such a run by rehydrating it from
// storage — otherwise they 409 on any run that outlived the process that ran it
// (the real bug: "can't restart a failed task" after restarting candyland).
func TestTrackedRehydratesFromStorage(t *testing.T) {
	dir := t.TempDir()
	st := storage.New(storage.LayeredConfig{
		Memory:   storage.NewMemoryLayer(),
		Embedded: ko.NewEmbeddedStorage(filepath.Join(dir, "data")),
	})
	srv := &ooo.Server{Storage: st}
	monotonic.Init() // the real binary inits this in server.Start; storage.Set needs it
	if err := st.Start(storage.Options{}); err != nil {
		t.Fatalf("storage start: %v", err)
	}
	defer st.Close()

	c := New(srv)

	// Persist a finished+failed run directly, as a prior process would have, with
	// NO entry in the in-memory map.
	r := run.Run{ID: "r9", Status: "done", Error: "boom", Mode: "developer", Workspace: "ws", Prompt: "x", Agents: []run.Agent{}, Tasks: []run.Task{}}
	b, _ := json.Marshal(r)
	if _, err := st.Set("runs/r9", b); err != nil {
		t.Fatalf("persist run: %v", err)
	}
	if _, ok := c.Get("r9"); ok {
		t.Fatal("precondition: r9 must not be tracked in memory yet")
	}

	// tracked rehydrates it from storage…
	if rt := c.tracked("r9"); rt == nil {
		t.Fatal("tracked should rehydrate a persisted run that isn't in memory")
	}
	got, ok := c.Get("r9")
	if !ok || got.Prompt != "x" || got.Workspace != "ws" {
		t.Errorf("rehydrated run wrong/missing: ok=%v %+v", ok, got)
	}
	// …and an unknown run is still nil (not in memory, not in storage).
	if c.tracked("nope") != nil {
		t.Error("tracked should return nil for a run that's neither tracked nor stored")
	}
}
