package conductor

import (
	"encoding/json"
	"testing"

	"github.com/benitogf/ooo/meta"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func obj(t *testing.T, v any) meta.Object {
	return meta.Object{Data: mustJSON(t, v)}
}

// CPB3 (inbox half) + seq assignment: the inbox write filter stamps a monotonic
// seq, requires a sender, and rejects a post whose addressing doesn't match the
// inbox owner in the path.
func TestInboxWriteFilterSeqAndAddressing(t *testing.T) {
	b := NewBus("tech-lead", nil)
	wf := b.InboxWriteFilter()

	out1, err := wf("inbox/bob/0001", mustJSON(t, Envelope{From: "alice", To: "bob", Type: MsgQuestion}))
	if err != nil {
		t.Fatalf("valid post rejected: %v", err)
	}
	out2, err := wf("inbox/bob/0002", mustJSON(t, Envelope{From: "carol", To: "bob", Type: MsgFeedback}))
	if err != nil {
		t.Fatal(err)
	}
	var e1, e2 Envelope
	json.Unmarshal(out1, &e1)
	json.Unmarshal(out2, &e2)
	if e1.Seq != 1 || e2.Seq != 2 {
		t.Errorf("seq not monotonic from 1: got %d, %d", e1.Seq, e2.Seq)
	}

	if _, err := wf("inbox/bob/x", mustJSON(t, Envelope{From: "", To: "bob"})); err == nil {
		t.Error("expected rejection of a post with no sender")
	}
	if _, err := wf("inbox/bob/x", mustJSON(t, Envelope{From: "alice", To: "carol"})); err == nil {
		t.Error("expected rejection: To=carol does not match inbox/bob path (CPB3)")
	}
}

// CPB3 (graph half): only the orchestrator may write the task ledger.
func TestGraphNodesWriteFilterOrchestratorOnly(t *testing.T) {
	b := NewBus("tech-lead", nil)
	wf := b.GraphNodesWriteFilter()

	if _, err := wf("graph/nodes/t1", mustJSON(t, GraphNode{ID: "t1", Status: NodePending, From: "tech-lead"})); err != nil {
		t.Errorf("orchestrator node write rejected: %v", err)
	}
	if _, err := wf("graph/nodes/t1", mustJSON(t, GraphNode{ID: "t1", Status: NodeDone, From: "coder-1"})); err == nil {
		t.Error("expected rejection of a graph/nodes write from a non-orchestrator (CPB3)")
	}
}

// CPB3 (propose-cannot-commit): graph_propose appends to graph/events (accepted
// as a proposal), but the same non-orchestrator writing graph/nodes is rejected
// — so proposing can never commit a node.
func TestGraphProposeCannotCommit(t *testing.T) {
	b := NewBus("tech-lead", nil)
	if _, err := b.GraphEventsWriteFilter()("graph/events/0001", mustJSON(t, Envelope{From: "coder-1", Type: MsgTaskMutation, Body: "split t1"})); err != nil {
		t.Fatalf("a worker proposal to graph/events should be accepted: %v", err)
	}
	if _, err := b.GraphNodesWriteFilter()("graph/nodes/t1", mustJSON(t, GraphNode{ID: "t1", From: "coder-1"})); err == nil {
		t.Error("a worker must not be able to commit a node directly (CPB3)")
	}
}

// CPB2 (inbox half): the read filter returns only messages newer than the
// recipient's cursor.
func TestInboxReadFilterSeqCursor(t *testing.T) {
	b := NewBus("tech-lead", func(agent string) int64 {
		if agent == "bob" {
			return 5
		}
		return 0
	})
	objs := []meta.Object{
		obj(t, Envelope{From: "a", To: "bob", Seq: 3}),
		obj(t, Envelope{From: "a", To: "bob", Seq: 5}),
		obj(t, Envelope{From: "a", To: "bob", Seq: 6}),
		obj(t, Envelope{From: "a", To: "bob", Seq: 9}),
	}
	got, err := b.InboxReadFilter()("inbox/bob/*", objs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages with seq>5, got %d", len(got))
	}
	for _, o := range got {
		var e Envelope
		json.Unmarshal(o.Data, &e)
		if e.Seq <= 5 {
			t.Errorf("returned a message at or below cursor: seq %d", e.Seq)
		}
	}
}

// CPB2 (graph half): the read filter returns only non-done nodes (the
// open-items view).
func TestGraphNodesReadFilterNonDone(t *testing.T) {
	b := NewBus("tech-lead", nil)
	objs := []meta.Object{
		obj(t, GraphNode{ID: "t1", Status: NodePending}),
		obj(t, GraphNode{ID: "t2", Status: NodeInProgress}),
		obj(t, GraphNode{ID: "t3", Status: NodeDone}),
		obj(t, GraphNode{ID: "t4", Status: NodeBlocked}),
	}
	got, err := b.GraphNodesReadFilter()("graph/nodes/*", objs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 non-done nodes, got %d", len(got))
	}
	for _, o := range got {
		var n GraphNode
		json.Unmarshal(o.Data, &n)
		if n.Status == NodeDone {
			t.Errorf("done node %q leaked into the open-items view", n.ID)
		}
	}
}
