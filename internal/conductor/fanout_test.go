package conductor

import (
	"os"
	"os/exec"
	"path/filepath"
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
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
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

	// Duplicate ids (more likely once one partition spans multiple repos) are made
	// unique — a collision would otherwise silently overwrite the first task's
	// brief / bus agent / worktree / branch.
	dup := parsePartition(`PARTITION [{"id":"a","title":"X","repo":"alpha"},{"id":"a","title":"Y","repo":"beta"}]`)
	if len(dup) != 2 || dup[0].ID == dup[1].ID {
		t.Errorf("duplicate task ids must be made unique, got %q and %q", dup[0].ID, dup[1].ID)
	}
}

// argvCaptureClaude records the -p argument ($2) of every spawn to the file in
// CANDYLAND_ARGV_CAPTURE, then drives a minimal single-task delivery. It lets the
// test prove the plan (which is large) never reaches the command line — it rides
// the brief instead. A single-task PARTITION also exercises the atomic-task path.
const argvCaptureClaude = `#!/usr/bin/env bash
printf '%s\n' "$2" >> "$CANDYLAND_ARGV_CAPTURE"
prompt="$2"
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"the whole thing\",\"role\":\"fullstack\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "done" > "a.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

// The plan never rides on argv: a large prompt is delivered through the bus brief,
// so every spawned claude's -p stays a small constant bootstrap. This is the fix
// for the Windows "command line too long" failure (argv caps at ~32k).
func TestSpawnArgvCarriesNoPlanBody(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "argv.txt")
	t.Setenv("CANDYLAND_ARGV_CAPTURE", capture)
	c, _ := deliveryConductor(t, argvCaptureClaude)

	plan := strings.Repeat("PLANBODY ", 6000) // ~54k — well over the 32k argv ceiling
	id := c.Create(run.Spec{Prompt: plan})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 30*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("single-task run did not finish cleanly: status=%q error=%q", r.Status, r.Error)
	}
	if len(r.Tasks) != 1 {
		t.Fatalf("a single atomic task must be a valid partition, got %d tasks", len(r.Tasks))
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("no argv captured (claude never spawned?): %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least tl + coder spawns captured, got %d", len(lines))
	}
	for i, ln := range lines {
		if strings.Contains(ln, "PLANBODY") {
			t.Errorf("spawn %d: plan body leaked onto the claude -p arg (first 80 chars): %.80s", i, ln)
		}
		if len(ln) > 4000 {
			t.Errorf("spawn %d: -p arg is %d chars — context must ride the brief, not argv", i, len(ln))
		}
	}
}

// A multi-repo run: the tech lead partitions work across TWO repos (task a→alpha,
// task b→beta via the `repo` field). candyland delivers ONE PR PER IMPACTED REPO —
// each repo's task lands on its own run branch and opens its own PR.
const multiRepoClaude = `#!/usr/bin/env bash
prompt="$2"
brief=$(curl -s "http://$CANDYLAND_BUS_ADDR/brief/$CANDYLAND_AGENT_ID" 2>/dev/null)
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"alpha task\",\"files\":[\"a.txt\"],\"test\":\"t\",\"repo\":\"alpha\"},{\"id\":\"b\",\"title\":\"beta task\",\"files\":[\"b.txt\"],\"test\":\"t\",\"repo\":\"beta\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  file=$(printf '%s' "$brief" | sed -n 's/.*"files":\["\([^"]*\)".*/\1/p')
  [ -z "$file" ] && file="fallback_$$.txt"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"'"$file"'"}}]}}'
  echo "by $$" > "$file"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

// A stub gh that echoes a per-repo PR URL (derived from the integration worktree's
// repo dir), so two repos produce two distinct PRs.
const perRepoGh = "#!/usr/bin/env bash\nrepo=$(basename \"$(dirname \"$PWD\")\")\necho \"https://github.com/example/$repo/pull/7\"\n"

func writeGh(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_GH", gh)
}

func TestMultiRepoOnePRPerImpactedRepo(t *testing.T) {
	c, repos := multiRepoConductor(t, multiRepoClaude, "alpha", "beta")
	writeGh(t, perRepoGh)
	id := c.Create(run.Spec{Prompt: "ship across two repos"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("multi-repo run did not finish cleanly: status=%q error=%q", r.Status, r.Error)
	}
	if len(r.PRs) != 2 {
		t.Fatalf("a 2-repo feature must open one PR per impacted repo, got %d: %+v", len(r.PRs), r.PRs)
	}
	byRepo := map[string]run.PR{}
	for _, pr := range r.PRs {
		byRepo[pr.Repo] = pr
	}
	for _, name := range []string{"alpha", "beta"} {
		pr, ok := byRepo[name]
		if !ok || pr.URL == "" || pr.Err != "" {
			t.Errorf("repo %q did not get an opened PR: %+v", name, pr)
		}
	}
	if byRepo["alpha"].URL == byRepo["beta"].URL {
		t.Errorf("the two repos must open distinct PRs, both = %q", byRepo["alpha"].URL)
	}
	// Each repo's task file landed on its own run branch (pushed to origin).
	for _, want := range []struct{ repo, file string }{{repos[0], "a.txt"}, {repos[1], "b.txt"}} {
		out, err := exec.Command("git", "-C", want.repo, "show", r.Branch+":"+want.file).CombinedOutput()
		if err != nil {
			t.Errorf("%s missing on %s's run branch: %v\n%s", want.file, filepath.Base(want.repo), err, out)
		}
	}
}

// Partial-failure isolation: when ONE repo's PR can't open, the OTHER still ships
// and the failure is surfaced — never a falsely-green run, never aborting the rest.
func TestMultiRepoPartialFailureIsolation(t *testing.T) {
	c, _ := multiRepoConductor(t, multiRepoClaude, "alpha", "beta")
	// gh fails only for beta (its integration worktree path contains /beta/).
	writeGh(t, "#!/usr/bin/env bash\nif [[ \"$PWD\" == *\"/beta/\"* ]]; then echo 'gh: beta not authenticated' >&2; exit 1; fi\necho 'https://github.com/example/alpha/pull/7'\n")
	id := c.Create(run.Spec{Prompt: "ship across two repos"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" {
		t.Fatalf("run did not finish: status=%q error=%q", r.Status, r.Error)
	}
	if r.Error != "" {
		t.Fatalf("one repo failing must NOT fail the whole run (the other shipped): %q", r.Error)
	}
	if len(r.PRs) != 2 {
		t.Fatalf("expected a PR record per repo, got %d: %+v", len(r.PRs), r.PRs)
	}
	byRepo := map[string]run.PR{}
	for _, pr := range r.PRs {
		byRepo[pr.Repo] = pr
	}
	if byRepo["alpha"].URL == "" {
		t.Errorf("alpha should have shipped despite beta failing: %+v", byRepo["alpha"])
	}
	if byRepo["beta"].Err == "" || byRepo["beta"].URL != "" {
		t.Errorf("beta's failure must be surfaced (Err set, no URL): %+v", byRepo["beta"])
	}
	if r.PrURL != byRepo["alpha"].URL {
		t.Errorf("PrURL should mirror the first opened PR (alpha), got %q", r.PrURL)
	}
}

func TestClaudeFanOut(t *testing.T) {
	c, repo := deliveryConductor(t, fanOutClaude)
	id := c.Create(run.Spec{Prompt: "add a CSV export"})
	c.Begin(id)

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
	// agents = tech-lead + one coder per task + the reviewer (review phase).
	if len(r.Agents) != 4 {
		t.Fatalf("fan-out wrong: got %d agents, want 4 (tl + 2 coders + reviewer)", len(r.Agents))
	}
	hasReviewer := false
	for _, a := range r.Agents {
		if a.ID == reviewerID {
			hasReviewer = true
		}
	}
	if !hasReviewer {
		t.Errorf("the review phase must spawn a separate reviewer agent (%q), agents: %+v", reviewerID, r.Agents)
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
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"git merge conflict"* ]]; then
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
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

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
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

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
// and the re-plan (the tech lead's brief now carries "feedback") produces a
// file-disjoint split that integrates cleanly and ships a PR.
const replanRecoverClaude = `#!/usr/bin/env bash
prompt="$2"
brief=$(curl -s "http://$CANDYLAND_BUS_ADDR/brief/$CANDYLAND_AGENT_ID" 2>/dev/null)
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"git merge conflict"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"I cannot reconcile this."}]}}'
  echo '{"type":"result","subtype":"success","result":"x","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  if [[ "$brief" == *'"feedback":'* ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"t\"},{\"id\":\"b\",\"title\":\"task b\",\"files\":[\"b.txt\"],\"test\":\"t\"}]"}]}}'
  else
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"shared.txt\"],\"test\":\"t\"},{\"id\":\"b\",\"title\":\"task b\",\"files\":[\"shared.txt\"],\"test\":\"t\"}]"}]}}'
  fi
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  file=$(printf '%s' "$brief" | sed -n 's/.*"files":\["\([^"]*\)".*/\1/p')
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
brief=$(curl -s "http://$CANDYLAND_BUS_ADDR/brief/$CANDYLAND_AGENT_ID" 2>/dev/null)
if [[ "$prompt" == *"code reviewer"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"git merge conflict"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"resolve"}]}}'
  echo '{"type":"result","subtype":"success","result":"x","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  if [[ "$brief" == *'"feedback":'* ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"simple task\",\"files\":[\"x.txt\"],\"test\":\"t\"}]"}]}}'
  else
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"impossible task\",\"files\":[\"x.txt\"],\"test\":\"t\"}]"}]}}'
  fi
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  if [[ "$brief" == *"impossible"* ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"I will not act on this."}]}}'
    echo '{"type":"result","subtype":"success","result":"no-op","usage":{"output_tokens":1}}'
  else
    file=$(printf '%s' "$brief" | sed -n 's/.*"files":\["\([^"]*\)".*/\1/p')
    [ -z "$file" ] && file="fallback_$$.txt"
    echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"'"$file"'"}}]}}'
    echo "done" > "$file"
    echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
  fi
fi
`

func TestCoderFailureTriggersReplan(t *testing.T) {
	c, repo := deliveryConductor(t, coderFailReplanClaude)
	t.Setenv("CANDYLAND_AGENT_ATTEMPTS", "1") // the impossible coder fails in one attempt
	t.Setenv("CANDYLAND_REPLAN_ATTEMPTS", "3")
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

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
	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)

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
