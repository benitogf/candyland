package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShutdownBusy(t *testing.T) {
	called := false
	handler := newShutdownHandler(stubActivity{runs: 1}, func() { called = true })

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/api/shutdown", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var body struct {
		ActiveRuns   int `json:"activeRuns"`
		ActiveQuests int `json:"activeQuests"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ActiveRuns != 1 {
		t.Errorf("activeRuns = %d, want 1", body.ActiveRuns)
	}
	if called {
		t.Error("shutdown fired while busy — must never kill running work")
	}
}

func TestShutdownIdle(t *testing.T) {
	done := make(chan struct{})
	handler := newShutdownHandler(stubActivity{}, func() { close(done) })

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/api/shutdown", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK {
		t.Error("ok = false, want true")
	}
	// The shutdown hook fires after a short grace delay; wait for it.
	<-done
}
