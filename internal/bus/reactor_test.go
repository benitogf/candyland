package bus

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/io"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// startServer stands up an embedded ooo server (Static, like production) with
// the bus registered for the given agents, started on an ephemeral port.
func startServer(t *testing.T, orchestrator string, agents ...string) (*ooo.Server, *Bus, func()) {
	t.Helper()
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	b := NewBus(orchestrator, CursorReader(srv))
	b.RegisterGlobal(srv)
	for _, a := range agents {
		b.RegisterAgent(srv, a)
	}
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatalf("start: %v", err)
	}
	return srv, b, func() { srv.Close(os.Interrupt) }
}

// CPB4: a worker write to graph/events triggers the AfterWriteFilter reactor,
// which writes a directive the worker consumes next turn.
func TestReactorWritesDirectiveOnWorkerEvent(t *testing.T) {
	// Register everything (filters + reactor) BEFORE Start — like the conductor's
	// StartBus, which runs before server.Start binds the listener.
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	b := NewBus("conductor", CursorReader(srv))
	b.RegisterGlobal(srv)
	b.RegisterAgent(srv, "worker")
	done := make(chan struct{}, 1)
	b.RegisterReactor(srv, func(s *ooo.Server, ev Envelope) {
		_ = b.PushDirective(s, ev.From, "noted: "+ev.Body)
		select {
		case done <- struct{}{}:
		default:
		}
	})
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close(os.Interrupt)

	// A worker proposes over HTTP (like a real coder via graph_propose) — the
	// write filter assigns its seq and the AfterWriteFilter reactor fires
	// (asynchronously, off the storage lock), pushing a directive to the inbox.
	cfg := io.RemoteConfig{Host: srv.Address, Client: &http.Client{}}
	if err := io.RemotePush(cfg, GraphEventsGlob, Envelope{From: "worker", Type: MsgTaskMutation, Body: "split t1"}); err != nil {
		t.Fatalf("worker event: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reactor did not fire within 2s")
	}

	msgs, err := io.RemoteGetList[Envelope](cfg, InboxGlob("worker"))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected one directive in the worker inbox, got %d", len(msgs))
	}
	if d := msgs[0].Data; d.Type != MsgDirective || d.From != "conductor" || d.Seq == 0 {
		t.Errorf("expected a seq'd directive from conductor, got %+v", d)
	}
}

// CPB6: a stuck node escalates to blocked with a recorded reason (the K=3 cap's
// terminal disposition — no further retries).
func TestEscalateMarksBlocked(t *testing.T) {
	srv, b, stop := startServer(t, "conductor")
	defer stop()
	if err := b.CommitNode(srv, GraphNode{ID: "t1", Title: "export", Status: NodePending}); err != nil {
		t.Fatal(err)
	}
	if err := b.Escalate(srv, "t1", "no working split after 3 attempts"); err != nil {
		t.Fatal(err)
	}
	var got GraphNode
	for _, n := range b.ReadNodes(srv) {
		if n.ID == "t1" {
			got = n
		}
	}
	if got.Status != NodeBlocked {
		t.Errorf("escalated node should be blocked, got %q", got.Status)
	}
	if got.Reason == "" {
		t.Error("escalation should record a reason on the blocked node")
	}
}

// CPB4: a blocked node whose deps are all done auto-unblocks to pending.
func TestAutoUnblockOnDepsDone(t *testing.T) {
	srv, b, stop := startServer(t, "conductor")
	defer stop()

	commit := func(n GraphNode) {
		n.From = "conductor"
		if err := ooo.Set(srv, GraphNodeKey(n.ID), n); err != nil {
			t.Fatalf("commit %s: %v", n.ID, err)
		}
	}
	commit(GraphNode{ID: "t1", Status: NodeDone})
	commit(GraphNode{ID: "t2", Status: NodeBlocked, Deps: []string{"t1"}})
	commit(GraphNode{ID: "t3", Status: NodeBlocked, Deps: []string{"t1", "tX"}}) // tX not done

	unblocked := b.AutoUnblock(srv)
	if len(unblocked) != 1 || unblocked[0] != "t2" {
		t.Fatalf("expected only t2 to unblock (deps done), got %v", unblocked)
	}

	// t2 is now pending (open); t3 stays blocked (a dep is unfinished).
	byID := map[string]GraphNode{}
	for _, n := range b.ReadNodes(srv) {
		byID[n.ID] = n
	}
	if byID["t2"].Status != NodePending {
		t.Errorf("t2 should be pending after unblock, got %q", byID["t2"].Status)
	}
	if byID["t3"].Status != NodeBlocked {
		t.Errorf("t3 should remain blocked (dep tX not done), got %q", byID["t3"].Status)
	}
}
