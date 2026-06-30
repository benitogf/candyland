package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// The campaign REST surface mirrors the quest endpoints: create returns {id}, the
// snapshot is served from storage, and pause/stop drive the lifecycle. (begin is not
// exercised here — it launches the supervisor, which spawns a real `claude`; the
// supervisor flow's deterministic oracle lives in the conductor package's stub test.)
func TestCampaignEndpointsLifecycle(t *testing.T) {
	_, srv := questServer(t) // questServer wires Register, which mounts the campaign endpoints too
	base := "http://" + srv.Address

	// Create.
	resp := post(t, base+"/api/campaigns", run.CampaignSpec{Input: "build the thing", Folders: []string{"/repo"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d, want 200", resp.StatusCode)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("create did not return an id")
	}

	// Read snapshot: campaigns default to L2 (never L1) and capture the input.
	get, err := http.Get(base + "/api/campaigns/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cam run.Campaign
	_ = json.NewDecoder(get.Body).Decode(&cam)
	get.Body.Close()
	if cam.ID != created.ID || cam.OriginalInput != "build the thing" || cam.Status != "running" {
		t.Fatalf("snapshot wrong: %+v", cam)
	}
	if cam.AutonomyLevel != run.AutonomyGatePR {
		t.Errorf("campaign autonomy = %q, want L2 (never L1)", cam.AutonomyLevel)
	}

	// Pause with a reason (idle campaign — no supervisor running yet).
	if r := post(t, base+"/api/campaigns/"+created.ID+"/pause", map[string]string{"reason": "hold"}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("pause status = %d, want 204", r.StatusCode)
	}
	get, _ = http.Get(base + "/api/campaigns/" + created.ID)
	_ = json.NewDecoder(get.Body).Decode(&cam)
	get.Body.Close()
	if cam.Status != "paused" || cam.PauseReason != "hold" {
		t.Fatalf("pause not applied: status=%q reason=%q", cam.Status, cam.PauseReason)
	}

	// Stop is terminal.
	if r := post(t, base+"/api/campaigns/"+created.ID+"/stop", map[string]string{"reason": "fin"}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("stop status = %d, want 204", r.StatusCode)
	}
	// A stopped campaign can't begin.
	if r := post(t, base+"/api/campaigns/"+created.ID+"/begin", nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("begin on stopped campaign = %d, want 409", r.StatusCode)
	}

	// Child quests + runs endpoints return arrays (empty here).
	for _, path := range []string{"/quests", "/runs"} {
		g, _ := http.Get(base + "/api/campaigns/" + created.ID + path)
		if g.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, g.StatusCode)
		}
		g.Body.Close()
	}
}

// Create validation: an input and at least one folder are required (mirrors the
// run/quest-create validation).
func TestCampaignCreateValidation(t *testing.T) {
	_, srv := questServer(t)
	base := "http://" + srv.Address
	if r := post(t, base+"/api/campaigns", run.CampaignSpec{Folders: []string{"/repo"}}); r.StatusCode != http.StatusBadRequest {
		t.Errorf("missing input = %d, want 400", r.StatusCode)
	}
	if r := post(t, base+"/api/campaigns", run.CampaignSpec{Input: "x"}); r.StatusCode != http.StatusBadRequest {
		t.Errorf("missing folders = %d, want 400", r.StatusCode)
	}
}

// Unknown-campaign reads/commands 404.
func TestCampaignEndpointsNotFound(t *testing.T) {
	_, srv := questServer(t)
	base := "http://" + srv.Address
	if g, _ := http.Get(base + "/api/campaigns/nope"); g.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown campaign = %d, want 404", g.StatusCode)
	}
	if r := post(t, base+"/api/campaigns/nope/pause", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("pause unknown campaign = %d, want 404", r.StatusCode)
	}
}
