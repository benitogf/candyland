package conductor

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/ooo"
)

// OrchestratorID is the single-writer identity for the task-graph ledger. The
// conductor (pure Go, zero model tokens) is the orchestrator; the tech-lead and
// coders reach the bus as workers (they propose; the conductor commits).
const OrchestratorID = "conductor"

// StartBus registers the coordination bus (Realization B) on the conductor's
// ooo server — the task-graph, brief, inbox, and cursor filters plus the
// coordination reactor — ALL of them here, before server.Start (filters must
// register before the listener binds, and never while the server is serving:
// mutating the filter set from a spawn goroutine raced ooo's broadcast loop).
// The bus is a back-channel beside the stdout loop, which is untouched. No-op
// without a server (serverless tests).
func (c *Conductor) StartBus() {
	if c.server == nil {
		return
	}
	b := bus.NewBus(OrchestratorID, bus.CursorReader(c.server))
	b.RegisterGlobal(c.server)
	b.RegisterReactor(c.server, func(srv *ooo.Server, ev bus.Envelope) {
		// Acknowledge the worker's proposal with a directive it consumes next turn,
		// then re-evaluate the graph — auto-unblock any nodes whose deps are now
		// done. This is coordination, not re-planning: the actual re-plan (the tech
		// lead re-emitting a partition when a coder's split fails) is driven by the
		// stdout loop; this back-channel only acknowledges and unblocks.
		_ = b.PushDirective(srv, ev.From, "noted: "+ev.Body)
		b.AutoUnblock(srv)
	})
	c.mu.Lock()
	c.bus = b
	c.mu.Unlock()
}

// mcpServerSpec is one entry in a Claude Code --mcp-config file.
type mcpServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

type mcpConfigFile struct {
	MCPServers map[string]mcpServerSpec `json:"mcpServers"`
}

// busMCPConfig writes a per-agent --mcp-config that launches `candyland
// comms-mcp`, wiring the coder to the conductor's bus as agentID. Returns the
// config path, or "" when no bus is wired (no flag is added then). The inbox
// filters are already registered globally at StartBus, so there is no per-agent
// registration here. The conductor stays pure Go — it only spawns the process
// and maps its stdout.
func (c *Conductor) busMCPConfig(runID, agentID string) string {
	c.mu.Lock()
	b := c.bus
	c.mu.Unlock()
	if b == nil || c.server == nil || c.server.Address == "" {
		return ""
	}
	// No per-agent filter registration here: the inbox filters are registered
	// once, globally, in StartBus (before the server serves). Registering them at
	// spawn raced ooo's broadcast loop.

	self, err := os.Executable()
	if err != nil {
		return ""
	}
	cfg := mcpConfigFile{MCPServers: map[string]mcpServerSpec{
		"candyland-comms": {
			Command: self,
			Args:    []string{"comms-mcp"},
			Env: map[string]string{
				"CANDYLAND_BUS_ADDR":     c.server.Address,
				"CANDYLAND_AGENT_ID":     agentID,
				"CANDYLAND_ORCHESTRATOR": OrchestratorID,
			},
		},
	}}
	data, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	dir := busConfigDir(runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(dir, agentID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return ""
	}
	return path
}

// busConfigDir is the per-run directory holding the spawned agents'
// --mcp-config files. Per-run so cleanupBusConfigs can remove them all at once
// when the run ends (the configs must outlive each claude spawn, so they can't
// be removed eagerly).
func busConfigDir(runID string) string {
	return filepath.Join(os.TempDir(), "candyland-mcp-"+runID)
}

// cleanupBusConfigs removes a run's --mcp-config files once it is finished (no
// further coder spawns). Idempotent and best-effort.
func (c *Conductor) cleanupBusConfigs(runID string) {
	_ = os.RemoveAll(busConfigDir(runID))
}

// publishGraphNodes mirrors the tech-lead's partition into the bus task-graph
// ledger so coders can graph_read the open work and the conductor can
// auto-unblock / escalate real nodes. No-op without a bus.
func (c *Conductor) publishGraphNodes(tasks []partitionTask) {
	c.mu.Lock()
	b := c.bus
	c.mu.Unlock()
	if b == nil || c.server == nil {
		return
	}
	for _, t := range tasks {
		_ = b.CommitNode(c.server, bus.GraphNode{ID: t.ID, Title: t.Title, Status: bus.NodePending, Deps: t.Deps})
	}
}

// putBrief writes an agent's brief to the bus before it is spawned, so the agent
// fetches its context via brief_get instead of receiving the plan/task on argv.
// No-op without a bus (serverless tests) — the stub claude needs no brief, and a
// real run always has the bus up (StartBus).
func (c *Conductor) putBrief(agentID string, br bus.Brief) {
	c.mu.Lock()
	b := c.bus
	c.mu.Unlock()
	if b == nil || c.server == nil {
		return
	}
	_ = b.PutBrief(c.server, agentID, br)
}

// escalateOpenNodes marks every still-open node blocked — the terminal
// disposition of the K=3 escalation cap when the conductor gives up on a run
// (no further retries, so no quota thrash). No-op without a bus.
func (c *Conductor) escalateOpenNodes(reason string) {
	c.mu.Lock()
	b := c.bus
	c.mu.Unlock()
	if b == nil || c.server == nil {
		return
	}
	for _, n := range b.ReadNodes(c.server) {
		if n.Status != bus.NodeDone && n.Status != bus.NodeBlocked {
			_ = b.Escalate(c.server, n.ID, reason)
		}
	}
}
