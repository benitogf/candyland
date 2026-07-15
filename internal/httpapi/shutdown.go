package httpapi

import (
	"log"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/benitogf/ooo"
)

// shutdownGraceDelay is the pause between flushing the 200 response and
// triggering the server shutdown, so the HTTP reply reaches detritus before the
// listener closes underneath it.
const shutdownGraceDelay = 200 * time.Millisecond

// registerShutdown mounts POST /api/shutdown — the flow-level control detritus
// uses for a smart takeover: it asks a version-skewed or UI-degraded sidecar to
// exit, but only when idle. The server is loopback-bound, so this is a local
// control surface, not a network-exposed kill switch. shutdown is the trigger
// that closes the server after the response is flushed (SIGTERM's path); it is a
// seam so tests can assert the decision without exiting the process.
func registerShutdown(server *ooo.Server, act healthActivity, shutdown func()) {
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/shutdown",
		Methods: ooo.Methods{"POST": ooo.MethodSpec{}},
		Handler: newShutdownHandler(act, shutdown),
	})
}

// newShutdownHandler builds the POST /api/shutdown handler. Busy (any running
// run or quest) → 409 with the counts and the process stays up; idle → 200 then
// a deferred shutdown so the reply flushes first. Never kills running work.
func newShutdownHandler(act healthActivity, shutdown func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		activeRuns := act.ActiveRuns()
		activeQuests := act.ActiveQuests()
		if activeRuns+activeQuests > 0 {
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, map[string]any{"activeRuns": activeRuns, "activeQuests": activeQuests})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		// Defer the actual shutdown so the 200 reaches the caller before the
		// listener closes underneath the response.
		time.AfterFunc(shutdownGraceDelay, shutdown)
	}
}

// serverShutdown is the real shutdown trigger passed to registerShutdown in
// production. It signals the process with SIGTERM rather than calling
// server.Close directly: main blocks in server.WaitClose, which only returns on
// a signal delivered to its notify channel. Calling Close in isolation shuts the
// API listener but leaves WaitClose parked, so main never returns and the SPA
// port stays bound (a takeover relaunch then cannot rebind). Routing through the
// real SIGTERM path unblocks WaitClose, so main runs its deferred cleanup and
// the process exits, releasing every port.
func serverShutdown(server *ooo.Server) func() {
	return func() {
		proc, err := os.FindProcess(os.Getpid())
		if err != nil {
			log.Printf("serverShutdown: could not find own process (%v); forcing exit", err)
			os.Exit(0)
		}
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			log.Printf("serverShutdown: could not deliver SIGTERM (%v); forcing exit", err)
			os.Exit(0)
		}
	}
}
