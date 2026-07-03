package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/benitogf/candyland/internal/conductor"
)

// GET returns the effective settings (defaults before any POST); POST validates and
// persists, and a subsequent GET reflects the change. An out-of-enum value is 400.
func TestSettingsEndpoint(t *testing.T) {
	_, srv := questServer(t)
	base := "http://" + srv.Address

	// GET the defaults.
	resp, err := http.Get(base + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	var got conductor.Settings
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.Levels[conductor.RoleCoder].Model != "claude-opus-4-8" || got.Levels[conductor.RoleCoder].Thinking != "low" {
		t.Fatalf("default coder level = %+v", got.Levels[conductor.RoleCoder])
	}
	if got.Levels[conductor.RoleReviewer].Thinking != "high" {
		t.Fatalf("default reviewer thinking = %q, want high", got.Levels[conductor.RoleReviewer].Thinking)
	}

	// POST a valid change.
	resp = post(t, base+"/api/settings", conductor.Settings{Levels: map[string]conductor.LevelConfig{
		conductor.RoleCoder: {Model: "claude-sonnet-5", Thinking: "high"},
	}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST valid settings: status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// GET reflects it.
	resp, _ = http.Get(base + "/api/settings")
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.Levels[conductor.RoleCoder].Model != "claude-sonnet-5" {
		t.Errorf("persisted coder model = %q, want claude-sonnet-5", got.Levels[conductor.RoleCoder].Model)
	}

	// POST an invalid model → 400.
	resp = post(t, base+"/api/settings", conductor.Settings{Levels: map[string]conductor.LevelConfig{
		conductor.RoleCoder: {Model: "gpt-4"},
	}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST invalid model: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}
