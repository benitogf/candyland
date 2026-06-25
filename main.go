// Candyland — a standalone agent-orchestration dashboard. A single binary that
// embeds the built React UI, serves it, and runs an ooo realtime backend whose
// conductor drives runs with real headless Claude Code — publishing live state
// the UI subscribes to. Built on the mono boilerplate (embed + ooo) and the ooo
// realtime stack.
package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/benitogf/candyland/internal/comms"
	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/httpapi"
	"github.com/benitogf/candyland/internal/spa"
	"github.com/benitogf/candyland/internal/version"
	"github.com/benitogf/ko"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed all:build
var uiFS embed.FS

var (
	host     = flag.String("host", "127.0.0.1", "host/interface to bind (default loopback; set 0.0.0.0 to expose on the network)")
	port     = flag.Int("port", 8888, "ooo realtime + api port")
	spaPort  = flag.Int("spaPort", 8080, "SPA http port")
	dataPath = flag.String("dataPath", "db/data", "data storage path")
	silence  = flag.Bool("silence", true, "silence ooo output")
)

func main() {
	// Hidden subcommand: the per-coder coordination-bus MCP server, launched by
	// the conductor via --mcp-config. It bridges a claude coder to the
	// conductor's ooo bus (the comms_*/graph_* tools as io.Remote* clients).
	if len(os.Args) > 1 && os.Args[1] == "comms-mcp" {
		runCommsMCP()
		return
	}

	flag.Parse()
	log.Printf("candyland %s", version.Version)

	server := &ooo.Server{
		ReadTimeout:  20 * time.Minute,
		WriteTimeout: 20 * time.Minute,
		IdleTimeout:  20 * time.Minute,
		Router:       mux.NewRouter(),
		Static:       true,
		Workers:      2,
		Storage: storage.New(storage.LayeredConfig{
			Memory:   storage.NewMemoryLayer(),
			Embedded: ko.NewEmbeddedStorage(*dataPath),
		}),
		Silence: *silence,
	}

	cond := conductor.New(server)
	httpapi.Register(server, cond)
	// Register the coordination bus (Realization B) before Start — filters must
	// be registered before the listener binds. A back-channel beside the stdout
	// loop; per-agent inboxes are registered at spawn.
	cond.StartBus()

	// Serve the embedded SPA on its own port; the client connects ooo-client to
	// the realtime port for live state.
	build, err := fs.Sub(uiFS, "build")
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		log.Printf("candyland UI → http://localhost:%d", *spaPort)
		if err := http.ListenAndServe(*host+":"+strconv.Itoa(*spaPort), spa.Handler(build, *port)); err != nil {
			log.Println("spa server:", err)
		}
	}()

	// Bind to loopback by default: a run drives headless Claude with tool access
	// and the API can browse the backend's filesystem, so it must not be on the
	// network unless the user explicitly opts in with --host 0.0.0.0.
	server.Start(*host + ":" + strconv.Itoa(*port))
	log.Printf("candyland API → http://%s:%d (bound to %s; use --host 0.0.0.0 to expose on the network)", *host, *port, *host)
	cond.ReconcileOrphans() // storage is live only after Start; close out phantom runs from a prior process
	server.WaitClose()
}

// runCommsMCP serves the per-coder coordination-bus MCP over stdio. The
// conductor passes the bus address + this agent's identity via env when it
// generates the --mcp-config; identity rides in the payload `from`.
func runCommsMCP() {
	addr := os.Getenv("CANDYLAND_BUS_ADDR")
	self := os.Getenv("CANDYLAND_AGENT_ID")
	orchestrator := os.Getenv("CANDYLAND_ORCHESTRATOR")
	if addr == "" || self == "" {
		log.Fatal("comms-mcp: CANDYLAND_BUS_ADDR and CANDYLAND_AGENT_ID are required")
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "candyland-comms", Version: version.Version}, nil)
	comms.RegisterTools(srv, comms.NewClient(addr, self, orchestrator))
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("comms-mcp: %v", err)
	}
}
