package conductor

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/io"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// CPB5: at spawn the conductor generates a --mcp-config pointing the agent at the
// app-hosted comms MCP endpoint over HTTP, identified by the agentID in the URL
// path. The conductor itself never calls a model.
func TestBusMCPConfigWiresAgentToBus(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := New(srv)
	c.StartBus() // registers global filters + reactor before Start
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(os.Interrupt)

	// DETRITUS_BIN unset: the comms entry is always present; the detritus entry is
	// absent only when `detritus` is also not on PATH (gate that case on LookPath).
	os.Unsetenv("DETRITUS_BIN")
	path := c.busMCPConfig("run1", "coder-1")
	if path == "" {
		t.Fatal("expected a --mcp-config path when the bus is wired")
	}
	cfg := readConfig(t, path)
	spec, ok := cfg.MCPServers["candyland-comms"]
	if !ok {
		t.Fatalf("config missing candyland-comms server: %+v", cfg)
	}
	if spec.Type != "http" {
		t.Errorf("comms entry type = %q, want http", spec.Type)
	}
	wantSuffix := "/mcp/comms/coder-1"
	if !strings.HasPrefix(spec.URL, "http://"+srv.Address) || !strings.HasSuffix(spec.URL, wantSuffix) {
		t.Errorf("comms url = %q, want http://%s...%s", spec.URL, srv.Address, wantSuffix)
	}
	if _, onPath := exec.LookPath("detritus"); onPath != nil {
		// Neither DETRITUS_BIN nor PATH resolves detritus → entry omitted (degraded).
		if _, has := cfg.MCPServers["detritus"]; has {
			t.Errorf("detritus entry must be absent when DETRITUS_BIN unset and detritus not on PATH: %+v", cfg)
		}
	}

	// DETRITUS_BIN set: a `detritus` STDIO entry is added so the agent has
	// kb_*/code_*/skill_* (the Composition Constraint). It is {command, args:[]} —
	// a passive stdio child the agent spawns, like a VSCode session.
	const detBin = "/opt/detritus/bin/detritus"
	t.Setenv("DETRITUS_BIN", detBin)
	cfg = readConfig(t, c.busMCPConfig("run1", "coder-2"))
	det, ok := cfg.MCPServers["detritus"]
	if !ok {
		t.Fatalf("config missing detritus server when DETRITUS_BIN is set: %+v", cfg)
	}
	if det.Command != detBin {
		t.Errorf("detritus entry command = %q, want %q", det.Command, detBin)
	}
	if len(det.Args) != 0 {
		t.Errorf("detritus entry args = %v, want empty", det.Args)
	}
	if det.Type != "" || det.URL != "" {
		t.Errorf("detritus stdio entry must not carry type/url, got %+v", det)
	}
	if comm := cfg.MCPServers["candyland-comms"]; !strings.HasSuffix(comm.URL, "/mcp/comms/coder-2") {
		t.Errorf("comms url for coder-2 = %q, want suffix /mcp/comms/coder-2", comm.URL)
	}

	// The agent's inbox is live via the global filters registered at StartBus: a
	// peer can send to it over the bus and read it back (no per-agent registration).
	rc := io.RemoteConfig{Host: srv.Address, Client: &http.Client{}}
	if err := io.RemotePush(rc, bus.InboxGlob("coder-1"), bus.Envelope{From: "peer", To: "coder-1", Type: bus.MsgQuestion}); err != nil {
		t.Fatalf("send to the newly-registered inbox failed: %v", err)
	}
	msgs, err := io.RemoteGetList[bus.Envelope](rc, bus.InboxGlob("coder-1"))
	if err != nil || len(msgs) != 1 || msgs[0].Data.Seq == 0 {
		t.Errorf("expected one seq'd message in coder-1's live inbox, got %d (err %v)", len(msgs), err)
	}
}

// The comms URL host is normalized to loopback so an agent on the same host can
// reach a server bound to an unspecified/all-interfaces address.
func TestLoopbackHost(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8080":     "127.0.0.1:8080",
		"[::]:8080":        "127.0.0.1:8080",
		":8080":            "127.0.0.1:8080",
		"127.0.0.1:0":      "127.0.0.1:0",
		"192.168.1.5:9000": "192.168.1.5:9000",
		"not-an-addr":      "not-an-addr",
	}
	for in, want := range cases {
		if got := loopbackHost(in); got != want {
			t.Errorf("loopbackHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// The inherited origin MCP set (DETRITUS_ORIGIN_MCP → path to a JSON file) is
// merged into each per-agent config while candyland-comms + detritus are
// preserved. Unset/unreadable/malformed input degrades to no inherited servers.
func TestBusMCPConfigMergesInheritedSet(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := New(srv)
	c.StartBus()
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(os.Interrupt)

	// A JSON file describing an inherited server (and one that collides with a
	// preserved name, to prove the overlay wins).
	inherited := mcpConfigFile{MCPServers: map[string]mcpServerSpec{
		"origin-thing":    {Command: "/bin/origin", Args: []string{"--flag"}},
		"candyland-comms": {Command: "/should/be/overridden"},
	}}
	data, err := json.Marshal(inherited)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "origin.json")
	if err := os.WriteFile(f, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DETRITUS_ORIGIN_MCP", f)

	cfg := readConfig(t, c.busMCPConfig("run1", "coder-1"))
	origin, ok := cfg.MCPServers["origin-thing"]
	if !ok || origin.Command != "/bin/origin" {
		t.Errorf("inherited origin-thing not merged: %+v", cfg)
	}
	comms, ok := cfg.MCPServers["candyland-comms"]
	if !ok || comms.Type != "http" || comms.Command != "" {
		t.Errorf("candyland-comms must be preserved over an inherited collision: %+v", comms)
	}
}

func readConfig(t *testing.T, path string) mcpConfigFile {
	t.Helper()
	if path == "" {
		t.Fatal("expected a --mcp-config path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var cfg mcpConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
	return cfg
}

// Without a bus (serverless test conductor), no --mcp-config is produced.
func TestBusMCPConfigNoBus(t *testing.T) {
	c := New(nil)
	if got := c.busMCPConfig("run1", "coder-1"); got != "" {
		t.Errorf("expected no config without a bus, got %q", got)
	}
}

// CPB6: the retry cap is K=3 (no quota thrash).
func TestEscalationCapIsThree(t *testing.T) {
	if maxReplans() != 3 || maxAttempts() != 3 {
		t.Errorf("K must be 3 to bound quota, got replans=%d attempts=%d", maxReplans(), maxAttempts())
	}
}

// CPB6: when the conductor gives up, the still-open nodes escalate to blocked
// (done nodes are left alone).
func TestEscalateOpenNodesBlocksOpenOnly(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := New(srv)
	c.StartBus()
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(os.Interrupt)

	c.publishGraphNodes([]partitionTask{{ID: "t1", Title: "one"}, {ID: "t2", Title: "two"}})
	if err := c.bus.CommitNode(srv, bus.GraphNode{ID: "t1", Status: bus.NodeDone}); err != nil {
		t.Fatal(err)
	}
	c.escalateOpenNodes("no working split after 3 attempts")

	byID := map[string]bus.GraphNode{}
	for _, n := range c.bus.ReadNodes(srv) {
		byID[n.ID] = n
	}
	if byID["t1"].Status != bus.NodeDone {
		t.Errorf("done node t1 must not be escalated, got %q", byID["t1"].Status)
	}
	if byID["t2"].Status != bus.NodeBlocked || byID["t2"].Reason == "" {
		t.Errorf("open node t2 should be blocked with a reason, got %q (%q)", byID["t2"].Status, byID["t2"].Reason)
	}
}
