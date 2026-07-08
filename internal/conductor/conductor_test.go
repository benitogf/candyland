package conductor

import (
	"os"
	"testing"

	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
	"github.com/benitogf/ooo/storage"
	"github.com/gorilla/mux"
)

// newIncidentServer builds a serverful conductor with the run/quest/campaign
// filters open, so an incident recorded on any of the three round-trips through
// storage (the mining data-access path reads the same records).
func newIncidentServer(t *testing.T) *Conductor {
	t.Helper()
	st := storage.New(storage.LayeredConfig{Memory: storage.NewMemoryLayer()})
	srv := &ooo.Server{Storage: st, Static: true, Router: mux.NewRouter(), Silence: true}
	srv.OpenFilter("runs/*")
	srv.OpenFilter("quests/*")
	srv.OpenFilter("campaigns/*")
	c := New(srv)
	if err := srv.StartWithError("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(os.Interrupt) })
	return c
}

// parseIncidentNotes collects EVERY `INCIDENT <json>` line (an agent may self-report
// several while it keeps working), ignores non-incident lines, and skips a line whose
// JSON is unparseable or that carries no summary.
func TestParseIncidentNotesCollectsAllValidLines(t *testing.T) {
	text := "starting work\n" +
		`INCIDENT {"summary":"flaky dependency retried","detail":"npm registry 503, retried once","severity":"warn"}` + "\n" +
		"some narration in between\n" +
		`INCIDENT {"summary":"repaired stale lockfile"}` + "\n" +
		`INCIDENT {not valid json}` + "\n" +
		`INCIDENT {"detail":"no summary so dropped"}` + "\n" +
		"DECISION {\"answer\":\"unrelated verdict line\"}\n"

	notes := parseIncidentNotes(text)
	if len(notes) != 2 {
		t.Fatalf("want 2 valid incidents, got %d: %+v", len(notes), notes)
	}
	if notes[0].Summary != "flaky dependency retried" || notes[0].Severity != "warn" {
		t.Errorf("first incident not parsed correctly: %+v", notes[0])
	}
	if notes[0].Detail != "npm registry 503, retried once" {
		t.Errorf("first incident detail wrong: %q", notes[0].Detail)
	}
	if notes[1].Summary != "repaired stale lockfile" {
		t.Errorf("second incident summary wrong: %q", notes[1].Summary)
	}
}

// parseIncidentNotes returns nothing for a transcript with no INCIDENT line — the
// common case, so captureIncidents is a no-op there.
func TestParseIncidentNotesEmptyWhenNoneReported(t *testing.T) {
	if notes := parseIncidentNotes("just working\nno self-report here\n"); len(notes) != 0 {
		t.Fatalf("want no incidents, got %+v", notes)
	}
}

// captureIncidents persists a run agent's self-acknowledged incidents onto the run
// record, stamps the reporting agent and a timestamp, and the record round-trips —
// retrievable via the same data-access path /learn mines.
func TestCaptureIncidentsPersistsOnRun(t *testing.T) {
	c := newIncidentServer(t)
	id := c.Create(run.Spec{Prompt: "do the thing", Folders: []string{"/repo"}})

	c.captureIncidents(id, "t1", `INCIDENT {"summary":"worked around a missing env var","severity":"warn"}`)

	got, ok := c.Get(id)
	if !ok {
		t.Fatalf("Get(%q) not found", id)
	}
	if len(got.Incidents) != 1 {
		t.Fatalf("want 1 incident recorded on the run, got %+v", got.Incidents)
	}
	n := got.Incidents[0]
	if n.Summary != "worked around a missing env var" {
		t.Errorf("summary not persisted: %q", n.Summary)
	}
	if n.Agent != "t1" {
		t.Errorf("reporting agent not stamped: %q, want t1", n.Agent)
	}
	if n.At == "" {
		t.Error("timestamp not stamped")
	}
}

// A second capture on the same run APPENDS — the incident audit trail accumulates
// rather than overwriting, mirroring how escalations accumulate.
func TestCaptureIncidentsAppends(t *testing.T) {
	c := newIncidentServer(t)
	id := c.Create(run.Spec{Prompt: "x", Folders: []string{"/repo"}})

	c.captureIncidents(id, "t1", `INCIDENT {"summary":"first"}`)
	c.captureIncidents(id, "t2", `INCIDENT {"summary":"second"}`)

	got, _ := c.Get(id)
	if len(got.Incidents) != 2 {
		t.Fatalf("want 2 accumulated incidents, got %+v", got.Incidents)
	}
	if got.Incidents[0].Summary != "first" || got.Incidents[1].Summary != "second" {
		t.Errorf("incidents not appended in order: %+v", got.Incidents)
	}
}

// captureIncidents with no INCIDENT line leaves the record untouched (the common case).
func TestCaptureIncidentsNoopWhenNoneReported(t *testing.T) {
	c := newIncidentServer(t)
	id := c.Create(run.Spec{Prompt: "x", Folders: []string{"/repo"}})

	c.captureIncidents(id, "t1", "no self-report in this transcript")

	got, _ := c.Get(id)
	if len(got.Incidents) != 0 {
		t.Fatalf("want no incidents, got %+v", got.Incidents)
	}
}

// The host-id prefix routes an incident to the right record kind, exactly as
// recordEscalation does: a quest id lands on the quest, a campaign id on the campaign.
func TestCaptureIncidentsRoutesByHostKind(t *testing.T) {
	c := newIncidentServer(t)
	qid := c.CreateQuest(run.QuestSpec{Objective: "keep lint clean", Folders: []string{"/repo"}})
	cid := c.CreateCampaign(run.CampaignSpec{Input: "ship the redesign", Folders: []string{"/repo"}})

	c.captureIncidents(qid, questLeadID, `INCIDENT {"summary":"quest-lead recovered from a transient scan failure"}`)
	c.captureIncidents(cid, intentLeadID, `INCIDENT {"summary":"campaign lead worked around a rate limit"}`)

	q, ok := c.GetQuest(qid)
	if !ok || len(q.Incidents) != 1 || q.Incidents[0].Agent != questLeadID {
		t.Fatalf("quest incident not routed/recorded: ok=%v incidents=%+v", ok, q.Incidents)
	}
	cam, ok := c.GetCampaign(cid)
	if !ok || len(cam.Incidents) != 1 || cam.Incidents[0].Agent != intentLeadID {
		t.Fatalf("campaign incident not routed/recorded: ok=%v incidents=%+v", ok, cam.Incidents)
	}
}
