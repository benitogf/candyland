package conductor

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/ooo"
)

// minClaudeMajor is the floor major version of the claude CLI: `type:http`
// mcp-config support (which the coordination bus now relies on) lands in 2.x.
// The installer floats the latest CLI, so this is asserted at startup, not
// pinned — a mismatch warns rather than hard-failing the whole app.
const minClaudeMajor = 2

// claudeVersionRe pulls the leading semver out of `claude --version` output
// (e.g. "2.1.4 (Claude Code)" → "2", "1", "4").
var claudeVersionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// CheckClaudeVersion probes the pinned claude CLI and logs a WARNING if it is
// below the floor that supports `type:http` mcp-config entries. It never fails
// the app: a missing or unparseable CLI is left to surface honestly when a run
// actually spawns claude. Cheap and windowless (reuses configureProc).
func CheckClaudeVersion() {
	cmd := exec.Command(claudeBin(), "--version")
	cmd.Env = claudeEnv()
	configureProc(cmd)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("candyland: could not probe %q --version (%v); a run will fail honestly if the CLI is missing or too old", claudeBin(), err)
		return
	}
	m := claudeVersionRe.FindStringSubmatch(string(out))
	if m == nil {
		log.Printf("candyland: could not parse claude version from %q", string(out))
		return
	}
	major, _ := strconv.Atoi(m[1])
	if major < minClaudeMajor {
		log.Printf("candyland: WARNING claude CLI %s.%s.%s is below %d.0.0 — the coordination bus needs `type:http` mcp-config support (claude 2.x); upgrade the CLI", m[1], m[2], m[3], minClaudeMajor)
	}
}

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

// mcpServerSpec is one entry in a Claude Code --mcp-config file. It supports
// both shapes claude's --mcp-config accepts: a STDIO entry is {command, args,
// env}; an HTTP entry is {type:"http", url}. The comms surface is HTTP (it talks
// to candyland's shared ooo bus); detritus is STDIO — a passive stdio child each
// agent spawns from the installed binary, exactly as a VSCode Claude session does.
type mcpServerSpec struct {
	Type    string            `json:"type,omitempty"`
	URL     string            `json:"url,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type mcpConfigFile struct {
	MCPServers map[string]mcpServerSpec `json:"mcpServers"`
}

// busMCPConfig writes a per-agent --mcp-config that points the coder at the
// app-hosted comms MCP endpoint over HTTP, identifying it by the agentID in the
// URL path. Returns the config path, or "" when no bus is wired (no flag is added
// then). It also adds a `detritus` STDIO entry so the agent has the
// kb_*/code_*/skill_* tools the Composition Constraint requires: detritus is a
// passive stdio MCP server, so each agent spawns its own detritus stdio child
// from the installed binary — exactly as a VSCode Claude session does — rather
// than sharing one long-lived process. The binary is resolved from DETRITUS_BIN
// (detritus sets this on the candyland process at launch) or PATH; if neither
// resolves, the entry is omitted (degraded — agents lack doctrine). The agent
// (claude) process is spawned with the full env, which its detritus stdio child
// inherits, so gh/HOME creds propagate. The inbox filters are registered globally
// at StartBus, so there is no per-agent registration here. The conductor stays
// pure Go — it only writes the config.
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

	servers := map[string]mcpServerSpec{
		"candyland-comms": {
			Type: "http",
			URL:  "http://" + loopbackHost(c.server.Address) + "/mcp/comms/" + agentID,
		},
	}
	// The agent needs detritus' kb_*/code_*/skill_* tools (the Composition
	// Constraint). detritus is a passive stdio MCP server: resolve the installed
	// binary and add a stdio entry {command, args:[]} so each agent spawns its own
	// detritus child (like a VSCode session). Resolve via DETRITUS_BIN, else PATH;
	// omit the entry (degraded) when neither resolves.
	if detritusBin := resolveDetritusBin(); detritusBin != "" {
		servers["detritus"] = mcpServerSpec{Command: detritusBin, Args: []string{}}
	}
	cfg := mcpConfigFile{MCPServers: servers}
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

// loopbackHost aligns the comms MCP handshake origin with the loopback interface
// the agent actually dials over. The bus is a same-host, loopback-only surface,
// but server.Address may carry a wildcard (0.0.0.0, ::) or a specific bind host —
// addressing the agent's HTTP client at that host makes it send a non-loopback
// Host header, which the go-sdk's DNS-rebinding guard rejects with 403 on a
// loopback connection. Rewriting the host to 127.0.0.1 (preserving the port) so
// the Host header matches the loopback connection lets that guard stay enabled
// rather than being blanket-disabled. A host that is already loopback, or an
// address we cannot split, is returned unchanged.
func loopbackHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return addr
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// resolveDetritusBin locates the detritus binary an agent's stdio MCP child
// should run. detritus sets DETRITUS_BIN on the candyland process at launch;
// fall back to PATH for a dev/manual run. Returns "" when neither resolves, so
// the caller omits the detritus entry (degraded — agents lack doctrine).
func resolveDetritusBin() string {
	if bin := os.Getenv("DETRITUS_BIN"); bin != "" {
		return bin
	}
	if bin, err := exec.LookPath("detritus"); err == nil {
		return bin
	}
	return ""
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
