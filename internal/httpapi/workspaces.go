package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/gorilla/mux"
)

// Workspaces are persisted in ooo (workspaces/<id>) and served live to the
// client — created and deleted via REST. A fresh install starts with none; the
// user creates real ones pointing at their own repositories. (No demo data.)

// wsNonAlnum strips every character that isn't a lowercase letter or digit.
var wsNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a workspace label into its ooo storage-key id. ooo keys only
// allow [a-zA-Z0-9*/] (benitogf/ooo key.IsValid) — a separator like '-' makes the
// key invalid, so Storage.Set silently rejects it and the workspace never
// persists. The id is therefore alphanumeric-only; the human-readable name lives
// in the Label field, so collapsing word boundaries here is harmless.
func slugify(s string) string {
	s = wsNonAlnum.ReplaceAllString(strings.ToLower(s), "")
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

// registerWorkspaces opens the realtime path and mounts CRUD endpoints.
func registerWorkspaces(server *ooo.Server, c *conductor.Conductor) {
	server.OpenFilter("workspaces/*")

	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/workspaces",
		Methods: ooo.Methods{"POST": ooo.MethodSpec{}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var ws run.Workspace
			if err := json.NewDecoder(r.Body).Decode(&ws); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// The id is the ooo storage key, so the server always derives a
			// key-safe one from the label — never trusting a client-sent id that
			// might contain characters ooo rejects.
			ws.ID = slugify(ws.Label)
			ws.Deleted = false // recreating a slug reattaches: a prior soft-delete is cleared
			if ws.ID == "" || len(ws.Folders) == 0 {
				http.Error(w, "label (with at least one letter or digit) and at least one folder required", http.StatusBadRequest)
				return
			}
			// Every folder must be a real directory the backend can read AND write
			// (it's where the agents work). Store the resolved absolute path so the
			// workspace never depends on an ambiguous ~ or relative path.
			for i, f := range ws.Folders {
				abs := expandPath(f)
				st := checkPath(abs)
				if !st.Exists || !st.Dir {
					http.Error(w, "folder is not a directory the server can see: "+f, http.StatusBadRequest)
					return
				}
				if !st.Readable || !st.Writable {
					http.Error(w, "folder is not readable + writable by the server: "+f, http.StatusBadRequest)
					return
				}
				ws.Folders[i] = abs
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

			// Block if any run referencing this workspace is still active
			// (planning|running|paused) — deleting it out from under a live run
			// would orphan it. Report the blockers so the UI can offer to cancel
			// them first, then retry.
			if blocking := c.BlockingRuns(id); len(blocking) > 0 {
				out := make([]blockingRun, 0, len(blocking))
				for _, b := range blocking {
					out = append(out, blockingRun{ID: b.ID, Title: runTitle(b), Status: b.Status})
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"blocking": out})
				return
			}

			// Soft-delete: mark the record deleted but KEEP it (and its folders on
			// disk) so the Tasks history still shows the name. Never Storage.Del.
			obj, err := server.Storage.Get("workspaces/" + id)
			if err != nil {
				w.WriteHeader(http.StatusNoContent) // already gone — idempotent
				return
			}
			var ws run.Workspace
			if json.Unmarshal(obj.Data, &ws) != nil {
				log.Printf("candyland: delete workspace %s: unreadable record", id)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			ws.Deleted = true
			setWorkspace(server, ws)
			w.WriteHeader(http.StatusNoContent)
		},
	})
}

// blockingRun is the JSON shape returned in a 409 when an active run keeps a
// workspace from being deleted — enough for the UI to list them and offer to
// cancel each before retrying.
type blockingRun struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// runTitle is the human label for a run in the blocking list: its title, else the
// first line of its prompt, else its id.
func runTitle(r run.Run) string {
	if t := strings.TrimSpace(r.Title); t != "" {
		return t
	}
	if p := strings.TrimSpace(r.Prompt); p != "" {
		first := strings.SplitN(p, "\n", 2)[0]
		// Cut on a rune boundary so a multi-byte character isn't split into invalid
		// UTF-8 (which would corrupt the JSON the UI renders).
		if rs := []rune(first); len(rs) > 72 {
			first = string(rs[:72]) + "…"
		}
		return first
	}
	return r.ID
}
