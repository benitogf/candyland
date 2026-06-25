package conductor

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// A fake `claude` that emits a two-task PARTITION for the tech-lead and, for each
// coder, writes a real file in its worktree. Drives the full delivery: partition
// → two parallel coder worktrees → integrate (merge) → push → PR.
const fanOutClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"system","subtype":"init","session_id":"s1"}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"role\":\"Backend\",\"emoji\":\"X\",\"files\":[\"a.txt\"],\"test\":\"a_test\"},{\"id\":\"b\",\"title\":\"task b\",\"role\":\"Frontend\",\"emoji\":\"Y\",\"files\":[\"b.txt\"],\"test\":\"b_test\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"partition emitted","usage":{"output_tokens":1000}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"implementing"}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"f"}}]}}'
  echo "work by $$" > "candyland_$$.txt"
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"TEST {\"pass\":2,\"fail\":0}"}]}}'
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2000}}'
fi
`

// Task ids from the tech-lead's PARTITION line become worktree path components
// and git branch refs, and their deps are matched against task ids by the bus
// auto-unblock. parsePartition must normalize all of them through slug so a
// malformed id can't escape the worktree root or break ref creation — while
// leaving realistic ids untouched and keeping deps consistent with the ids they
// reference (else dependent tasks would never unblock).
func TestParsePartitionSlugsIDsAndDeps(t *testing.T) {
	// Realistic ids pass through unchanged — normal partitions behave identically.
	clean := parsePartition(`PARTITION [{"id":"a","title":"A"},{"id":"backend","title":"B","deps":["a"]}]`)
	if len(clean) != 2 || clean[0].ID != "a" || clean[1].ID != "backend" || len(clean[1].Deps) != 1 || clean[1].Deps[0] != "a" {
		t.Fatalf("realistic ids must be unchanged, got %+v", clean)
	}

	// A malformed id (path traversal) and a dep referencing it are slugged the
	// SAME way, so the dependency still matches the (now slugged) task id.
	got := parsePartition(`PARTITION [{"id":"../escape","title":"X"},{"id":"task B","title":"Y","deps":["../escape"]}]`)
	if len(got) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(got))
	}
	if strings.ContainsAny(got[0].ID, "/.") {
		t.Errorf("traversal id not sanitized: %q", got[0].ID)
	}
	if got[1].ID != "task-b" {
		t.Errorf("id not slugged: %q", got[1].ID)
	}
	if got[1].Deps[0] != got[0].ID {
		t.Errorf("dep %q must match the slugged task id %q so unblock still works", got[1].Deps[0], got[0].ID)
	}
}

func TestClaudeFanOut(t *testing.T) {
	c, repo := deliveryConductor(t, fanOutClaude)
	id := c.Create(run.Spec{Mode: "developer", Prompt: "add a CSV export"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 30*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error != "" {
		t.Fatalf("run errored: %q", r.Error)
	}
	if len(r.Tasks) != 2 {
		t.Fatalf("partition not parsed: got %d tasks, want 2", len(r.Tasks))
	}
	// agents = tech-lead + one coder per task.
	if len(r.Agents) != 3 {
		t.Fatalf("fan-out wrong: got %d agents, want 3 (tl + 2 coders)", len(r.Agents))
	}
	byID := map[string]run.Agent{}
	for _, a := range r.Agents {
		byID[a.ID] = a
	}
	for _, aid := range []string{"tl", "a", "b"} {
		a, ok := byID[aid]
		if !ok {
			t.Fatalf("missing agent %q", aid)
		}
		if len(a.Events) == 0 {
			t.Errorf("agent %q has no events (process output not aggregated)", aid)
		}
	}
	if byID["a"].State != "green" || byID["b"].State != "green" {
		t.Errorf("coders not marked green: a=%s b=%s", byID["a"].State, byID["b"].State)
	}
	if r.TasksGreen != 2 {
		t.Errorf("tasksGreen=%d want 2", r.TasksGreen)
	}
	// The coder's `TEST {json}` line must flow through parseTest → the agent's test
	// event (the real producer path the audit's per-task pass/fail is derived from —
	// not just the unit fixture). Each coder emitted TEST {"pass":2,"fail":0}.
	for _, aid := range []string{"a", "b"} {
		var pass, fail, n int
		for _, ev := range byID[aid].Events {
			if ev.T == "test" {
				n++
				pass += ev.Pass
				fail += ev.Fail
			}
		}
		if n == 0 {
			t.Errorf("coder %q emitted no test event — the TEST-line producer path is dead", aid)
		}
		if pass != 2 || fail != 0 {
			t.Errorf("coder %q test counts = %d pass/%d fail, want 2/0", aid, pass, fail)
		}
	}
	// A clean run opens a real PR (the stub gh returns the URL).
	if r.PrURL == "" {
		t.Error("a completed run must set PrURL")
	}
	// The real git flow pushed the run branch to origin with both coders' commits.
	out, err := exec.Command("git", "-C", repo, "ls-remote", "--heads", "origin", r.Branch).CombinedOutput()
	if err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "refs/heads/"+r.Branch) {
		t.Errorf("run branch %q was not pushed to origin; ls-remote: %q", r.Branch, out)
	}
}

// A partition whose tasks are NOT file-disjoint: both coders write the same file,
// so their branches conflict at integration. A real tech lead reconciles the
// conflict (here the integrator — prompt contains "conflict" — rewrites the file
// cleanly) and the run completes with a PR, rather than giving up at the first
// overlap. The reconciled content must land (no leftover conflict markers).
const conflictResolvedClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"git merge conflict"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file":"shared.txt"}}]}}'
  printf 'reconciled: a + b\n' > "shared.txt"
  echo '{"type":"result","subtype":"success","result":"resolved","usage":{"output_tokens":3}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"shared.txt\"],\"test\":\"a_test\"},{\"id\":\"b\",\"title\":\"task b\",\"files\":[\"shared.txt\"],\"test\":\"b_test\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"shared.txt"}}]}}'
  echo "content by $$" > "shared.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

func TestIntegrationConflictResolved(t *testing.T) {
	c, repo := deliveryConductor(t, conflictResolvedClaude)
	id := c.Create(run.Spec{Mode: "developer", Prompt: "do the thing"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 30*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error != "" {
		t.Fatalf("the tech lead should have reconciled the conflict, but the run errored: %q", r.Error)
	}
	if r.PrURL == "" {
		t.Error("a resolved run must open a PR")
	}
	// The reconciled content must actually be committed on the run branch — no
	// leftover conflict markers, the integrator's merged version present.
	out, err := exec.Command("git", "-C", repo, "show", r.Branch+":shared.txt").CombinedOutput()
	if err != nil {
		t.Fatalf("reading shared.txt on the run branch: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "<<<<<<<") || strings.Contains(string(out), ">>>>>>>") {
		t.Errorf("conflict markers survived into the run branch:\n%s", out)
	}
	if !strings.Contains(string(out), "reconciled") {
		t.Errorf("the integrator's reconciled content is missing on the run branch:\n%s", out)
	}
}

// When the tech lead's split conflicts and the integrator CAN'T reconcile it (here
// it only talks, never edits), the tech lead REASSESSES and re-partitions. With a
// stub whose split never improves, the run exhausts its re-plan budget and then
// fails honestly on a clean (aborted) tree — never a silent green or a PR on a
// half-merged tree. (It must still have actually re-planned, not failed on the
// first conflict.)
const conflictUnresolvableClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"git merge conflict"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"I cannot reconcile these."}]}}'
  echo '{"type":"result","subtype":"success","result":"gave up","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"shared.txt\"],\"test\":\"a_test\"},{\"id\":\"b\",\"title\":\"task b\",\"files\":[\"shared.txt\"],\"test\":\"b_test\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"shared.txt"}}]}}'
  echo "content by $$" > "shared.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

func TestUnresolvableConflictFailsHonestly(t *testing.T) {
	c, _ := deliveryConductor(t, conflictUnresolvableClaude)
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "1")  // one resolution attempt per integrate
	t.Setenv("CANDYLAND_REPLAN_ATTEMPTS", "2") // reassess once, then give an honest failure
	id := c.Create(run.Spec{Mode: "developer", Prompt: "do the thing"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q", r.Status)
	}
	if r.Error == "" {
		t.Fatal("an unresolvable conflict must record an honest error, not finish clean")
	}
	if !strings.Contains(strings.ToLower(r.Error), "conflict") && !strings.Contains(strings.ToLower(r.Error), "split") {
		t.Errorf("error should name the unresolved conflict / failed split, got %q", r.Error)
	}
	// It must have REASSESSED before giving up — not failed on the first conflict.
	replanned := false
	for _, a := range r.Agents {
		if a.ID != "tl" {
			continue
		}
		for _, e := range a.Events {
			if strings.Contains(e.Text, "re-planning") {
				replanned = true
			}
		}
	}
	if !replanned {
		t.Error("the tech lead should have re-planned before failing, not given up on the first conflict")
	}
	// The defining safety property: no false success.
	if r.PrURL != "" {
		t.Errorf("an unresolved run must not open a PR, got %q", r.PrURL)
	}
	if r.Phase == len(run.Phases)-1 || r.Progress >= 1 {
		t.Errorf("an unresolved run must not claim completion: phase=%d progress=%v", r.Phase, r.Progress)
	}
}

// The behavior the user asked for: a conflict from the tech lead's OWN split must
// not kill the run — it reassesses and tries a different breakdown. Here the first
// partition puts both coders on the same file (conflict the integrator can't fix),
// and the re-plan (prompt now carries "PREVIOUS ATTEMPT FAILED") produces a
// file-disjoint split that integrates cleanly and ships a PR.
const replanRecoverClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"git merge conflict"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"I cannot reconcile this."}]}}'
  echo '{"type":"result","subtype":"success","result":"x","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  if [[ "$prompt" == *"PREVIOUS ATTEMPT FAILED"* ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"t\"},{\"id\":\"b\",\"title\":\"task b\",\"files\":[\"b.txt\"],\"test\":\"t\"}]"}]}}'
  else
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"shared.txt\"],\"test\":\"t\"},{\"id\":\"b\",\"title\":\"task b\",\"files\":[\"shared.txt\"],\"test\":\"t\"}]"}]}}'
  fi
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  file=$(printf '%s' "$prompt" | sed -n 's/.*Files: \([A-Za-z0-9_]*\.[A-Za-z0-9_]*\).*/\1/p' | head -1)
  [ -z "$file" ] && file="fallback_$$.txt"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"'"$file"'"}}]}}'
  echo "content by $$" > "$file"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

// A coder that can't finish its task is also the tech lead's delegation failing —
// the run reassesses (re-splits into a doable task) rather than dying. Here the
// first partition hands the coder an "impossible" task it refuses to act on; the
// re-plan hands it a "simple" one it completes. (This also confirms a normal coder
// content-failure re-plans — only a claude START failure stays terminal.)
const coderFailReplanClaude = `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"git merge conflict"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"resolve"}]}}'
  echo '{"type":"result","subtype":"success","result":"x","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  if [[ "$prompt" == *"PREVIOUS ATTEMPT FAILED"* ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"simple task\",\"files\":[\"x.txt\"],\"test\":\"t\"}]"}]}}'
  else
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"impossible task\",\"files\":[\"x.txt\"],\"test\":\"t\"}]"}]}}'
  fi
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"impossible"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"I will not act on this."}]}}'
  echo '{"type":"result","subtype":"success","result":"no-op","usage":{"output_tokens":1}}'
else
  file=$(printf '%s' "$prompt" | sed -n 's/.*Files: \([A-Za-z0-9_]*\.[A-Za-z0-9_]*\).*/\1/p' | head -1)
  [ -z "$file" ] && file="fallback_$$.txt"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"'"$file"'"}}]}}'
  echo "done" > "$file"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

func TestCoderFailureTriggersReplan(t *testing.T) {
	c, repo := deliveryConductor(t, coderFailReplanClaude)
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "1") // the impossible coder fails in one attempt
	t.Setenv("CANDYLAND_REPLAN_ATTEMPTS", "3")
	id := c.Create(run.Spec{Mode: "developer", Prompt: "do the thing"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error != "" {
		t.Fatalf("a coder failure should have re-planned and recovered, but the run errored: %q", r.Error)
	}
	if r.PrURL == "" {
		t.Error("the recovered run must open a PR")
	}
	out, err := exec.Command("git", "-C", repo, "show", r.Branch+":x.txt").CombinedOutput()
	if err != nil {
		t.Errorf("the re-planned task didn't land x.txt: %v\n%s", err, out)
	}
}

func TestReplanRecoversFromBadSplit(t *testing.T) {
	c, repo := deliveryConductor(t, replanRecoverClaude)
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "1")
	t.Setenv("CANDYLAND_REPLAN_ATTEMPTS", "3")
	id := c.Create(run.Spec{Mode: "developer", Prompt: "do the thing"})
	c.Begin(id, nil)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error != "" {
		t.Fatalf("re-planning should have recovered the run, but it errored: %q", r.Error)
	}
	if r.PrURL == "" {
		t.Error("the recovered run must open a PR")
	}
	// The reassessed, file-disjoint split landed BOTH files on the run branch.
	for _, f := range []string{"a.txt", "b.txt"} {
		out, err := exec.Command("git", "-C", repo, "show", r.Branch+":"+f).CombinedOutput()
		if err != nil {
			t.Errorf("file %s missing on the run branch (re-plan didn't land it): %v\n%s", f, err, out)
		}
	}
	// It genuinely re-planned (the tech lead recorded a reassessment step).
	replanned := false
	for _, a := range r.Agents {
		if a.ID != "tl" {
			continue
		}
		for _, e := range a.Events {
			if strings.Contains(e.Text, "re-planning") {
				replanned = true
			}
		}
	}
	if !replanned {
		t.Error("expected the tech lead to record a re-planning step")
	}
}
