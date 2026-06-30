// Package httpapi wires the conductor to ooo: it opens the realtime run paths
// for subscription and exposes REST endpoints the React app calls to create
// runs, begin the build after planning, send Stop/Restart, and fetch the
// planning questions. No data is hardcoded in the client.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/gorilla/mux"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Register opens the realtime run paths and mounts the REST endpoints.
func Register(server *ooo.Server, c *conductor.Conductor) {
	server.OpenFilter("runs/*")      // enables both the list (runs/*) and item (runs/<id>) reads
	server.OpenFilter("quests/*")    // quest state (quests/* list + quests/<id> item); endpoints come in a later phase
	server.OpenFilter("campaigns/*") // campaign state (campaigns/* list + campaigns/<id> item); endpoints come in a later phase
	server.OpenFilter("audits/*")    // per-run verification audits (audits/* list + audits/<id> item)
	registerSystem(server)
	registerHealth(server)
	// Host the per-agent coordination-bus comms tools over HTTP at
	// /mcp/comms/{agentID}; spawned coders reach it via an http mcp-config entry
	// instead of a per-agent stdio process.
	RegisterCommsMCP(server)

	post := ooo.Methods{"POST": ooo.MethodSpec{}}
	get := ooo.Methods{"GET": ooo.MethodSpec{}}

	// Create a run (folders/prompt/title) — from the web UI or the trigger MCP.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var spec run.Spec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(spec.Prompt) == "" || len(spec.Folders) == 0 {
				http.Error(w, "a prompt and at least one folder are required", http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]string{"id": c.Create(spec)})
		},
	})

	// Read a single run's current snapshot (the trigger MCP's run_status reads
	// this; the web UI uses the ooo subscription instead). Served from storage so
	// it works for finished/untracked runs too.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs/{id}",
		Methods: get,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			obj, err := server.Storage.Get("runs/" + mux.Vars(r)["id"])
			if err != nil {
				http.Error(w, "run not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(obj.Data)
		},
	})

	// Export a run's full normalized trace: the stored Run plus its Audit (when
	// present) and the schema version, in a stable JSONL-friendly shape. Local
	// export only — a redaction seam lives on run.RunTrace for any future sync.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs/{id}/trace",
		Methods: get,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			id := mux.Vars(r)["id"]
			obj, err := server.Storage.Get("runs/" + id)
			if err != nil {
				http.Error(w, "run not found", http.StatusNotFound)
				return
			}
			var rr run.Run
			if err := json.Unmarshal(obj.Data, &rr); err != nil {
				http.Error(w, "run unreadable", http.StatusInternalServerError)
				return
			}
			trace := run.RunTrace{TraceVersion: run.TraceVersion, Run: &rr}
			// Audit is optional: a paused/stopped run is never audited, and a finished
			// run's audit may not be written yet. Attach it only when present.
			if a, err := server.Storage.Get("audits/" + id); err == nil {
				var au run.Audit
				if json.Unmarshal(a.Data, &au) == nil {
					trace.Audit = &au
				}
			}
			writeJSON(w, trace)
		},
	})

	// Begin the build. This is the detritus trigger (POST after POST /api/runs):
	// it just starts the run. The body is ignored; an empty body is fine.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs/{id}/begin",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			c.Begin(mux.Vars(r)["id"])
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// Stop / Restart.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs/{id}/command",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Command string `json:"command"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// Lean control surface: stop pauses a live run; restart re-runs from a
			// clean slate — including a FINISHED/failed run, whose executor has
			// exited (Restart relaunches it; Command only reaches a live executor).
			id := mux.Vars(r)["id"]
			var ok bool
			switch body.Command {
			case "stop":
				ok = c.Command(id, "stop")
			case "restart":
				ok = c.Restart(id)
			default:
				http.Error(w, "unknown command: "+body.Command, http.StatusBadRequest)
				return
			}
			if !ok {
				http.Error(w, "run not found or not "+body.Command+"-able", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// Cancel: abandon a run from any state (works during the planning Q&A, before
	// an executor exists — which stop can't reach). The run is kept as "cancelled"
	// in the Tasks history, not deleted.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs/{id}/cancel",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !c.Cancel(mux.Vars(r)["id"]) {
				http.Error(w, "run not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// Edit: change a finished run's task in place and reset it to planning.
	// Distinct from restart, which re-runs the task as-is.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs/{id}/edit",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var spec run.Spec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(spec.Prompt) == "" || len(spec.Folders) == 0 {
				http.Error(w, "a prompt and at least one folder are required", http.StatusBadRequest)
				return
			}
			if !c.Edit(mux.Vars(r)["id"], spec) {
				http.Error(w, "run not found or can't be edited while running", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// Archive: clear a run from the dashboard while keeping it in the Tasks
	// history (hide, never delete).
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/runs/{id}/archive",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !c.Archive(mux.Vars(r)["id"]) {
				http.Error(w, "run not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	registerQuestEndpoints(server, c)
}

// registerQuestEndpoints mounts the quest REST surface, mirroring the run
// endpoints' style: create, begin (kick the tick loop), read status, pause/resume,
// stop, and the child-runs / findings rollups. Quest state is served from storage
// (so it works for untracked quests too), exactly like the run snapshot endpoint.
func registerQuestEndpoints(server *ooo.Server, c *conductor.Conductor) {
	post := ooo.Methods{"POST": ooo.MethodSpec{}}
	get := ooo.Methods{"GET": ooo.MethodSpec{}}

	// Create a quest (objective/folders/scope/safety/verify/autonomy/deliver/…).
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/quests",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var spec run.QuestSpec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(spec.Objective) == "" || len(spec.Folders) == 0 {
				http.Error(w, "an objective and at least one folder are required", http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]string{"id": c.CreateQuest(spec)})
		},
	})

	// Read a single quest's snapshot + rollup (served from storage, like a run).
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/quests/{id}",
		Methods: get,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			obj, err := server.Storage.Get("quests/" + mux.Vars(r)["id"])
			if err != nil {
				http.Error(w, "quest not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(obj.Data)
		},
	})

	// Begin / continue execution: kick (or resume) the tick loop.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/quests/{id}/begin",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !c.BeginQuest(mux.Vars(r)["id"]) {
				http.Error(w, "quest not found or not begin-able (stopped/done)", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// Pause future ticks (no delete) with a reason.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/quests/{id}/pause",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !c.PauseQuest(mux.Vars(r)["id"], reasonFromBody(r)) {
				http.Error(w, "quest not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// Resume a paused quest.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/quests/{id}/resume",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !c.ResumeQuest(mux.Vars(r)["id"]) {
				http.Error(w, "quest not found or not paused", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// Stop: terminal halt with a reason.
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/quests/{id}/stop",
		Methods: post,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !c.StopQuest(mux.Vars(r)["id"], reasonFromBody(r)) {
				http.Error(w, "quest not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	// The quest's child runs (runs whose QuestID == id).
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/quests/{id}/runs",
		Methods: get,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, c.QuestChildRuns(mux.Vars(r)["id"]))
		},
	})

	// The quest's work items (findings).
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/quests/{id}/findings",
		Methods: get,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			q, ok := c.GetQuest(mux.Vars(r)["id"])
			if !ok {
				http.Error(w, "quest not found", http.StatusNotFound)
				return
			}
			writeJSON(w, q.WorkItems)
		},
	})
}

// reasonFromBody pulls an optional {"reason":"…"} from a pause/stop request body.
// An empty/absent body is fine — the conductor records a default reason then.
func reasonFromBody(r *http.Request) string {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	return body.Reason
}
