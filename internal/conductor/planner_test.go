package conductor

import (
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// The planner branch of a stub claude: when asked to plan (the prompt names
// "clarifying questions"), it echoes the run's actual subject back inside a
// generated question — proving the questions are derived from the prompt, not
// canned. `--output-format json` wraps the model text in a {"result": ...} object.
const plannerClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"clarifying questions"* ]]; then
  echo '{"type":"result","result":"Here you go: [{\"id\":\"scope\",\"question\":\"For the CSV export, all rows or the filtered view?\",\"options\":[\"All\",\"Filtered\"]}]"}'
else
  echo '{"type":"result","result":"ok"}'
fi
`

func TestGenerateQuestionsFromPrompt(t *testing.T) {
	writeFakeClaude(t, plannerClaude)
	c := New(nil)
	id := c.Create(run.Spec{Mode: "non-developer", Prompt: "add a CSV export to the reports page"})

	qs := c.GenerateQuestions(id)
	if len(qs) != 1 {
		t.Fatalf("want 1 generated question, got %d: %+v", len(qs), qs)
	}
	if qs[0].ID != "scope" || len(qs[0].Options) != 2 {
		t.Errorf("question not parsed from the model output: %+v", qs[0])
	}
	// It must reflect the prompt, not a fixed template.
	if qs[0].Question == "" {
		t.Error("empty question text")
	}
}

// Editing a finished run resets it to planning and regenerates the questions from
// the NEW prompt (the cached set is invalidated). It refuses a live run.
func TestEditResetsAndRegeneratesQuestions(t *testing.T) {
	writeFakeClaude(t, plannerClaude)
	c := New(nil)
	id := c.Create(run.Spec{Mode: "non-developer", Prompt: "first prompt"})

	// Generate + cache questions for the original prompt.
	if len(c.GenerateQuestions(id)) != 1 {
		t.Fatal("expected a generated question for the original prompt")
	}
	// Pretend the run finished, then edit it.
	c.Update(id, func(r *run.Run) { r.Status = "done"; r.Error = "boom" })

	if !c.Edit(id, run.Spec{Mode: "developer", Prompt: "a totally different request"}) {
		t.Fatal("Edit should succeed for a finished run")
	}
	r, _ := c.Get(id)
	if r.Status != "planning" {
		t.Errorf("edit should reset status to planning, got %q", r.Status)
	}
	if r.Prompt != "a totally different request" || r.Mode != "developer" {
		t.Errorf("edit did not apply the new task: %+v", r)
	}
	if r.Error != "" {
		t.Errorf("edit should clear the prior error, got %q", r.Error)
	}
	// The reset must use empty (non-nil) slices: nil marshals to JSON null and
	// crashes the UI's .map/.length. Check the runtime's own run (what publish
	// marshals) — Get/cloneRun would mask a nil by rebuilding it.
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	rt.mu.Lock()
	nilAgents, nilTasks := rt.r.Agents == nil, rt.r.Tasks == nil
	rt.mu.Unlock()
	if nilAgents || nilTasks {
		t.Errorf("edit must reset Agents/Tasks to [] (non-nil), got agents-nil=%v tasks-nil=%v", nilAgents, nilTasks)
	}
	// Questions regenerate from the new prompt (the cache was invalidated).
	if len(c.GenerateQuestions(id)) != 1 {
		t.Error("questions should regenerate after an edit (cache invalidated)")
	}

	// A stopped (paused) run CAN be edited — its parked executor is terminated
	// first, then it re-plans (the "edit a stopped run" case).
	c.Update(id, func(r *run.Run) { r.Status = "paused" })
	if !c.Edit(id, run.Spec{Mode: "developer", Prompt: "edit a stopped run"}) {
		t.Error("Edit should be allowed for a paused (stopped) run")
	}
	if r, _ := c.Get(id); r.Status != "planning" {
		t.Errorf("editing a paused run should reset to planning, got %q", r.Status)
	}

	// An actively running build must be stopped first.
	c.Update(id, func(r *run.Run) { r.Status = "running" })
	if c.Edit(id, run.Spec{Prompt: "x"}) {
		t.Error("Edit must refuse an actively running run")
	}
}

// A run with no prompt, or a claude that returns junk, yields no questions — the
// planning step then goes straight to the build (never a canned fallback).
func TestGenerateQuestionsEmptyOnFailure(t *testing.T) {
	writeFakeClaude(t, "#!/usr/bin/env bash\necho 'not json at all'\n")
	c := New(nil)
	id := c.Create(run.Spec{Mode: "developer", Prompt: "do a thing"})
	if qs := c.GenerateQuestions(id); len(qs) != 0 {
		t.Errorf("unparseable claude output must yield no questions, got %+v", qs)
	}

	// Unknown run → nil.
	if qs := c.GenerateQuestions("nope"); qs != nil {
		t.Errorf("unknown run should yield nil, got %+v", qs)
	}
}
