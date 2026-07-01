package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// The copy-reference handle for each item kind resolves to that item's stored
// snapshot via GET /api/reference/{kind}/{id} — the resolver behind the one-click
// copy control. A task, a quest, and a campaign each round-trip; an unknown kind
// and a missing id are 404s.
func TestReferenceResolvesEachKind(t *testing.T) {
	c, srv := questServer(t)
	base := "http://" + srv.Address

	runID := c.Create(run.Spec{Prompt: "do the thing", Folders: []string{"/repo"}})
	questID := c.CreateQuest(run.QuestSpec{Objective: "tidy up", Folders: []string{"/repo"}, AutonomyLevel: run.AutonomyReportOnly})
	campaignID := c.CreateCampaign(run.CampaignSpec{Input: "ship it", Folders: []string{"/repo"}})

	// Each kind's handle resolves to a JSON snapshot carrying the item's own id —
	// proving the copied reference points at that run's stored data.
	cases := []struct{ kind, id string }{
		{"task", runID},
		{"run", runID},
		{"quest", questID},
		{"campaign", campaignID},
	}
	for _, tc := range cases {
		resp, err := http.Get(base + "/api/reference/" + tc.kind + "/" + tc.id)
		if err != nil {
			t.Fatalf("get %s/%s: %v", tc.kind, tc.id, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("reference %s/%s: status = %d, want 200", tc.kind, tc.id, resp.StatusCode)
		}
		var snap map[string]any
		if err := json.Unmarshal(body, &snap); err != nil {
			t.Fatalf("reference %s/%s: body not JSON: %v", tc.kind, tc.id, err)
		}
		if snap["id"] != tc.id {
			t.Fatalf("reference %s/%s: resolved id = %v, want %s", tc.kind, tc.id, snap["id"], tc.id)
		}
	}

	// Unknown kind → 404.
	resp, err := http.Get(base + "/api/reference/bogus/" + runID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown kind: status = %d, want 404", resp.StatusCode)
	}

	// Known kind, missing id → 404.
	resp, err = http.Get(base + "/api/reference/task/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing id: status = %d, want 404", resp.StatusCode)
	}
}
