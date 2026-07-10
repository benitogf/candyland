package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/gorilla/mux"
)

// registerAccounting mounts GET /api/accounting/{kind}/{id}: the weighted token
// accounting for a run/quest/campaign — the persisted `accounting` rollup when
// present (quests/campaigns), else summed over its own agents. It reuses the
// copy-reference kind→collection map so "task"/"run"/"quest"/"campaign" all
// resolve, and reads from storage so it works for finished/untracked items too.
// The raw usage split, the single cost-proportional weighted total, and the
// derived cost are all computed server-side (run.SumTokenAccounting) — the UI
// never has to weight tokens itself.
func registerAccounting(server *ooo.Server) {
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/accounting/{kind}/{id}",
		Methods: ooo.Methods{"GET": ooo.MethodSpec{}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			coll, ok := referenceCollections[mux.Vars(r)["kind"]]
			if !ok {
				http.Error(w, "unknown accounting kind", http.StatusNotFound)
				return
			}
			obj, err := server.Storage.Get(coll + "/" + mux.Vars(r)["id"])
			if err != nil {
				http.Error(w, "accounting not found", http.StatusNotFound)
				return
			}
			// Run, Quest, and Campaign all carry an `agents` array and a persisted
			// `accounting` rollup; decode only those so one handler serves every kind
			// without importing their full shapes. Quests/campaigns roll their child
			// runs up into `accounting`, so it — not the lead-agents-only agents array
			// — is the authoritative breakdown. Runs and older records that never
			// stamped `accounting` fall back to summing their own agents.
			var entity struct {
				Agents     []run.Agent         `json:"agents"`
				Accounting run.TokenAccounting `json:"accounting"`
			}
			if err := json.Unmarshal(obj.Data, &entity); err != nil {
				http.Error(w, "accounting decode failed", http.StatusInternalServerError)
				return
			}
			if entity.Accounting != (run.TokenAccounting{}) {
				writeJSON(w, entity.Accounting)
				return
			}
			writeJSON(w, run.SumTokenAccounting(entity.Agents))
		},
	})
}
