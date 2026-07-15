// Candyland — a standalone agent-orchestration dashboard. A single binary that
// embeds the built React UI, serves it, and runs an ooo realtime backend whose
// conductor drives runs with real headless Claude Code — publishing live state
// the UI subscribes to. Built on the mono boilerplate (embed + ooo) and the ooo
// realtime stack.
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/datadir"
	"github.com/benitogf/candyland/internal/httpapi"
	"github.com/benitogf/candyland/internal/spa"
	"github.com/benitogf/candyland/internal/version"
	"github.com/benitogf/ko"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

//go:embed all:build
var uiFS embed.FS

var (
	host     = flag.String("host", "127.0.0.1", "host/interface to bind (default loopback; set 0.0.0.0 to expose on the network)")
	port     = flag.Int("port", 8888, "ooo realtime + api port")
	spaPort  = flag.Int("spaPort", 8080, "SPA http port")
	dataPath = flag.String("dataPath", "", "data storage path (default: ~/.candyland/db)")
	silence  = flag.Bool("silence", true, "silence ooo output")

	showVersion = flag.Bool("version", false, "print the candyland version and exit")

	// Desktop window (webview build only; ignored by the default headless build).
	headless     = flag.Bool("headless", false, "serve the UI on spaPort only, without opening the desktop window")
	openBrowser  = flag.Bool("openBrowser", false, "when no desktop window opens, open the dashboard in the default browser")
	windowW      = flag.Int("width", 1280, "desktop window width")
	windowH      = flag.Int("height", 820, "desktop window height")
	debugWebview = flag.Bool("debugWebview", false, "open the desktop window with devtools")
)

// uiModeHolder is the shared UI-mode cell: main seeds it "headless", runUI
// updates it to "window"/"browser" when a surface opens, and /api/health reads it
// via get. Backed by atomic.Value so the health poll and runUI never race.
type uiModeHolder struct{ v atomic.Value }

func (h *uiModeHolder) set(mode string) { h.v.Store(mode) }

func (h *uiModeHolder) get() string {
	if mode, ok := h.v.Load().(string); ok {
		return mode
	}
	return "headless"
}

// endpointInfo is the ~/.candyland/endpoint.json advertisement: enough for a
// launcher to discover the running sidecar's ports and verify its identity
// against /api/health before trusting it.
type endpointInfo struct {
	APIPort   int    `json:"apiPort"`
	SpaPort   int    `json:"spaPort"`
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	StartedAt string `json:"startedAt"`
}

func main() {
	flag.Parse()

	// --version is a pure query: print and exit before any server setup, so a
	// launcher can learn the installed binary's version with one cheap exec.
	if *showVersion {
		fmt.Println(version.Version)
		os.Exit(0)
	}

	log.Printf("candyland %s", version.Version)

	// The pinned claude CLI must support `type:http` mcp-config entries (the
	// coordination bus is now an HTTP MCP endpoint). The installer floats the
	// latest CLI, so assert a floor at startup — warn (don't hard-fail) if below.
	conductor.CheckClaudeVersion()

	// Resolve the data directory: an explicit --dataPath wins verbatim; unset
	// resolves to ~/.candyland/db, creating it and migrating any legacy
	// project-local ./db/data on a best-effort basis (never fails startup).
	resolvedDataPath := datadir.Resolve(*dataPath)

	server := &ooo.Server{
		ReadTimeout:  20 * time.Minute,
		WriteTimeout: 20 * time.Minute,
		IdleTimeout:  20 * time.Minute,
		Router:       mux.NewRouter(),
		Static:       true,
		Workers:      2,
		Storage: storage.New(storage.LayeredConfig{
			Memory:   storage.NewMemoryLayer(),
			Embedded: ko.NewEmbeddedStorage(resolvedDataPath),
		}),
		Silence: *silence,
	}

	// Seed the UI-mode holder headless; runUI promotes it once a surface opens.
	uiMode := &uiModeHolder{}
	uiMode.set("headless")

	cond := conductor.New(server)
	httpapi.Register(server, cond, uiMode.get)
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

	// Advertise the bound endpoint at ~/.candyland/endpoint.json (fixed per-user
	// path, independent of --dataPath) so a launcher can discover this sidecar.
	// Removed on clean exit both here (defer) and on the SIGTERM path (preclose).
	endpointPath := datadir.EndpointPath()
	writeEndpointFile(endpointPath, endpointInfo{
		APIPort:   *port,
		SpaPort:   *spaPort,
		PID:       os.Getpid(),
		Version:   version.Version,
		StartedAt: time.Now().Format(time.RFC3339),
	})
	server.RegisterPreClose(func() { removeEndpointFile(endpointPath) })
	defer removeEndpointFile(endpointPath)

	cond.ReconcileOrphans() // storage is live only after Start; close out phantom runs from a prior process
	runUI(server, "http://localhost:"+strconv.Itoa(*spaPort), *headless, *windowW, *windowH, *debugWebview, *openBrowser, uiMode.set)
}

// writeEndpointFile writes the endpoint advertisement to path (0600, parent dir
// 0700). Best-effort: any failure is logged and swallowed — a missing endpoint
// file only degrades discovery, it must never abort startup. A "" path (no home
// directory) skips advertising.
func writeEndpointFile(path string, info endpointInfo) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("candyland: could not create endpoint dir %q (%v); discovery file not written", filepath.Dir(path), err)
		return
	}
	data, err := json.Marshal(info)
	if err != nil {
		log.Printf("candyland: could not marshal endpoint info (%v); discovery file not written", err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Printf("candyland: could not write endpoint file %q (%v); discovery degraded", path, err)
	}
}

// removeEndpointFile deletes the endpoint advertisement on clean exit. A missing
// file is fine (consumers verify via health before trusting it, so a stale file
// is harmless); any other error is logged and swallowed.
func removeEndpointFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("candyland: could not remove endpoint file %q (%v)", path, err)
	}
}
