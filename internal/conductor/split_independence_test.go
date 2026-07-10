package conductor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/candyland/internal/run"
)

// A tech lead whose FIRST partition declares overlapping files (both tasks own
// shared.txt) and whose re-plan (brief carries "feedback") is file-disjoint.
// Every coder spawn appends to CANDYLAND_CODER_SPAWN_LOG, so the test can prove
// no coder ran on the rejected attempt.
const overlapPartitionClaude = `#!/usr/bin/env bash
prompt="$2"
brief=$(curl -s "http://$CANDYLAND_BUS_ADDR/brief/$CANDYLAND_AGENT_ID" 2>/dev/null)
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  if [[ "$brief" == *'"feedback":'* ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"t\"},{\"id\":\"b\",\"title\":\"task b\",\"files\":[\"b.txt\"],\"test\":\"t\"}]"}]}}'
  else
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"shared.txt\"],\"test\":\"t\"},{\"id\":\"b\",\"title\":\"task b\",\"files\":[\"shared.txt\"],\"test\":\"t\"}]"}]}}'
  fi
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo "spawned" >> "$CANDYLAND_CODER_SPAWN_LOG"
  file=$(printf '%s' "$brief" | sed -n 's/.*"files":\["\([^"]*\)".*/\1/p')
  [ -z "$file" ] && file="fallback_$$.txt"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"'"$file"'"}}]}}'
  echo "content by $$" > "$file"
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"TEST {\"pass\":1,\"fail\":0}"}]}}'
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

// A partition whose declared Files overlap is rejected statically BEFORE any
// coder spawns: the re-plan feedback names the overlapping file and the
// offending task ids, and only the second (disjoint) attempt runs coders — one
// cheap tech-lead round instead of N burned coder budgets.
func TestOverlappingPartitionRejectedBeforeSpawn(t *testing.T) {
	spawnLog := filepath.Join(t.TempDir(), "coder-spawns.txt")
	t.Setenv("CANDYLAND_CODER_SPAWN_LOG", spawnLog)
	c, _ := deliveryConductor(t, overlapPartitionClaude)
	t.Setenv("CANDYLAND_REPLAN_ATTEMPTS", "3")
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("re-plan should have recovered the run: status=%q error=%q", r.Status, r.Error)
	}

	// The tech lead re-planned, and the feedback named the overlap precisely.
	var feedback string
	for _, a := range r.Agents {
		if a.ID != "tl" {
			continue
		}
		for _, e := range a.Events {
			if strings.Contains(e.Text, "re-planning") {
				feedback = e.Text
			}
		}
	}
	if feedback == "" {
		t.Fatal("the overlapping partition must trigger a re-plan")
	}
	for _, want := range []string{"shared.txt", "a", "b"} {
		if !strings.Contains(feedback, want) {
			t.Errorf("re-plan feedback must name %q, got %q", want, feedback)
		}
	}

	// Zero coders spawned on the rejected attempt: only the 2 disjoint tasks ran.
	data, err := os.ReadFile(spawnLog)
	if err != nil {
		t.Fatalf("the accepted attempt's coders never spawned: %v", err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 2 {
		t.Errorf("expected exactly 2 coder spawns (none on the rejected attempt), got %d", got)
	}
}

// A single-task partition is a valid answer (accepted, one coder proceeds), and
// the coder's brief carries the run's FULL prompt for context — not just the
// task fields.
func TestSingleTaskPartitionAcceptedAndBriefCarriesPrompt(t *testing.T) {
	script := stubClaude(
		roleCleanReviewer,
		role("tech lead", emitPartition(`[{"id":"a","title":"the whole thing","files":["a.txt"],"test":"t"}]`)),
		coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
	)
	c, _ := deliveryConductor(t, script)
	const prompt = "add csv export end to end"
	id := c.Create(run.Spec{Prompt: prompt})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 30*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("single-task run did not finish cleanly: status=%q error=%q", r.Status, r.Error)
	}
	if len(r.Tasks) != 1 {
		t.Fatalf("a single atomic task must be a valid partition, got %d tasks", len(r.Tasks))
	}

	obj, err := c.server.Storage.Get(bus.BriefKey("a"))
	if err != nil {
		t.Fatalf("read coder brief: %v", err)
	}
	var br bus.Brief
	if json.Unmarshal(obj.Data, &br) != nil {
		t.Fatal("unmarshal coder brief")
	}
	if br.Prompt != prompt {
		t.Errorf("the coder brief must carry the run's full prompt, got %q", br.Prompt)
	}
}

// Two conductors in the same process must not share a worktree root: run ids
// reset per-conductor, so the SAME id on both must map to distinct paths (the
// r131 flake was tests colliding on os.TempDir()/candyland-wt/<runID>).
func TestConductorWorktreeRootsDistinct(t *testing.T) {
	c1, c2 := New(nil), New(nil)
	r1, r2 := c1.worktreeRoot("r1"), c2.worktreeRoot("r1")
	if r1 == r2 {
		t.Fatalf("two conductors share the worktree root for the same run id: %q", r1)
	}
	if !strings.HasPrefix(r1, filepath.Join(os.TempDir(), "candyland-wt")+string(filepath.Separator)) {
		t.Errorf("default worktree root must live under os.TempDir()/candyland-wt, got %q", r1)
	}
}
