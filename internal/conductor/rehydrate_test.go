package conductor

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

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
	r := run.Run{ID: "r9", Status: "done", Error: "boom", Prompt: "x", Agents: []run.Agent{}, Tasks: []run.Task{}}
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
	if !ok || got.Prompt != "x" {
		t.Errorf("rehydrated run wrong/missing: ok=%v %+v", ok, got)
	}
	// …and an unknown run is still nil (not in memory, not in storage).
	if c.tracked("nope") != nil {
		t.Error("tracked should return nil for a run that's neither tracked nor stored")
	}
}

// The conductor-wide usage-limit gate lives only in memory, so a restart loses
// it. When tracked rehydrates a run that was paused mid-limit-wait (status
// "paused" with a future resumeAt), it must re-arm the gate from that persisted
// resumeAt — otherwise post-restart spawns would fire into the still-closed limit
// window. A past resumeAt is a no-op (the gate is never pulled earlier).
func TestRehydrateReArmsLimitGate(t *testing.T) {
	dir := t.TempDir()
	st := storage.New(storage.LayeredConfig{
		Memory:   storage.NewMemoryLayer(),
		Embedded: ko.NewEmbeddedStorage(filepath.Join(dir, "data")),
	})
	srv := &ooo.Server{Storage: st}
	monotonic.Init()
	if err := st.Start(storage.Options{}); err != nil {
		t.Fatalf("storage start: %v", err)
	}
	defer st.Close()

	c := New(srv)

	future := time.Now().Add(2 * time.Hour).UTC().Round(time.Second)
	r := run.Run{ID: "r7", Status: "paused", ResumeAt: future.Format(time.RFC3339), Prompt: "x", Agents: []run.Agent{}, Tasks: []run.Task{}}
	b, _ := json.Marshal(r)
	if _, err := st.Set("runs/r7", b); err != nil {
		t.Fatalf("persist run: %v", err)
	}

	// Precondition: a fresh conductor's gate is unarmed.
	if !c.limitDeadline().IsZero() {
		t.Fatal("precondition: a fresh conductor must have an unarmed limit gate")
	}

	if rt := c.tracked("r7"); rt == nil {
		t.Fatal("tracked should rehydrate the paused run")
	}
	got := c.limitDeadline()
	if !got.Equal(future) {
		t.Errorf("rehydrate must re-arm the gate to the persisted resumeAt: got %v want %v", got, future)
	}
}

// On restart ReconcileOrphans closes out non-terminal phantom runs (no executor
// survives a process) but must leave terminal history intact — a deliberately
// cancelled run is genuine history, not a phantom, so rewriting it to
// "done"/Interrupted would corrupt what the user actually did.
func TestReconcileOrphansClosesPhantomsKeepsTerminal(t *testing.T) {
	dir := t.TempDir()
	st := storage.New(storage.LayeredConfig{
		Memory:   storage.NewMemoryLayer(),
		Embedded: ko.NewEmbeddedStorage(filepath.Join(dir, "data")),
	})
	srv := &ooo.Server{Storage: st}
	monotonic.Init()
	if err := st.Start(storage.Options{}); err != nil {
		t.Fatalf("storage start: %v", err)
	}
	defer st.Close()

	c := New(srv)

	seed := func(id, status, errStr string) {
		r := run.Run{ID: id, Status: status, Error: errStr, Prompt: "x", Agents: []run.Agent{}, Tasks: []run.Task{}}
		b, _ := json.Marshal(r)
		if _, err := st.Set("runs/"+id, b); err != nil {
			t.Fatalf("persist run %s: %v", id, err)
		}
	}
	read := func(id string) run.Run {
		obj, err := st.Get("runs/" + id)
		if err != nil {
			t.Fatalf("read run %s: %v", id, err)
		}
		var r run.Run
		if err := json.Unmarshal(obj.Data, &r); err != nil {
			t.Fatalf("unmarshal run %s: %v", id, err)
		}
		return r
	}

	seed("phantom", "running", "")     // left mid-flight by a dead process
	seed("cancelled", "cancelled", "") // deliberately cancelled — terminal history
	seed("finished", "done", "boom")   // already terminal with its own error

	c.ReconcileOrphans()

	if r := read("phantom"); r.Status != "done" || r.Error == "" {
		t.Errorf("phantom running run should be closed out: status=%q error=%q", r.Status, r.Error)
	}
	if r := read("cancelled"); r.Status != "cancelled" || r.Error != "" {
		t.Errorf("cancelled run must stay cancelled with no synthesized error: status=%q error=%q", r.Status, r.Error)
	}
	if r := read("finished"); r.Status != "done" || r.Error != "boom" {
		t.Errorf("finished run must keep its own terminal record: status=%q error=%q", r.Status, r.Error)
	}
}

// seq is in-memory and resets to 0 on restart. ReconcileOrphans must seed it past
// the highest persisted run id, or the first post-restart Create mints an id that
// already exists and Storage.Set (upsert) overwrites that run's record — silent
// history loss.
func TestReconcileSeedsSeqPastPersistedRuns(t *testing.T) {
	dir := t.TempDir()
	st := storage.New(storage.LayeredConfig{
		Memory:   storage.NewMemoryLayer(),
		Embedded: ko.NewEmbeddedStorage(filepath.Join(dir, "data")),
	})
	srv := &ooo.Server{Storage: st}
	monotonic.Init()
	if err := st.Start(storage.Options{}); err != nil {
		t.Fatalf("storage start: %v", err)
	}
	defer st.Close()

	// Two runs a prior process left behind (one terminal, one a higher-numbered
	// cancelled run — both must be counted even though the status checks skip them).
	for _, seed := range []run.Run{
		{ID: "r1", Status: "done", Prompt: "first", Agents: []run.Agent{}, Tasks: []run.Task{}},
		{ID: "r5", Status: "cancelled", Prompt: "fifth", Agents: []run.Agent{}, Tasks: []run.Task{}},
	} {
		b, _ := json.Marshal(seed)
		if _, err := st.Set("runs/"+seed.ID, b); err != nil {
			t.Fatalf("persist %s: %v", seed.ID, err)
		}
	}

	c := New(srv)
	c.ReconcileOrphans()

	// The next Create must skip past r5, not reuse r1.
	id := c.Create(run.Spec{Prompt: "new", Folders: []string{"/tmp"}})
	if id != "r6" {
		t.Errorf("post-restart Create reused/collided an id: got %q, want r6", id)
	}
	// The prior runs' records are intact (not overwritten).
	obj, err := st.Get("runs/r1")
	if err != nil {
		t.Fatalf("r1 record missing after reconcile+create: %v", err)
	}
	var r run.Run
	if err := json.Unmarshal(obj.Data, &r); err != nil {
		t.Fatalf("unmarshal r1: %v", err)
	}
	if r.Prompt != "first" {
		t.Errorf("r1 was overwritten: prompt=%q", r.Prompt)
	}
}

// questSeq is in-memory and resets to 0 on restart, exactly like runSeq.
// reconcileQuestSeq (called by ReconcileOrphans) must seed it past the highest
// persisted id, or the first post-restart CreateQuest mints an id that already
// exists and Storage.Set (upsert) overwrites that record — silent history loss.
// This mirrors TestReconcileSeedsSeqPastPersistedRuns for the quest sequence.
func TestReconcileSeedsSeqPastPersistedQuests(t *testing.T) {
	dir := t.TempDir()
	st := storage.New(storage.LayeredConfig{
		Memory:   storage.NewMemoryLayer(),
		Embedded: ko.NewEmbeddedStorage(filepath.Join(dir, "data")),
	})
	srv := &ooo.Server{Storage: st}
	monotonic.Init()
	if err := st.Start(storage.Options{}); err != nil {
		t.Fatalf("storage start: %v", err)
	}
	defer st.Close()

	// A prior process left two quests behind.
	for _, seed := range []run.Quest{
		{ID: "q1", Status: "done", Objective: "first quest", WorkItems: []run.WorkItem{}, Ticks: []run.Tick{}},
		{ID: "q2", Status: "done", Objective: "second quest", WorkItems: []run.WorkItem{}, Ticks: []run.Tick{}},
	} {
		b, _ := json.Marshal(seed)
		if _, err := st.Set("quests/"+seed.ID, b); err != nil {
			t.Fatalf("persist %s: %v", seed.ID, err)
		}
	}

	// A fresh Conductor over the SAME storage, as if the process just restarted.
	c := New(srv)
	c.ReconcileOrphans()

	// The next CreateQuest must skip past the persisted ids, not collide with q1.
	if id := c.CreateQuest(run.QuestSpec{Objective: "new", Folders: []string{"/tmp"}}); id != "q3" {
		t.Errorf("post-restart CreateQuest reused/collided an id: got %q, want q3", id)
	}

	// The prior records are intact (not overwritten by a colliding Create).
	obj, err := st.Get("quests/q1")
	if err != nil {
		t.Fatalf("q1 record missing after reconcile+create: %v", err)
	}
	var q run.Quest
	if err := json.Unmarshal(obj.Data, &q); err != nil {
		t.Fatalf("unmarshal q1: %v", err)
	}
	if q.Objective != "first quest" {
		t.Errorf("q1 was overwritten: objective=%q", q.Objective)
	}
}
