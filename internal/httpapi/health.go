package httpapi

import (
	"net/http"

	"github.com/benitogf/candyland/internal/version"
	"github.com/benitogf/ooo"
)

// registerHealth mounts GET /api/health — a cheap liveness probe for detritus,
// which drives candyland over REST. Unlike /api/system it never shells out to
// probe CLI versions, so it's safe to poll as a hot health check.
func registerHealth(server *ooo.Server) {
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/health",
		Methods: ooo.Methods{"GET": ooo.MethodSpec{}},
		Handler: healthHandler,
	})
}

// healthHandler returns 200 with a tiny JSON body. It never shells out.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "version": version.Version})
}
