package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/gorilla/mux"
)

// Workspaces are persisted in ooo (workspaces/<id>) and served live to the
// client. Seeded once on startup; created/deleted via REST.
var defaultWorkspaces = []run.Workspace{
	{ID: "web", Label: "Web app", Folders: []string{"~/src/acme/web", "~/src/acme/ui-kit"}},
	{ID: "reports-api", Label: "Reports API", Folders: []string{"~/src/acme/reports-api", "~/src/acme/shared-go"}},
	{ID: "full-stack", Label: "Full stack (web + api)", Folders: []string{"~/src/acme/web", "~/src/acme/ui-kit", "~/src/acme/reports-api"}},
	{ID: "mobile", Label: "Mobile app", Folders: []string{"~/src/acme/mobile", "~/src/acme/ui-kit"}},
	{ID: "infra", Label: "Infra & deploy", Folders: []string{"~/src/acme/infra", "~/src/acme/terraform", "~/src/acme/ci"}},
	{ID: "docs", Label: "Docs site", Folders: []string{"~/src/acme/docs"}},
}

var wsSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.Trim(wsSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

func setWorkspace(server *ooo.Server, ws run.Workspace) {
	b, err := json.Marshal(ws)
	if err != nil {
		log.Printf("candyland: marshal workspace %s: %v", ws.ID, err)
		return
	}
	if _, err := server.Storage.Set("workspaces/"+ws.ID, json.RawMessage(b)); err != nil {
		log.Printf("candyland: persist workspace %s: %v", ws.ID, err)
	}
}

// SeedWorkspaces writes the defaults once (only if the path is empty). Must run
// AFTER server.Start() — the storage isn't live before then.
func SeedWorkspaces(server *ooo.Server) {
	keys, _ := server.Storage.Keys()
	for _, k := range keys {
		if strings.HasPrefix(k, "workspaces/") {
			return // already seeded
		}
	}
	for _, ws := range defaultWorkspaces {
		setWorkspace(server, ws)
	}
}

// registerWorkspaces opens the realtime path and mounts CRUD endpoints.
func registerWorkspaces(server *ooo.Server) {
	server.OpenFilter("workspaces/*")
	// Seeding happens after server.Start() (see SeedWorkspaces) — storage isn't
	// live at registration time.

	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/workspaces",
		Methods: ooo.Methods{"POST": ooo.MethodSpec{}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var ws run.Workspace
			if err := json.NewDecoder(r.Body).Decode(&ws); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if ws.ID == "" {
				ws.ID = slugify(ws.Label)
			}
			if ws.ID == "" || ws.Label == "" || len(ws.Folders) == 0 {
				http.Error(w, "label and at least one folder required", http.StatusBadRequest)
				return
			}
			setWorkspace(server, ws)
			writeJSON(w, ws)
		},
	})

	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/workspaces/{id}",
		Methods: ooo.Methods{"DELETE": ooo.MethodSpec{}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			id := mux.Vars(r)["id"]
			if err := server.Storage.Del("workspaces/" + id); err != nil {
				// Best-effort delete; the response stays 204 (the client reads the
				// live list), but a failed delete must not vanish silently.
				log.Printf("candyland: delete workspace %s: %v", id, err)
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})
}
