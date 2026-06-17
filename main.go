// Candyland — a standalone agent-orchestration dashboard. A single binary that
// embeds the built React UI, serves it, and runs an ooo realtime backend whose
// conductor drives runs (real headless claude when available, else a scripted
// executor) — publishing live state the UI subscribes to. Built on the mono
// boilerplate (embed + ooo) and the ooo realtime stack.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/httpapi"
	"github.com/benitogf/candyland/internal/spa"
	"github.com/benitogf/ko"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

//go:embed all:build
var uiFS embed.FS

var (
	port     = flag.Int("port", 8888, "ooo realtime + api port")
	spaPort  = flag.Int("spaPort", 8080, "SPA http port")
	dataPath = flag.String("dataPath", "db/data", "data storage path")
	silence  = flag.Bool("silence", true, "silence ooo output")
)

func main() {
	flag.Parse()

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

	// Serve the embedded SPA on its own port; the client connects ooo-client to
	// the realtime port for live state.
	build, err := fs.Sub(uiFS, "build")
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		log.Printf("candyland UI → http://localhost:%d", *spaPort)
		if err := http.ListenAndServe(":"+strconv.Itoa(*spaPort), spa.Handler(build)); err != nil {
			log.Println("spa server:", err)
		}
	}()

	server.Start("0.0.0.0:" + strconv.Itoa(*port))
	server.WaitClose()
}
