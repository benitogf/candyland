package conductor

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/io"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// CPB5: at spawn the conductor generates a --mcp-config that launches
// `candyland comms-mcp` wired (via env) to the bus as the given agent, and
// registers that agent's inbox live. The conductor itself never calls a model.
func TestBusMCPConfigWiresAgentToBus(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := New(srv)
	c.StartBus() // registers global filters + reactor before Start
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(os.Interrupt)

	path := c.busMCPConfig("run1", "coder-1")
	if path == "" {
		t.Fatal("expected a --mcp-config path when the bus is wired")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var cfg mcpConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
	spec, ok := cfg.MCPServers["candyland-comms"]
	if !ok {
		t.Fatalf("config missing candyland-comms server: %s", data)
	}
	if len(spec.Args) != 1 || spec.Args[0] != "comms-mcp" {
		t.Errorf("expected args [comms-mcp], got %v", spec.Args)
	}
	if spec.Env["CANDYLAND_BUS_ADDR"] != srv.Address {
		t.Errorf("BUS_ADDR = %q, want server address %q", spec.Env["CANDYLAND_BUS_ADDR"], srv.Address)
	}
	if spec.Env["CANDYLAND_AGENT_ID"] != "coder-1" || spec.Env["CANDYLAND_ORCHESTRATOR"] != OrchestratorID {
		t.Errorf("agent/orchestrator env wrong: %v", spec.Env)
	}

	// The agent's inbox is now live: a peer can send to it over the bus (proving
	// post-spawn registration took effect on the HTTP path).
	if !c.busAgents["coder-1"] {
		t.Error("coder-1 inbox not marked registered")
	}
	rc := io.RemoteConfig{Host: srv.Address, Client: &http.Client{}}
	if err := io.RemotePush(rc, bus.InboxGlob("coder-1"), bus.Envelope{From: "peer", To: "coder-1", Type: bus.MsgQuestion}); err != nil {
		t.Fatalf("send to the newly-registered inbox failed: %v", err)
	}
	msgs, err := io.RemoteGetList[bus.Envelope](rc, bus.InboxGlob("coder-1"))
	if err != nil || len(msgs) != 1 || msgs[0].Data.Seq == 0 {
		t.Errorf("expected one seq'd message in coder-1's live inbox, got %d (err %v)", len(msgs), err)
	}
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
