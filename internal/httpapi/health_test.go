package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/version"
)

// stubActivity is a fixed-count healthActivity for driving the health handler
// without a live conductor.
type stubActivity struct {
	runs   int
	quests int
}

func (s stubActivity) ActiveRuns() int   { return s.runs }
func (s stubActivity) ActiveQuests() int { return s.quests }

func TestHealthHandler(t *testing.T) {
	started := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	// One running run, zero running quests — the identity+activity contract
	// detritus verifies before a smart takeover.
	handler := newHealthHandler(stubActivity{runs: 1, quests: 0}, func() string { return "window" }, started)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		OK           bool   `json:"ok"`
		Version      string `json:"version"`
		PID          int    `json:"pid"`
		StartedAt    string `json:"startedAt"`
		ActiveRuns   int    `json:"activeRuns"`
		ActiveQuests int    `json:"activeQuests"`
		UI           string `json:"ui"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.OK {
		t.Error("ok = false, want true")
	}
	if body.Version != version.Version {
		t.Errorf("version = %q, want %q", body.Version, version.Version)
	}
	if body.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", body.PID, os.Getpid())
	}
	if body.StartedAt != started.Format(time.RFC3339) {
		t.Errorf("startedAt = %q, want %q", body.StartedAt, started.Format(time.RFC3339))
	}
	if body.ActiveRuns != 1 || body.ActiveQuests != 0 {
		t.Errorf("activity = {%d, %d}, want {1, 0}", body.ActiveRuns, body.ActiveQuests)
	}
	if body.UI != "window" {
		t.Errorf("ui = %q, want %q", body.UI, "window")
	}
}

// TestHealthUIModeDefault verifies a nil mode holder reports "headless".
func TestHealthUIModeDefault(t *testing.T) {
	handler := newHealthHandler(stubActivity{}, nil, time.Now())
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	var body struct {
		UI string `json:"ui"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.UI != "headless" {
		t.Errorf("ui = %q, want headless", body.UI)
	}
}
