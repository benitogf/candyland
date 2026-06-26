// Package comms is the coordination back-channel client a coder reaches through
// its MCP surface (candyland-hosted, since the tools are ooo io.Remote* clients
// and detritus stays ooo-free). It talks to the conductor's ooo bus over HTTP;
// identity rides in the payload `from`, set from the agent's own id.
package comms

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/ooo/io"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client is one agent's view of the bus: a remote ooo config pointing at the
// conductor + the agent's own id (the From on everything it sends, the inbox it
// reads). orchestrator is the id permitted to commit task-graph nodes (the
// server-gated single writer).
type Client struct {
	cfg          io.RemoteConfig
	self         string
	orchestrator string
}

// NewClient builds a bus client for agent self, reaching the conductor at
// busAddr (host:port). orchestrator names the single task-graph writer.
func NewClient(busAddr, self, orchestrator string) *Client {
	return &Client{
		cfg:          io.RemoteConfig{Host: busAddr, Client: &http.Client{}},
		self:         self,
		orchestrator: orchestrator,
	}
}

// Send delivers a message to another agent's inbox.
func (c *Client) Send(to, msgType, body, conversationID, correlationID string) error {
	if to == "" {
		return fmt.Errorf("comms_send: 'to' required")
	}
	return io.RemotePush(c.cfg, bus.InboxGlob(to), bus.Envelope{
		From: c.self, To: to, Type: msgType, Body: body,
		ConversationID: conversationID, CorrelationID: correlationID,
	})
}

// Inbox returns this agent's new messages (the server scopes to seq>cursor),
// then advances the cursor so the next call returns only newer ones.
//
// Each agent has exactly one bus client — the spawned comms-mcp process — and an
// MCP server handles its tool calls serially, so comms_inbox is the single,
// sequential writer of this agent's cursor: the read-then-advance has no
// concurrent reader to race, and maxSeq only grows. Delivery is at-least-once:
// if the cursor write fails the messages are still returned (and re-delivered
// next call), and the failure is logged rather than silently dropped so a
// persistently-failing advance is visible instead of a silent re-delivery loop.
func (c *Client) Inbox() ([]bus.Envelope, error) {
	metas, err := io.RemoteGetList[bus.Envelope](c.cfg, bus.InboxGlob(c.self))
	if err != nil {
		return nil, err
	}
	out := make([]bus.Envelope, 0, len(metas))
	var maxSeq int64
	for _, m := range metas {
		out = append(out, m.Data)
		if m.Data.Seq > maxSeq {
			maxSeq = m.Data.Seq
		}
	}
	if maxSeq > 0 {
		if err := io.RemoteSet(c.cfg, bus.CursorKey(c.self), bus.Cursor{Seq: maxSeq}); err != nil {
			log.Printf("candyland comms: agent %q cursor advance to %d failed: %v (messages re-delivered next inbox)", c.self, maxSeq, err)
		}
	}
	return out, nil
}

// BriefGet returns this agent's brief — the initial context (the plan for the
// tech lead; the task spec for a coder) the orchestrator wrote to brief/<self>
// before spawn. The agent reads it once via the brief_get tool instead of
// receiving the plan on its command line. Errors if no brief was written.
func (c *Client) BriefGet() (bus.Brief, error) {
	m, err := io.RemoteGet[bus.Brief](c.cfg, bus.BriefKey(c.self))
	if err != nil {
		return bus.Brief{}, err
	}
	return m.Data, nil
}

// GraphRead returns the open task-graph nodes (the server filters out done).
func (c *Client) GraphRead() ([]bus.GraphNode, error) {
	metas, err := io.RemoteGetList[bus.GraphNode](c.cfg, bus.GraphNodesGlob)
	if err != nil {
		return nil, err
	}
	out := make([]bus.GraphNode, 0, len(metas))
	for _, m := range metas {
		out = append(out, m.Data)
	}
	return out, nil
}

// GraphPropose appends a worker proposal to the event log. It can never commit a
// node — the server only accepts node writes from the orchestrator.
func (c *Client) GraphPropose(mutation string) error {
	return io.RemotePush(c.cfg, bus.GraphEventsGlob, bus.Envelope{
		From: c.self, Type: bus.MsgTaskMutation, Body: mutation,
	})
}

// GraphCommit writes a node to the durable ledger. It is NOT exposed as an MCP
// tool — coders can only graph_propose; the conductor commits in-process via
// bus.CommitNode. This client-side helper exists to exercise the orchestrator
// single-writer gate from the comms path (the integration test): it fails fast
// when self isn't the orchestrator, and the server's GraphNodesWriteFilter is
// the actual gate that rejects any non-orchestrator write.
func (c *Client) GraphCommit(n bus.GraphNode) error {
	if c.self != c.orchestrator {
		return fmt.Errorf("graph_commit: only the orchestrator %q may commit nodes, not %q", c.orchestrator, c.self)
	}
	n.From = c.self
	return io.RemoteSet(c.cfg, bus.GraphNodeKey(n.ID), n)
}

// --- MCP tools ---

// RegisterTools registers the lean comms surface on an MCP server.
func RegisterTools(server *mcp.Server, c *Client) {
	type sendArgs struct {
		To             string `json:"to" jsonschema:"Recipient agent id."`
		Type           string `json:"type" jsonschema:"Message type: question | response | feedback | directive | task_mutation."`
		Body           string `json:"body" jsonschema:"Message body."`
		ConversationID string `json:"conversationId,omitempty"`
		CorrelationID  string `json:"correlationId,omitempty" jsonschema:"Echo a question's correlationId on a response."`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "comms_send",
		Description: "Send a coordination message to another agent's inbox via the conductor bus.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a sendArgs) (*mcp.CallToolResult, any, error) {
		if err := c.Send(a.To, a.Type, a.Body, a.ConversationID, a.CorrelationID); err != nil {
			return errResult("comms_send: " + err.Error()), nil, nil
		}
		return textResult(fmt.Sprintf("sent %s to %s", a.Type, a.To)), nil, nil
	})

	type noArgs struct{}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "comms_inbox",
		Description: "Return this agent's new messages (only those newer than its cursor) and advance the cursor.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		msgs, err := c.Inbox()
		if err != nil {
			return errResult("comms_inbox: " + err.Error()), nil, nil
		}
		if len(msgs) == 0 {
			return textResult("(no new messages)"), nil, nil
		}
		var b strings.Builder
		for _, m := range msgs {
			fmt.Fprintf(&b, "[%d] %s from %s: %s\n", m.Seq, m.Type, m.From, m.Body)
		}
		return textResult(strings.TrimSpace(b.String())), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "brief_get",
		Description: "Return this agent's brief — its task/plan context. Call this FIRST, before doing any work; it carries the instructions that used to be passed on the command line.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		br, err := c.BriefGet()
		if err != nil {
			return errResult("brief_get: " + err.Error()), nil, nil
		}
		return textResult(formatBrief(br)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "graph_read",
		Description: "Return the open (non-done) task-graph nodes — the current work to do.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		nodes, err := c.GraphRead()
		if err != nil {
			return errResult("graph_read: " + err.Error()), nil, nil
		}
		if len(nodes) == 0 {
			return textResult("(no open nodes)"), nil, nil
		}
		var b strings.Builder
		for _, n := range nodes {
			fmt.Fprintf(&b, "%s [%s] %s deps=%v\n", n.ID, n.Status, n.Title, n.Deps)
		}
		return textResult(strings.TrimSpace(b.String())), nil, nil
	})

	type proposeArgs struct {
		Mutation string `json:"mutation" jsonschema:"A proposed task-graph change for the orchestrator to consider (a proposal, not a commit)."`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "graph_propose",
		Description: "Propose a task-graph change to the orchestrator. A proposal is appended to the event log; it never commits a node directly.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a proposeArgs) (*mcp.CallToolResult, any, error) {
		if err := c.GraphPropose(a.Mutation); err != nil {
			return errResult("graph_propose: " + err.Error()), nil, nil
		}
		return textResult("proposed"), nil, nil
	})
}

// formatBrief renders a brief as the readable text the agent sees from brief_get.
// The tech lead's brief carries the plan (Prompt); a coder's carries the task
// fields. Empty fields are omitted so each role sees only what's relevant.
func formatBrief(b bus.Brief) string {
	var sb strings.Builder
	w := func(label, val string) {
		if val != "" {
			fmt.Fprintf(&sb, "%s: %s\n", label, val)
		}
	}
	w("role", b.Role)
	w("repo", b.Repo)
	w("task", b.Title)
	if len(b.Files) > 0 {
		w("files", strings.Join(b.Files, ", "))
	}
	w("test", b.Test)
	if len(b.Deps) > 0 {
		w("deps", strings.Join(b.Deps, ", "))
	}
	if b.Attempt > 1 {
		fmt.Fprintf(&sb, "attempt: %d\n", b.Attempt)
	}
	if b.Feedback != "" {
		fmt.Fprintf(&sb, "previous attempt failed — avoid this: %s\n", b.Feedback)
	}
	if b.Prompt != "" {
		fmt.Fprintf(&sb, "\n%s", b.Prompt)
	}
	if s := strings.TrimSpace(sb.String()); s != "" {
		return s
	}
	return "(no brief)"
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}, IsError: true}
}
