package comms

import (
	"os"
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// A non-orchestrator client refuses to commit a node client-side, before any
// network call (the bus addr here is unreachable — the guard must short-circuit
// it). The server-side filter is the real gate; this is the fail-fast.
func TestGraphCommitClientSideGuard(t *testing.T) {
	coder := NewClient("127.0.0.1:1", "bob", "tech-lead") // self != orchestrator; addr unroutable
	err := coder.GraphCommit(bus.GraphNode{ID: "t1"})
	if err == nil {
		t.Fatal("a non-orchestrator GraphCommit must fail client-side")
	}
	if !strings.Contains(err.Error(), "only the orchestrator") {
		t.Errorf("expected a client-side orchestrator guard error, got %v", err)
	}
}

// startBus stands up an embedded ooo server with the coordination filters
// registered for the given agents, started on an ephemeral port. Returns the
// host:port and a teardown.
func startBus(t *testing.T, orchestrator string, agents ...string) (string, func()) {
	t.Helper()
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	// Static: true mirrors the production conductor (main.go) — deny-by-default,
	// so this also proves every bus path has a registered filter.
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	b := bus.NewBus(orchestrator, bus.CursorReader(srv))
	b.RegisterGlobal(srv)
	for _, a := range agents {
		b.RegisterAgent(srv, a)
	}
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatalf("start bus: %v", err)
	}
	return srv.Address, func() { srv.Close(os.Interrupt) }
}

// CPB1 + CPB2 (inbox): A sends to B; B's inbox returns it over io.Remote*, and a
// second read is empty (cursor advanced — server-side seq>cursor scoping).
func TestCommsSendInboxRoundTrip(t *testing.T) {
	addr, stop := startBus(t, "tech-lead", "alice", "bob")
	defer stop()

	alice := NewClient(addr, "alice", "tech-lead")
	bob := NewClient(addr, "bob", "tech-lead")

	if err := alice.Send("bob", bus.MsgQuestion, "where is the export defined?", "conv1", ""); err != nil {
		t.Fatalf("send: %v", err)
	}
	msgs, err := bob.Inbox()
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(msgs) != 1 || msgs[0].From != "alice" || msgs[0].Body != "where is the export defined?" {
		t.Fatalf("expected one message from alice, got %+v", msgs)
	}
	if msgs[0].Seq == 0 {
		t.Error("server should have assigned a seq")
	}
	// Cursor advanced → second read returns nothing.
	again, err := bob.Inbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second inbox read should be empty after cursor advance, got %d", len(again))
	}
}

// CPB3 (live): a non-orchestrator cannot commit a node; the orchestrator can;
// graph_read then shows the open node; a worker proposal is accepted (and is not
// a node).
func TestGraphCommitAuthAndRead(t *testing.T) {
	addr, stop := startBus(t, "tech-lead", "bob")
	defer stop()

	bob := NewClient(addr, "bob", "tech-lead")
	orch := NewClient(addr, "tech-lead", "tech-lead")

	if err := bob.GraphCommit(bus.GraphNode{ID: "t1", Title: "export", Status: bus.NodePending}); err == nil {
		t.Error("a non-orchestrator must not be able to commit a node (CPB3)")
	}
	if err := orch.GraphCommit(bus.GraphNode{ID: "t1", Title: "export", Status: bus.NodePending}); err != nil {
		t.Fatalf("orchestrator commit rejected: %v", err)
	}
	nodes, err := bob.GraphRead()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != "t1" {
		t.Fatalf("graph_read should return the open node t1, got %+v", nodes)
	}
	// A worker proposal is accepted to the event log (it does not create a node).
	if err := bob.GraphPropose("split t1 into t1a/t1b"); err != nil {
		t.Fatalf("propose rejected: %v", err)
	}
}

// CPB2 (graph half, live): a done node drops out of the open-items view.
func TestGraphReadHidesDone(t *testing.T) {
	addr, stop := startBus(t, "tech-lead")
	defer stop()
	orch := NewClient(addr, "tech-lead", "tech-lead")

	if err := orch.GraphCommit(bus.GraphNode{ID: "open1", Status: bus.NodeInProgress}); err != nil {
		t.Fatal(err)
	}
	if err := orch.GraphCommit(bus.GraphNode{ID: "closed1", Status: bus.NodeDone}); err != nil {
		t.Fatal(err)
	}
	nodes, err := orch.GraphRead()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != "open1" {
		t.Fatalf("graph_read should return only the non-done node, got %+v", nodes)
	}
}
