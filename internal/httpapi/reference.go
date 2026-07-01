package httpapi

import (
	"net/http"

	"github.com/benitogf/ooo"
	"github.com/gorilla/mux"
)

// referenceCollections maps the stable human "kind" a copy-reference handle uses
// to the storage collection its snapshot lives in. The client's src/lib/reference.js
// builds handles from the same kinds, so a copied handle always resolves back to
// the exact snapshot the item's dashboard page reads. "task" is the label the UI
// gives runs; "run" is accepted as an alias so either form resolves.
var referenceCollections = map[string]string{
	"task":     "runs",
	"run":      "runs",
	"quest":    "quests",
	"campaign": "campaigns",
}

// registerReference mounts GET /api/reference/{kind}/{id}. This is the resolver
// behind the one-click copy-reference control: fetching the copied URL returns
// the item's stored snapshot (the same JSON /api/runs|quests|campaigns/{id}
// serve), so a handle pasted into a VSCode Claude session resolves to the run's
// stored data. Served from storage so it works for finished/untracked items too.
func registerReference(server *ooo.Server) {
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/reference/{kind}/{id}",
		Methods: ooo.Methods{"GET": ooo.MethodSpec{}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			coll, ok := referenceCollections[mux.Vars(r)["kind"]]
			if !ok {
				http.Error(w, "unknown reference kind", http.StatusNotFound)
				return
			}
			obj, err := server.Storage.Get(coll + "/" + mux.Vars(r)["id"])
			if err != nil {
				http.Error(w, "reference not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(obj.Data)
		},
	})
}
