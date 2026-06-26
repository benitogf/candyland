package bus

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/meta"
)

// The coordination bus is the multi-process realization (B) of the portable
// protocol detritus documents in core/coordination. It is a back-channel on the
// conductor's existing ooo server, ALONGSIDE the stdout loop (which still drives
// agent output). The conductor stays pure Go; identity rides in the payload
// `from` (ooo filters see only (key,data) — no caller identity), so this is a
// cooperative integrity gate, not a security boundary.

// Message types (FIPA-reduced, two-tier correlation).
const (
	MsgQuestion     = "question"
	MsgResponse     = "response"
	MsgFeedback     = "feedback"
	MsgDirective    = "directive"
	MsgTaskMutation = "task_mutation"
)

// Task-graph node statuses.
const (
	NodePending    = "pending"
	NodeInProgress = "in_progress"
	NodeBlocked    = "blocked"
	NodeDone       = "done"
)

// Bus key namespaces (ooo glob keys; one glob, at the end — ValidateGlob).
const (
	GraphNodesGlob  = "graph/nodes/*"  // the durable task ledger (orchestrator single-writer)
	GraphEventsGlob = "graph/events/*" // append-only proposal/mutation log (anyone appends)
	BriefGlob       = "brief/*"        // per-agent initial context (orchestrator-written, agent-read)
	InboxFilterGlob = "inbox/*/*"      // server-side filter covering EVERY recipient's inbox/<id>/<seq>
)

// InboxGlob is the per-recipient inbox list path a client pushes to / reads from
// (the recipient is a fixed segment so the single trailing glob is valid for the
// HTTP request). The server-side filters are registered ONCE, globally, as
// InboxFilterGlob — see RegisterGlobal; there is no per-agent registration.
func InboxGlob(agentID string) string { return "inbox/" + agentID + "/*" }

// GraphNodeKey is the concrete (non-glob) key of one task-graph node.
func GraphNodeKey(id string) string { return "graph/nodes/" + id }

// BriefKey is the concrete (non-glob) key of one agent's brief.
func BriefKey(agentID string) string { return "brief/" + agentID }

// CursorKey holds an agent's last-consumed seq.
func CursorKey(agentID string) string { return "cursor/" + agentID }

// Cursor is an agent's since-cursor (last-consumed seq).
type Cursor struct {
	Seq int64 `json:"seq"`
}

// CursorReader builds the cursor lookup the inbox read filter uses, reading
// cursor/<agentID> from the conductor's own server in-process (0 if unset).
func CursorReader(server *ooo.Server) func(agentID string) int64 {
	return func(agentID string) int64 {
		m, err := ooo.Get[Cursor](server, CursorKey(agentID))
		if err != nil {
			return 0
		}
		return m.Data.Seq
	}
}

// Envelope is one coordination message (core/coordination protocol).
type Envelope struct {
	From           string `json:"from"`
	To             string `json:"to"`
	Type           string `json:"type"`
	ConversationID string `json:"conversationId,omitempty"`
	CorrelationID  string `json:"correlationId,omitempty"`
	Ts             int64  `json:"ts,omitempty"`
	Seq            int64  `json:"seq"`
	Body           string `json:"body,omitempty"`
}

// GraphNode is a task-graph node — a superset of the tech-lead's partitionTask,
// adding status/owner/deps/priority/version. From is the writer identity, used
// only for the cooperative orchestrator-single-writer check on graph/nodes/*.
type GraphNode struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Owner    string   `json:"owner,omitempty"`
	Deps     []string `json:"deps,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Version  int      `json:"version"`
	Reason   string   `json:"reason,omitempty"` // why a node is blocked (escalation)
	From     string   `json:"from,omitempty"`
}

// Brief is one agent's initial context — the work it would otherwise have
// received on the claude command line (the plan, the task spec, prior-attempt
// feedback). It is written by the orchestrator to brief/<agentID> BEFORE the
// agent is spawned and read once by the agent through brief_get, so the spawn
// prompt stays a tiny constant bootstrap and the plan never rides on argv
// (which Windows caps at 32k). From is the writer identity, used only for the
// cooperative orchestrator-only write gate on brief/* (mirrors GraphNode).
type Brief struct {
	From     string   `json:"from,omitempty"`     // writer identity (orchestrator-only gate)
	To       string   `json:"to"`                 // the agent this brief is for
	Role     string   `json:"role,omitempty"`     // tech-lead | backend | frontend | fullstack | test | …
	Prompt   string   `json:"prompt,omitempty"`   // the full request/plan (tech lead) — never on argv
	Title    string   `json:"title,omitempty"`    // task title (coder)
	Files    []string `json:"files,omitempty"`    // the task's fork-safe file boundary (coder)
	Test     string   `json:"test,omitempty"`     // the defining test (coder)
	Deps     []string `json:"deps,omitempty"`     // task ids that must finish first (coder)
	Repo     string   `json:"repo,omitempty"`     // the repo this task targets (multi-repo)
	Feedback string   `json:"feedback,omitempty"` // prior-attempt failure to avoid (re-plan / retry)
	Attempt  int      `json:"attempt,omitempty"`  // 1-based attempt number
}

// Bus carries the bus state: who the single-writer orchestrator is, the
// server-assigned monotonic seq, and a lookup for each agent's since-cursor.
type Bus struct {
	orchestrator string
	seq          int64 // atomic
	cursorOf     func(agentID string) int64
}

// NewBus builds a bus. cursorOf returns an agent's last-consumed seq (0 if
// none); it is how the inbox read filter scopes to seq>cursor without a caller
// identity. A nil cursorOf is treated as "cursor 0" (deliver everything).
func NewBus(orchestratorID string, cursorOf func(agentID string) int64) *Bus {
	if cursorOf == nil {
		cursorOf = func(string) int64 { return 0 }
	}
	return &Bus{orchestrator: orchestratorID, cursorOf: cursorOf}
}

func (b *Bus) nextSeq() int64 { return atomic.AddInt64(&b.seq, 1) }

// segment returns the n-th '/'-separated segment of key (0-based), "" if absent.
func segment(key string, n int) string {
	parts := strings.Split(key, "/")
	if n >= 0 && n < len(parts) {
		return parts[n]
	}
	return ""
}

// --- WriteFilters (run BEFORE the write; reject by returning a non-nil error) ---

// InboxWriteFilter assigns a monotonic seq and enforces addressing integrity:
// the post must declare a sender (From) and its To must equal the inbox owner in
// the key (inbox/<recipient>/...), so a message can't be misaddressed.
func (b *Bus) InboxWriteFilter() ooo.Apply {
	return func(key string, data json.RawMessage) (json.RawMessage, error) {
		var e Envelope
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("inbox: invalid envelope: %w", err)
		}
		if e.From == "" {
			return nil, errors.New("inbox: missing from")
		}
		recipient := segment(key, 1) // inbox/<recipient>/<minted>
		if e.To != recipient {
			return nil, fmt.Errorf("inbox: to %q does not match path recipient %q", e.To, recipient)
		}
		e.Seq = b.nextSeq()
		return json.Marshal(e)
	}
}

// GraphEventsWriteFilter assigns a seq to an appended proposal/event. Anyone may
// propose (append) here; only graph/nodes is orchestrator-gated, so a proposal
// can never become a committed node by writing events.
func (b *Bus) GraphEventsWriteFilter() ooo.Apply {
	return func(key string, data json.RawMessage) (json.RawMessage, error) {
		var e Envelope
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("graph/events: invalid envelope: %w", err)
		}
		if e.From == "" {
			return nil, errors.New("graph/events: missing from")
		}
		e.Seq = b.nextSeq()
		return json.Marshal(e)
	}
}

// GraphNodesWriteFilter enforces the orchestrator single-writer rule: only the
// orchestrator (by payload From) may write the durable task ledger. A coder's
// direct node write — or a graph_propose misrouted here — is rejected.
func (b *Bus) GraphNodesWriteFilter() ooo.Apply {
	return func(key string, data json.RawMessage) (json.RawMessage, error) {
		var n GraphNode
		if err := json.Unmarshal(data, &n); err != nil {
			return nil, fmt.Errorf("graph/nodes: invalid node: %w", err)
		}
		if n.From != b.orchestrator {
			return nil, fmt.Errorf("graph/nodes: write from %q rejected (orchestrator-only)", n.From)
		}
		return json.Marshal(n)
	}
}

// BriefWriteFilter enforces the orchestrator single-writer rule on brief/*: only
// the orchestrator (by payload From) may write an agent's brief — the brief is
// conductor-authored context, not something a worker mints. Mirrors the
// GraphNodes gate; a worker's RemoteSet of any brief is rejected.
func (b *Bus) BriefWriteFilter() ooo.Apply {
	return func(key string, data json.RawMessage) (json.RawMessage, error) {
		var br Brief
		if err := json.Unmarshal(data, &br); err != nil {
			return nil, fmt.Errorf("brief: invalid brief: %w", err)
		}
		if br.From != b.orchestrator {
			return nil, fmt.Errorf("brief: write from %q rejected (orchestrator-only)", br.From)
		}
		return json.Marshal(br)
	}
}

// BriefReadObjectFilter permits reading a brief. A brief is cooperative context
// (not a secret), so the read is open — registering it is what opens the brief/*
// GET route under the server's Static deny-by-default mode. Each agent reads its
// own brief/<self> by convention, the same cooperative model as the rest of the
// bus (identity is not a security boundary here).
func (b *Bus) BriefReadObjectFilter() ooo.ApplyObject {
	return func(key string, obj meta.Object) (meta.Object, error) {
		return obj, nil
	}
}

// --- ReadListFilters (scope what a list read returns) ---

// InboxReadFilter returns only messages newer than the recipient's cursor
// (seq>cursor). The recipient is the inbox owner segment in the read path, so a
// caller reading inbox/<self>/* sees only its own channel.
func (b *Bus) InboxReadFilter() ooo.ApplyList {
	return func(key string, objs []meta.Object) ([]meta.Object, error) {
		cursor := b.cursorOf(segment(key, 1))
		out := make([]meta.Object, 0, len(objs))
		for _, o := range objs {
			var e Envelope
			if err := json.Unmarshal(o.Data, &e); err != nil {
				continue // skip unparseable rather than fail the whole read
			}
			if e.Seq > cursor {
				out = append(out, o)
			}
		}
		return out, nil
	}
}

// GraphNodesReadFilter returns only non-done nodes — the open-items view the
// orchestrator re-derives each cycle without re-ingesting the whole graph.
func (b *Bus) GraphNodesReadFilter() ooo.ApplyList {
	return func(key string, objs []meta.Object) ([]meta.Object, error) {
		out := make([]meta.Object, 0, len(objs))
		for _, o := range objs {
			var n GraphNode
			if err := json.Unmarshal(o.Data, &n); err != nil {
				continue
			}
			if n.Status != NodeDone {
				out = append(out, o)
			}
		}
		return out, nil
	}
}

// --- registration ---

// RegisterGlobal registers the run-wide bus filters (the task-graph ledger and
// the proposal log) on the conductor's existing ooo server. Call once per run.
func (b *Bus) RegisterGlobal(server *ooo.Server) {
	server.WriteFilter(GraphNodesGlob, b.GraphNodesWriteFilter())
	server.ReadListFilter(GraphNodesGlob, b.GraphNodesReadFilter())
	server.WriteFilter(GraphEventsGlob, b.GraphEventsWriteFilter())
	// brief/<agentID>: orchestrator-only write, open read (the agent fetches its
	// own brief once via brief_get instead of receiving the plan on argv).
	server.WriteFilter(BriefGlob, b.BriefWriteFilter())
	server.ReadObjectFilter(BriefGlob, b.BriefReadObjectFilter())
	// inbox/<recipient>/<seq>: ONE global filter pair covering every recipient.
	// The filters derive the recipient from the key segment, so no per-agent
	// registration is needed — every bus filter is registered here, before
	// server.Start, and the filter set is never mutated again while the server is
	// serving (mutating it from a spawn goroutine raced ooo's broadcast loop).
	server.WriteFilter(InboxFilterGlob, b.InboxWriteFilter())
	server.ReadListFilter(InboxFilterGlob, b.InboxReadFilter())
	// The conductor folds the append-only event log in-process; expose it
	// unscoped (needed because the conductor runs Static: deny-by-default).
	server.ReadListFilter(GraphEventsGlob, func(key string, objs []meta.Object) ([]meta.Object, error) {
		return objs, nil
	})
	// cursor/<agentId> read+write must be permitted under Static mode (the
	// inbox read filter reads it; comms_inbox advances it).
	server.OpenFilter("cursor/*")
}

// --- coordination reaction (the conductor's in-process orchestration hook) ---

// EventHandler reacts to a committed worker event — the conductor's coordination
// hook (acknowledge + auto-unblock; not re-planning, which is stdout-driven).
type EventHandler func(server *ooo.Server, ev Envelope)

// RegisterReactor wires the coordination reaction to the global storage-level
// AfterWrite hook: when any write lands the conductor checks whether it was a
// worker event (graph/events/*) and, if so, invokes handler for each event
// newer than the last processed (advancing by seq). It uses the global hook —
// not a path AfterWriteFilter — because a path-filter's pool preallocation at
// Start blocks the per-agent inbox filters that are registered later, at spawn.
// The work runs in a goroutine: AfterWrite fires under the storage write lock,
// so the reactor's own writes must not run inline or they would deadlock the
// server (the directive is consumed on the worker's next turn regardless).
// Goroutines serialize on the cursor mutex; the handler must not write
// graph/events. Must be set before server.Start. The stdout loop is untouched.
func (b *Bus) RegisterReactor(server *ooo.Server, handler EventHandler) {
	var mu sync.Mutex
	var lastSeq int64
	server.AfterWrite = func(key string) {
		if !strings.HasPrefix(key, "graph/events/") {
			return
		}
		go func() {
			mu.Lock()
			defer mu.Unlock()
			objs, err := server.Storage.GetList(GraphEventsGlob)
			if err != nil {
				return
			}
			fresh := make([]Envelope, 0, len(objs))
			for _, o := range objs {
				var e Envelope
				if json.Unmarshal(o.Data, &e) == nil && e.Seq > lastSeq {
					fresh = append(fresh, e)
				}
			}
			sort.Slice(fresh, func(i, j int) bool { return fresh[i].Seq < fresh[j].Seq })
			for _, e := range fresh {
				lastSeq = e.Seq
				handler(server, e)
			}
		}()
	}
}

// PushDirective delivers a directive to an agent's inbox (orchestrator →
// worker), in-process. In-process writes still run the registered WriteFilters
// (the same InboxWriteFilter the untrusted HTTP path hits — being in-process
// only waives Static mode's route-required check, not the filters), so this must
// satisfy that filter: From must be set and To must match the inbox owner. The
// filter assigns the monotonic seq, so we don't stamp one here. The worker
// consumes the directive (seq>cursor) on its next comms_inbox.
func (b *Bus) PushDirective(server *ooo.Server, to, body string) error {
	_, err := ooo.Push(server, InboxGlob(to), Envelope{
		From: b.orchestrator, To: to, Type: MsgDirective, Body: body,
	})
	return err
}

// ReadNodes returns the FULL task-graph ledger (including done nodes) by reading
// raw storage — bypassing the agent-facing non-done read filter, since the
// orchestrator reasons over the complete graph (e.g. to know which deps are done).
func (b *Bus) ReadNodes(server *ooo.Server) []GraphNode {
	objs, err := server.Storage.GetList(GraphNodesGlob)
	if err != nil {
		return nil
	}
	out := make([]GraphNode, 0, len(objs))
	for _, o := range objs {
		var n GraphNode
		if json.Unmarshal(o.Data, &n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// AutoUnblock flips every blocked node whose dependencies are all done to
// pending (committing the change as the orchestrator), so dependents auto-unblock
// on completion. Returns the ids it unblocked.
func (b *Bus) AutoUnblock(server *ooo.Server) []string {
	nodes := b.ReadNodes(server)
	done := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.Status == NodeDone {
			done[n.ID] = true
		}
	}
	var unblocked []string
	for _, n := range nodes {
		if n.Status != NodeBlocked || !depsDone(n.Deps, done) {
			continue
		}
		n.Status = NodePending
		n.From = b.orchestrator
		n.Version++
		if err := ooo.Set(server, GraphNodeKey(n.ID), n); err == nil {
			unblocked = append(unblocked, n.ID)
		}
	}
	return unblocked
}

func depsDone(deps []string, done map[string]bool) bool {
	for _, d := range deps {
		if !done[d] {
			return false
		}
	}
	return true
}

// PutBrief writes an agent's brief as the orchestrator (the single writer),
// before that agent is spawned. The agent reads it once via brief_get, so the
// spawn prompt stays a constant bootstrap and the plan never rides on argv.
// In-process writes still run BriefWriteFilter (the orchestrator-only gate),
// satisfied here by setting From=orchestrator.
func (b *Bus) PutBrief(server *ooo.Server, agentID string, br Brief) error {
	br.From = b.orchestrator
	br.To = agentID
	return ooo.Set(server, BriefKey(agentID), br)
}

// CommitNode writes or updates a task-graph node as the orchestrator (the
// single writer). Used to publish the partition into the ledger and to update
// node status. In-process writes still run the GraphNodesWriteFilter (the
// orchestrator-only gate), which this satisfies by setting From=orchestrator —
// the same rule that rejects a worker's direct node write on the HTTP path.
func (b *Bus) CommitNode(server *ooo.Server, n GraphNode) error {
	if n.Status == "" {
		n.Status = NodePending
	}
	n.From = b.orchestrator
	n.Version++
	return ooo.Set(server, GraphNodeKey(n.ID), n)
}

// Escalate marks a stuck node blocked with a reason — the K=3 escalation cap's
// terminal disposition. Once blocked there are no further retries, so the cap
// prevents quota thrash. No-op if the node doesn't exist.
func (b *Bus) Escalate(server *ooo.Server, nodeID, reason string) error {
	m, err := ooo.Get[GraphNode](server, GraphNodeKey(nodeID))
	if err != nil {
		return err
	}
	n := m.Data
	n.Status = NodeBlocked
	n.Reason = reason
	n.From = b.orchestrator
	n.Version++
	return ooo.Set(server, GraphNodeKey(nodeID), n)
}
