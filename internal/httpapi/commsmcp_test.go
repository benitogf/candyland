package httpapi

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The /mcp/comms/{agentID} endpoint is mounted on the app's own port and speaks
// MCP over Streamable HTTP: a client can initialize against a per-agent URL and
// list the comms tools, proving the handler is reachable and routes by the
// agentID in the path (a fresh server is built per request from that id).
func TestCommsMCPEndpointMountedAndInitializes(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	c := conductor.New(srv)
	c.StartBus()     // bus filters + reactor before Start
	Register(srv, c) // mounts /mcp/comms/{agentID} (RegisterCommsMCP)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(os.Interrupt)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connect := func(agentID string) *mcp.ClientSession {
		t.Helper()
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
		tr := &mcp.StreamableClientTransport{
			Endpoint:             "http://" + srv.Address + "/mcp/comms/" + agentID,
			HTTPClient:           &http.Client{},
			DisableStandaloneSSE: true,
		}
		cs, err := client.Connect(ctx, tr, nil)
		if err != nil {
			t.Fatalf("MCP initialize against /mcp/comms/%s failed: %v", agentID, err)
		}
		return cs
	}

	cs := connect("coder-1")
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{"comms_send": false, "comms_inbox": false, "brief_get": false, "graph_read": false, "graph_propose": false}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("comms tool %q not exposed by the HTTP endpoint", name)
		}
	}

	// A different agentID in the path is routed independently (the handler builds a
	// fresh per-agent server keyed on the path), so it initializes too.
	cs2 := connect("coder-2")
	cs2.Close()
}
