package httpapi

import (
	"net/http"

	"github.com/benitogf/candyland/internal/comms"
	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/version"
	"github.com/benitogf/ooo"
	"github.com/gorilla/mux"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// commsMCPPath is the per-agent comms MCP endpoint. The agent id rides in the
// path so a single endpoint, hosted on the app's own realtime port, serves every
// coder — replacing the former per-agent comms stdio MCP process. Claude
// reaches it with an `{"type":"http","url":".../mcp/comms/<agentID>"}` mcp-config
// entry.
const commsMCPPath = "/mcp/comms/{agentID}"

// RegisterCommsMCP mounts the coordination-bus comms tools over Streamable HTTP
// on the ooo server's router at /mcp/comms/{agentID}. For each request the agent
// id is read from the path and a fresh mcp.Server is built with the comms tools
// wired to this same app's bus as that agent. The stateless handler needs no
// session retention: each tool call is an independent ooo.Remote* round-trip, and
// identity is carried by the path, not a session.
//
// The bus address is the app's own realtime address (server.Address), read at
// request time because it is only assigned once the server is listening — the
// route itself is registered before Start (the data wildcard must already know to
// defer to it). With the app bound to loopback by default the agent reaches the
// bus from the same host, and the conductor points each agent at a loopback host
// (loopbackHost in coordinator.go) so the request carries a loopback Host header.
// That keeps the SDK's DNS-rebinding guard satisfied without disabling it — a
// loopback→loopback call passes the localhost check on its own. The handler
// is mirrored onto the route oracle so the ooo data wildcard defers to it
// regardless of registration order — it shares the app's single port rather than
// spawning a process per agent.
func RegisterCommsMCP(server *ooo.Server) {
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		agentID := mux.Vars(r)["agentID"]
		if agentID == "" {
			return nil // 400 Bad Request — no identity in the path
		}
		srv := mcp.NewServer(&mcp.Implementation{Name: "candyland-comms", Version: version.Version}, nil)
		comms.RegisterTools(srv, comms.NewClient(server.Address, agentID, conductor.OrchestratorID))
		return srv
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})

	// Mount on the shared router for all methods the Streamable HTTP transport
	// uses (POST to call, GET for the event stream, DELETE to end a session).
	// RouterMutate serializes with the syncRouter dispatch; the oracle route makes
	// the data wildcard defer to this path regardless of registration order.
	methods := []string{http.MethodGet, http.MethodPost, http.MethodDelete}
	server.RouterMutate(func() {
		server.Router.Handle(commsMCPPath, handler).Methods(methods...)
	})
	server.RegisterOracleRoute(commsMCPPath, methods)
}
