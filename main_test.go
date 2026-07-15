package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEndpointFileWriteAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "endpoint.json")
	info := endpointInfo{APIPort: 8888, SpaPort: 8080, PID: 1234, Version: "v1.2.3", StartedAt: "2026-07-15T12:00:00Z"}

	writeEndpointFile(path, info)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("endpoint file not written: %v", err)
	}
	var got endpointInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("endpoint file not valid JSON: %v", err)
	}
	if got != info {
		t.Errorf("endpoint = %+v, want %+v", got, info)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
	if dirPerm := statPerm(t, filepath.Dir(path)); dirPerm != 0o700 {
		t.Errorf("dir perm = %o, want 700", dirPerm)
	}

	removeEndpointFile(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("endpoint file not removed (err=%v)", err)
	}
	// Removing an already-absent file is a no-op, never a panic/error.
	removeEndpointFile(path)
}

func statPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestUIModeHolder(t *testing.T) {
	h := &uiModeHolder{}
	if got := h.get(); got != "headless" {
		t.Errorf("zero-value get = %q, want headless", got)
	}
	h.set("window")
	if got := h.get(); got != "window" {
		t.Errorf("get = %q, want window", got)
	}
}

func TestOpenDashboardStub(t *testing.T) {
	prev := browserOpener
	defer func() { browserOpener = prev }()

	var opened string
	browserOpener = func(url string) error {
		opened = url
		return nil
	}
	// A bad URL makes waitForSPA return immediately (no host to dial).
	if !openDashboard("http://127.0.0.1:0/") {
		t.Fatal("openDashboard should report success when the opener succeeds")
	}
	if opened != "http://127.0.0.1:0/" {
		t.Errorf("opener url = %q", opened)
	}
}
