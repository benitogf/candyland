package conductor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// TestFullOutputPersistedUntruncated is the defining check for the "persist and
// serve complete untruncated agent output" slice: a tool_use with a large input
// and a result with a large payload must record BOTH a compact live summary
// (Input/Text, still truncated for the dashboard) AND the complete untruncated
// payload (InputFull/TextFull), and the full content must survive JSON
// serialization — the exact bytes the run snapshot/trace API serves.
func TestFullOutputPersistedUntruncated(t *testing.T) {
	c := New(nil) // serverless: state is held in the runtime and read via Get
	id := c.Create(run.Spec{Prompt: "x", Folders: []string{"/tmp"}})

	// Payloads well beyond the 200/300-char summary caps.
	bigInput := `{"file":"` + strings.Repeat("A", 4000) + `"}`
	bigResult := strings.Repeat("R", 5000)

	mapAgentLine(c, id, "coder", streamLine{
		Type: "assistant",
		Message: struct {
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		}{Content: []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{{Type: "tool_use", Name: "Edit", Input: json.RawMessage(bigInput)}}},
	})
	mapAgentLine(c, id, "coder", streamLine{Type: "result", Result: bigResult})

	r, ok := c.Get(id)
	if !ok {
		t.Fatal("run lost")
	}
	a := findAgent(r.Agents, "coder")
	if a == nil || len(a.Events) != 2 {
		t.Fatalf("expected 2 recorded events on the coder, got %+v", a)
	}
	tool, result := a.Events[0], a.Events[1]

	// The compact summary stays truncated for the live dashboard...
	if !strings.HasSuffix(tool.Input, "…") || len([]rune(tool.Input)) > 201 {
		t.Errorf("tool Input should be a truncated summary, got %d runes", len([]rune(tool.Input)))
	}
	if !strings.HasSuffix(result.Text, "…") || len([]rune(result.Text)) > 301 {
		t.Errorf("result Text should be a truncated summary, got %d runes", len([]rune(result.Text)))
	}
	// ...but the full untruncated payload is persisted alongside it.
	if tool.InputFull != bigInput {
		t.Errorf("tool InputFull was truncated: got %d bytes, want %d", len(tool.InputFull), len(bigInput))
	}
	if result.TextFull != bigResult {
		t.Errorf("result TextFull was truncated: got %d bytes, want %d", len(result.TextFull), len(bigResult))
	}

	// The API serves the stored Run verbatim (json.Marshal is exactly what
	// publish/the snapshot endpoint write) — the full payload must round-trip.
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal run: %v", err)
	}
	// Assert on the escape-free repeated segments (JSON escapes the quotes in the
	// input, so the raw bigInput substring wouldn't appear literally).
	if !strings.Contains(string(b), strings.Repeat("A", 4000)) {
		t.Error("serialized run (as served by the API) is missing the full tool input")
	}
	if !strings.Contains(string(b), bigResult) {
		t.Error("serialized run (as served by the API) is missing the full result output")
	}

	var back run.Run
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal run: %v", err)
	}
	ba := findAgent(back.Agents, "coder")
	if ba.Events[0].InputFull != bigInput || ba.Events[1].TextFull != bigResult {
		t.Error("full output did not survive the API JSON round-trip")
	}
}
