// Package httpapi wires the conductor to ooo: it opens the realtime run paths
// for subscription and exposes REST endpoints the React app calls to create
// runs, begin the build after planning, send Stop/Resume/Restart, and fetch the
// planning questions. No data is hardcoded in the client.
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/gorilla/mux"
)

// Question is one planning question (served to the client, not hardcoded there).
type Question struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Multi       bool     `json:"multi,omitempty"`
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

var questions = map[string][]Question{
	"non-developer": {
		{ID: "audience", Question: "Who is this for?", Options: []string{"Everyone", "Signed-in users", "Just admins"}},
		{ID: "frequency", Question: "Is this used once, or over and over?", Options: []string{"A one-time action", "Something people use regularly"}},
		{ID: "cares", Question: "Which of these matter for this change?", Multi: true, Options: []string{"Works on mobile", "Fast", "Matches our brand", "Accessible"}},
	},
	"developer": {
		{ID: "criteria", Question: `What does "done" look like? (acceptance criteria)`, Placeholder: "e.g. endpoint returns text/csv, the button downloads it, tests cover the filename", Suggestions: []string{"cover with tests", "match existing API client", "respect current filters"}},
		{ID: "constraints", Question: "Any constraints or existing patterns to follow?", Placeholder: "e.g. reuse the reports query layer; no new dependencies", Suggestions: []string{"no new dependencies", "reuse query layer", "follow current table styles"}},
		{ID: "scope", Question: "Anything explicitly out of scope?", Placeholder: "e.g. no scheduling, no email — just on-demand download", Suggestions: []string{"no scheduling", "no email delivery", "single PR"}},
	},
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Register opens the realtime run paths and mounts the REST endpoints.
func Register(server *ooo.Server, c *conductor.Conductor) {
	server.OpenFilter("runs/*") // enables both the list (runs/*) and item (runs/<id>) reads
	registerWorkspaces(server)

	post := ooo.Methods{"POST": ooo.MethodSpec{}}
	get := ooo.Methods{"GET": ooo.MethodSpec{}}

	// Create a run from the wizard (mode/workspace/prompt/title).
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var spec run.Spec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]string{"id": c.Create(spec)})
		},
	})

	// Begin the build after the planning Q&A completes.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs/{id}/begin",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Answers map[string]any `json:"answers"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			c.Begin(mux.Vars(r)["id"], body.Answers)
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// Stop / Resume / Restart.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs/{id}/command",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Command string `json:"command"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			c.Command(mux.Vars(r)["id"], body.Command)
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// Planning questions for a mode (served, not hardcoded in the client).
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/questions",
		Methods: get,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			mode := r.URL.Query().Get("mode")
			qs, ok := questions[mode]
			if !ok {
				qs = questions["non-developer"]
			}
			writeJSON(w, qs)
		},
	})
}
