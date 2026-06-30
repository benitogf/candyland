package conductor

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// CPB7: a `TEST {json}` line on an agent's stream parses into pass/fail counts
// (the same shape the UI reads from ev.pass/ev.fail); a plain text block does not.
func TestParseTestLine(t *testing.T) {
	pass, fail, ok := parseTest("running suite\nTEST {\"pass\":12,\"fail\":0}\ndone")
	if !ok || pass != 12 || fail != 0 {
		t.Errorf("expected pass=12 fail=0 ok, got pass=%d fail=%d ok=%v", pass, fail, ok)
	}
	// The last TEST line wins (a re-run after a fix supersedes the earlier result).
	pass, fail, ok = parseTest("TEST {\"pass\":1,\"fail\":3}\nTEST {\"pass\":4,\"fail\":0}")
	if !ok || pass != 4 || fail != 0 {
		t.Errorf("last TEST line should win, got pass=%d fail=%d ok=%v", pass, fail, ok)
	}
	if _, _, ok := parseTest("just some narration, no result line"); ok {
		t.Error("a plain text block must not be read as a test result")
	}
}

// CPB7: a completed run writes a queryable audit at audits/<id> derived from its
// final state, with per-task pass/fail summed from that task's agent test events.
func TestWriteAuditDerivesQueryableRecord(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	srv.OpenFilter("audits/*")
	srv.OpenFilter("runs/*")
	c := New(srv)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(os.Interrupt)

	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Update(id, func(r *run.Run) {
		r.Status = "done"
		r.Phase = run.PhasePR
		r.PrURL = "https://github.com/benitogf/candyland/pull/2"
		r.Tasks = []run.Task{{ID: "t1", State: "green"}, {ID: "t2", State: "green"}}
		// TokensUsed is derived (recompute sums agent tokens) → 2800 + 1400 = 4200.
		r.Agents = []run.Agent{
			{ID: "t1", Tokens: 2800, Events: []run.Event{
				{T: "test", Pass: 1, Fail: 2}, // a failing first run...
				{T: "test", Pass: 5, Fail: 0}, // ...superseded by the green re-run (last wins)
			}},
			{ID: "t2", Tokens: 1400, Events: []run.Event{{T: "test", Pass: 7, Fail: 0}}},
		}
	})

	c.writeAudit(id)

	obj, err := st.Get("audits/" + id)
	if err != nil {
		t.Fatalf("audit not stored/queryable at audits/%s: %v", id, err)
	}
	var a run.Audit
	if err := json.Unmarshal(obj.Data, &a); err != nil {
		t.Fatalf("audit not valid JSON: %v", err)
	}
	if a.RunID != id || a.Status != "done" || a.Phase != run.PhasePR || a.Tokens != 4200 {
		t.Errorf("audit header wrong: %+v", a)
	}
	if a.PrURL == "" {
		t.Error("audit should carry the run's PR URL")
	}
	byID := map[string]run.TaskAudit{}
	for _, ta := range a.Tasks {
		byID[ta.ID] = ta
	}
	if got := byID["t1"]; got.Pass != 5 || got.Fail != 0 || got.State != "green" {
		t.Errorf("t1 audit should take its LAST test event (pass=5 fail=0 green, the green re-run supersedes the failing first run), got %+v", got)
	}
	if got := byID["t2"]; got.Pass != 7 || got.Fail != 0 {
		t.Errorf("t2 audit wrong, got %+v", got)
	}
}

// A serverless conductor (no ooo) writes no audit and does not panic — the same
// nil-guard the publish path uses.
func TestWriteAuditNoServerIsNoop(t *testing.T) {
	c := New(nil)
	c.writeAudit("missing") // must not panic
}

// A run that failed before partitioning (no tasks) must still audit with
// tasks:[] — never tasks:null — since the UI reads the shape with no null guard.
func TestWriteAuditTaskLessRunMarshalsEmptyArray(t *testing.T) {
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	srv.OpenFilter("audits/*")
	srv.OpenFilter("runs/*")
	c := New(srv)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(os.Interrupt)

	id := c.Create(run.Spec{Prompt: "bad workspace"})
	c.Update(id, func(r *run.Run) { r.Status = "done"; r.Error = "workspace has no git repo" })
	c.writeAudit(id)

	obj, err := st.Get("audits/" + id)
	if err != nil {
		t.Fatalf("audit not stored: %v", err)
	}
	if !strings.Contains(string(obj.Data), `"tasks":[]`) {
		t.Errorf("task-less audit must serialize tasks:[] not null; got %s", obj.Data)
	}
}
