package httpapi

import (
	"net/http"
	"os"
	"time"

	"github.com/benitogf/candyland/internal/version"
	"github.com/benitogf/ooo"
)

// healthActivity is the slice of the conductor /api/health reports on: the live
// run/quest counts it exposes as proof of activity. Kept as an interface so the
// handler can be unit-tested with a stub, and so health never depends on the
// conductor's internals.
type healthActivity interface {
	ActiveRuns() int
	ActiveQuests() int
}

// registerHealth mounts GET /api/health — a cheap liveness + identity probe for
// detritus, which drives candyland over REST and must be able to tell THIS
// candyland apart from a foreign app on the same loopback port. Unlike
// /api/system it never shells out to probe CLI versions, so it's safe to poll as
// a hot health check. act supplies the live activity counts; uiMode reports the
// resolved UI surface (window/browser/headless).
func registerHealth(server *ooo.Server, act healthActivity, uiMode func() string) {
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/health",
		Methods: ooo.Methods{"GET": ooo.MethodSpec{}},
		Handler: newHealthHandler(act, uiMode, time.Now()),
	})
}

// newHealthHandler builds the /api/health handler, capturing the process
// identity (pid, startedAt) once so the hot poll never recomputes it. The body
// carries {ok, version, pid, startedAt, activeRuns, activeQuests, ui} — enough
// for detritus to verify identity (version + non-empty body), decide takeover
// (activity counts), and print the real UI outcome.
func newHealthHandler(act healthActivity, uiMode func() string, startedAt time.Time) http.HandlerFunc {
	pid := os.Getpid()
	started := startedAt.Format(time.RFC3339)
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"ok":           true,
			"version":      version.Version,
			"pid":          pid,
			"startedAt":    started,
			"activeRuns":   act.ActiveRuns(),
			"activeQuests": act.ActiveQuests(),
			"ui":           resolveUIMode(uiMode),
		})
	}
}

// resolveUIMode reads the current mode from the holder, defaulting to "headless"
// when no holder is wired (e.g. a server-only test).
func resolveUIMode(uiMode func() string) string {
	if uiMode == nil {
		return "headless"
	}
	if mode := uiMode(); mode != "" {
		return mode
	}
	return "headless"
}
