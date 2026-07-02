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

	// Seed with the origin session's inherited MCP servers (obsidian, project
	// servers, …) that detritus enumerated and handed us via CANDYLAND_INHERITED_MCP.
	// candyland-comms + detritus are overlaid on top below, so our own wiring always
	// wins over any same-named inherited entry — the coordination bus and the
	// Composition Constraint tools can never be shadowed.
	servers := inheritedMCPServers()
	servers["candyland-comms"] = mcpServerSpec{
		Type: "http",
		// loopbackHost normalizes the app address to a loopback host so the agent
		// reaches the bus with a loopback Host header — the comms MCP handler relies
		// on that (it no longer disables the SDK's DNS-rebinding guard).
		URL: "http://" + loopbackHost(c.server.Address) + "/mcp/comms/" + agentID,
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

// inheritedMCPEnv names the env var carrying the origin Claude session's
// inherited MCP servers. detritus enumerates that set at launch, writes it to a
// private (0600) file shaped like an --mcp-config ({"mcpServers": {...}}), and
// sets this var to that file's PATH — the file, not an inline value, because the
// server blocks may hold secret env tokens. The name and file-path transport MUST
// match detritus' candyland_client.go (detritusOriginMCPEnv); a mismatch silently
// drops every inherited server.
const inheritedMCPEnv = "CANDYLAND_INHERITED_MCP"

// inheritedMCPServers reads the origin session's inherited MCP servers from the
// file whose path is in CANDYLAND_INHERITED_MCP and returns them as a fresh map
// (so callers can overlay their own entries). It degrades to an empty non-nil map
// on any problem — unset var, missing/unreadable file, or malformed JSON — so a
// broken handshake never blocks a spawn; the agent just gets the base comms +
// detritus wiring. The env values inside the file are never logged.
func inheritedMCPServers() map[string]mcpServerSpec {
	servers := map[string]mcpServerSpec{}
	path := os.Getenv(inheritedMCPEnv)
	if path == "" {
		return servers
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return servers
	}
	var cfg mcpConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return servers
	}
	for name, spec := range cfg.MCPServers {
		servers[name] = spec
	}
	return servers
}

// loopbackHost normalizes a server listen address to one presenting a loopback
// host, preserving the port. A wildcard bind (0.0.0.0, ::, or empty host) becomes
// 127.0.0.1 so the agent connects over loopback with a loopback Host header — the
// comms MCP handler no longer disables the SDK's DNS-rebinding guard, which 403s a
// non-loopback Host on a localhost→localhost call. An address already presenting a
// concrete host (including 127.0.0.1) is returned unchanged. Unparseable input is
// returned as-is rather than guessed at.
func loopbackHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
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
