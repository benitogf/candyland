package control

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/httpapi"
	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// sidecar stands up a real candyland backend (conductor + httpapi) on an
// ephemeral port — the running sidecar the trigger MCP talks to.
func sidecar(t *testing.T) (*Client, func()) {
	t.Helper()
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	httpapi.Register(srv, conductor.New(srv))
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	return NewClient(srv.Address), func() { srv.Close(os.Interrupt) }
}

// The trigger client launches a run over the sidecar's HTTP API (Create+Begin —
// the VSCode session's entry point) and reads it back via run_status. The run's
// folders come from the launch call, and it begins immediately (no planning
// Q&A). A non-git temp folder makes it fail fast, so the test exercises the
// trigger contract — not the delivery pipeline — without spawning claude.
func TestControlLaunchAndStatus(t *testing.T) {
	cl, stop := sidecar(t)
	defer stop()

	folder := t.TempDir()
	id, err := cl.Launch(run.Spec{Mode: "developer", Folders: []string{folder}, Prompt: "build the thing"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if id == "" {
		t.Fatal("launch returned an empty id")
	}

	var got run.Run
	for range 50 {
		got, err = cl.Status(id)
		if err == nil && got.ID == id {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || got.ID != id {
		t.Fatalf("status: id=%q err=%v", got.ID, err)
	}
	if got.Prompt != "build the thing" {
		t.Errorf("status prompt = %q, want the launched prompt", got.Prompt)
	}
	if len(got.Folders) != 1 || got.Folders[0] != folder {
		t.Errorf("run must carry its launch folders, got %v", got.Folders)
	}

	// The trigger surfaces an honest terminal failure: a non-git launch folder
	// can't be branched, so the run fails (not a silent hang) and run_status
	// reports the error end-to-end through the control path.
	var failed run.Run
	for range 100 {
		failed, _ = cl.Status(id)
		if failed.Error != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if failed.Error == "" {
		t.Error("a run launched into a non-git folder should fail honestly with an error visible via run_status")
	}
}

// The resilience handshake: ping detects a live sidecar, and ensureUp returns
// immediately (no spawn) when one is already running. (The down→spawn path
// starts the real candyland binary, so it's exercised by the live MCP, not here
// where os.Executable() is the test binary.)
func TestPingAndEnsureUpWhenRunning(t *testing.T) {
	cl, stop := sidecar(t)
	defer stop()

	if !cl.ping() {
		t.Error("ping should detect the running sidecar")
	}
	if err := cl.ensureUp(); err != nil {
		t.Errorf("ensureUp should be a no-op when the sidecar is already up, got %v", err)
	}
	// A dead address pings false (so ensureUp would proceed to start one).
	if NewClient("127.0.0.1:1").ping() {
		t.Error("ping should be false for an unreachable address")
	}
}

// run_status / stop_run on an unknown run surface the API's not-found error
// rather than reporting success — the client must not silently swallow it.
func TestControlUnknownRunErrors(t *testing.T) {
	cl, stop := sidecar(t)
	defer stop()

	if _, err := cl.Status("nope"); err == nil {
		t.Error("status of an unknown run should error")
	}
	if err := cl.Stop("nope"); err == nil {
		t.Error("stop of an unknown run should error")
	} else if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "409") {
		t.Errorf("stop error should surface the HTTP status, got %v", err)
	}
}
