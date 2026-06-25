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
// ooo server — the task-graph + cursor filters and the coordination reactor —
// and stores it so per-agent inboxes can be registered at spawn. Must be called
// before server.Start (filters register before the listener binds). The bus is
// a back-channel beside the stdout loop, which is untouched. No-op without a
// server (serverless tests).
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

// busMCPConfig registers the agent's inbox (once) and writes a per-agent
// --mcp-config that launches `candyland comms-mcp`, wiring the coder to the
// conductor's bus as agentID. Returns the config path, or "" when no bus is
// wired (no flag is added then). The conductor stays pure Go — it only spawns
// the process and maps its stdout.
func (c *Conductor) busMCPConfig(runID, agentID string) string {
	c.mu.Lock()
	b := c.bus
	c.mu.Unlock()
	if b == nil || c.server == nil || c.server.Address == "" {
		return ""
	}
	c.registerBusAgent(agentID)

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

// registerBusAgent registers an agent's inbox filters exactly once.
func (c *Conductor) registerBusAgent(agentID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bus == nil || c.busAgents[agentID] {
		return
	}
	c.bus.RegisterAgent(c.server, agentID)
	c.busAgents[agentID] = true
}
