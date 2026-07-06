package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// === Session-fork spawn plumbing =============================================
//
// A later phase makes spawns fork a doctrine-loaded template session via
// `--resume <template-session-id> --fork-session`. These tests pin the plumbing
// that enables it: the fork args on the argv, the session-id + raw-usage capture
// from the stream, and the one-shot in-attempt fallback when the template
// session doesn't resolve.

// The kill switch: with no forkFrom the argv is byte-for-byte TODAY'S argv —
// pinned as a literal so any accidental new flag on the cold path fails here.
func TestClaudeArgsColdPathUnchanged(t *testing.T) {
	got := claudeArgs("do the work", []string{"/extra"}, "/bus.json",
		spawnOpts{maxTurns: 4, model: "claude-sonnet-5", thinking: "high"})
	want := []string{
		"-p", "do the work", "--output-format", "stream-json", "--verbose",
		"--model", "claude-sonnet-5", "--dangerously-skip-permissions",
		"--effort", "high", "--add-dir", "/extra", "--max-turns", "4",
		"--mcp-config", "/bus.json",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("cold-path argv changed:\n got %v\nwant %v", got, want)
	}
	// A fallbackPrompt alone (no forkFrom) must not perturb the argv either.
	if with := claudeArgs("do the work", []string{"/extra"}, "/bus.json",
		spawnOpts{maxTurns: 4, model: "claude-sonnet-5", thinking: "high", fallbackPrompt: "full bootstrap"}); !slices.Equal(with, want) {
		t.Fatalf("fallbackPrompt leaked onto the argv: %v", with)
	}
}

// The fork args: forkFrom emits `--resume <id> --fork-session` as an atomic
// pair — a bare --resume would CONTINUE the template session in place and
// corrupt it for every later spawn, so the invariant is checked on every case.
func TestClaudeArgsForkArgs(t *testing.T) {
	cases := []struct {
		name string
		o    spawnOpts
		fork bool
	}{
		{"zero opts", spawnOpts{}, false},
		{"fork only", spawnOpts{forkFrom: "sess-tpl"}, true},
		{"fork with fallback", spawnOpts{forkFrom: "sess-tpl", fallbackPrompt: "full bootstrap"}, true},
		{"fork with full opts", spawnOpts{maxTurns: 3, model: "m", thinking: "low", forkFrom: "sess-tpl"}, true},
		{"fallback without fork", spawnOpts{fallbackPrompt: "full bootstrap"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := claudeArgs("p", []string{"/extra"}, "/bus.json", tc.o)
			i := slices.Index(got, "--resume")
			if !tc.fork {
				if i >= 0 || slices.Contains(got, "--fork-session") {
					t.Fatalf("no fork requested but fork args present: %v", got)
				}
				return
			}
			if i < 0 || i+2 >= len(got) || got[i+1] != tc.o.forkFrom || got[i+2] != "--fork-session" {
				t.Fatalf("fork args must be `--resume %s --fork-session`, argv was %v", tc.o.forkFrom, got)
			}
		})
	}
}

// forkUnresolved classifies WHY a fork spawn failed: only a start failure or a
// missing-conversation exit means the template didn't resolve (retry cold); any
// other failure is an ordinary run failure the resilience retries own.
func TestForkUnresolvedDetection(t *testing.T) {
	cases := []struct {
		name string
		out  attemptOutcome
		want bool
	}{
		{"clean run", attemptOutcome{}, false},
		{"start failure", attemptOutcome{startErr: errors.New("exec: not found")}, true},
		{"missing conversation", attemptOutcome{runErr: errors.New("exit status 1"), stderr: "No conversation found with session ID: sess-tpl"}, true},
		{"ordinary crash", attemptOutcome{runErr: errors.New("exit status 1"), stderr: "some other crash"}, false},
		{"stall", attemptOutcome{stalled: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := forkUnresolved(tc.out); got != tc.want {
				t.Fatalf("forkUnresolved(%+v) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

// A stub whose result line carries the full usage block. output_tokens is 4000
// so the /1000 display scaling stays observable next to the raw counts.
const sessionUsageClaude = `#!/usr/bin/env bash
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"warming"}]}}'
echo '{"type":"system","subtype":"init","session_id":"sess-first"}'
echo '{"type":"assistant","session_id":"sess-later","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}'
echo '{"type":"result","subtype":"success","session_id":"sess-later","result":"done","usage":{"output_tokens":4000,"input_tokens":12,"cache_creation_input_tokens":34,"cache_read_input_tokens":56}}'
`

// streamOnce must accumulate the raw (unscaled) usage counts, while the existing
// /1000 tokens field is untouched; the same raw counts must land on the agent
// record for the dashboard/learn.
func TestStreamOnceCapturesRawUsage(t *testing.T) {
	c, repo := deliveryConductor(t, sessionUsageClaude)
	id := c.Create(run.Spec{Prompt: "capture"})

	out := streamOnce(context.Background(), c, id, "a", "capture", repo, nil)

	if out.inputTokens != 12 || out.cacheWriteTokens != 34 || out.cacheReadTokens != 56 {
		t.Errorf("raw usage = in:%d write:%d read:%d, want in:12 write:34 read:56",
			out.inputTokens, out.cacheWriteTokens, out.cacheReadTokens)
	}
	if out.tokens != 4 {
		t.Errorf("tokens = %d, want 4 (the /1000 scaling must be untouched)", out.tokens)
	}

	r, _ := c.Get(id)
	a := findAgent(r.Agents, "a")
	if a == nil {
		t.Fatal("agent 'a' not recorded on the run")
	}
	if a.InputTokens != 12 || a.CacheCreationTokens != 34 || a.CacheReadTokens != 56 {
		t.Errorf("agent raw usage = in:%d create:%d read:%d, want in:12 create:34 read:56",
			a.InputTokens, a.CacheCreationTokens, a.CacheReadTokens)
	}
	if a.Tokens != 4 {
		t.Errorf("agent Tokens = %d, want 4 (display semantics unchanged)", a.Tokens)
	}
}

// A stub that fails the FIRST invocation the way claude fails a dangling
// --resume ("No conversation found", exit 1) and succeeds on the second. Every
// invocation appends its argv to $CANDYLAND_FORK_FIXTURE.args so the test can
// assert the rerun count and that the rerun dropped the fork args.
const forkFallbackClaude = `#!/usr/bin/env bash
prompt="$2"
echo "$@" >> "$CANDYLAND_FORK_FIXTURE.args"
if [[ ! -f "$CANDYLAND_FORK_FIXTURE" ]]; then
  touch "$CANDYLAND_FORK_FIXTURE"
  echo "No conversation found with session ID: sess-tpl" >&2
  exit 1
fi
echo '{"type":"system","subtype":"init","session_id":"sess-cold"}'
echo "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"prompt: $prompt\"}]}}"
echo '{"type":"result","subtype":"success","result":"recovered","usage":{"output_tokens":2000}}'
`

// invocationArgs reads the per-invocation argv lines the stub logged.
func invocationArgs(t *testing.T, fixture string) []string {
	t.Helper()
	b, err := os.ReadFile(fixture + ".args")
	if err != nil {
		t.Fatalf("stub never ran: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// When the fork doesn't resolve, streamOnce reruns ONCE inside the same attempt
// with the fork cleared and the fallback prompt — so the caller sees one clean
// attempt (no runErr) and retry accounting never counts the fallback.
func TestStreamOnceForkFallback(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "fork")
	t.Setenv("CANDYLAND_FORK_FIXTURE", fixture)
	c, repo := deliveryConductor(t, forkFallbackClaude)
	id := c.Create(run.Spec{Prompt: "fork"})

	out := streamOnce(context.Background(), c, id, "a", "forked prompt", repo, nil,
		spawnOpts{forkFrom: "sess-tpl", fallbackPrompt: "FULL BOOTSTRAP fallback"})

	if out.startErr != nil || out.runErr != nil || out.stalled {
		t.Fatalf("fallback attempt must end clean, got startErr=%v runErr=%v stalled=%v stderr=%q",
			out.startErr, out.runErr, out.stalled, out.stderr)
	}
	if !strings.Contains(out.allText, "FULL BOOTSTRAP fallback") {
		t.Errorf("the rerun must carry the fallback prompt, saw %q", out.allText)
	}
	invocations := invocationArgs(t, fixture)
	if len(invocations) != 2 {
		t.Fatalf("want exactly 2 invocations (fork + one fallback), got %d: %v", len(invocations), invocations)
	}
	if !strings.Contains(invocations[0], "--resume sess-tpl --fork-session") {
		t.Errorf("first invocation must fork the template, argv was %q", invocations[0])
	}
	if strings.Contains(invocations[1], "--resume") || strings.Contains(invocations[1], "--fork-session") {
		t.Errorf("the fallback rerun must drop the fork args, argv was %q", invocations[1])
	}
}

// A stub that ALWAYS fails with the missing-conversation error, to pin the
// fallback as strictly one-shot and gated on a fallbackPrompt.
const forkAlwaysFailsClaude = `#!/usr/bin/env bash
echo "$@" >> "$CANDYLAND_FORK_FIXTURE.args"
echo "No conversation found with session ID: sess-tpl" >&2
exit 1
`

func TestStreamOnceForkFallbackIsOneShot(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "fork")
	t.Setenv("CANDYLAND_FORK_FIXTURE", fixture)
	c, repo := deliveryConductor(t, forkAlwaysFailsClaude)
	id := c.Create(run.Spec{Prompt: "fork"})

	out := streamOnce(context.Background(), c, id, "a", "forked prompt", repo, nil,
		spawnOpts{forkFrom: "sess-tpl", fallbackPrompt: "FULL BOOTSTRAP fallback"})

	if out.runErr == nil {
		t.Error("a failed fallback must surface the failure honestly")
	}
	if got := len(invocationArgs(t, fixture)); got != 2 {
		t.Fatalf("the fallback is ONE-SHOT: want exactly 2 invocations, got %d", got)
	}
}

// === RUN-level agents fork doctrine templates ================================
//
// With session reuse ON (and transcripts in place so the worktree copy
// resolves), every run-level spawn — tech lead, coder, reviewer (every round),
// fix — forks its role's doctrine template: `--resume <template-id>
// --fork-session` on the argv, and the reviewer gets the slim bootstrap (no
// kb_get — the doctrine is already in the forked context). With the kill
// switch off the spawns are byte-for-byte today's cold shape.

// templateForkRunClaude drives a full delivery (partition → coder → review
// findings → fix → re-review clean → PR) while recording every spawn's argv
// (newlines flattened) to $CANDYLAND_ARGV_CAPTURE and every reviewer prompt
// verbatim to $CANDYLAND_REVIEW_PROMPTS. A template-creation spawn
// ("pre-loading doctrine") writes the transcript the real claude would
// (projects/<escaped-cwd>/<session-id>.jsonl) so templateForWorkdir's copy
// resolves and the work spawns actually fork.
const templateForkRunClaude = `#!/usr/bin/env bash
if [[ "$1" == "--version" ]]; then echo "9.9.9 (stub)"; exit 0; fi
{ printf '%s' "$*" | tr '\n' ' '; echo; } >> "$CANDYLAND_ARGV_CAPTURE"
prompt="$2"
if [[ "$prompt" == *"pre-loading doctrine"* ]]; then
  sid=""; prev=""
  for a in "$@"; do
    if [[ "$prev" == "--session-id" ]]; then sid="$a"; fi
    prev="$a"
  done
  dir="$CANDYLAND_CLAUDE_PROJECTS_DIR/$(pwd | sed 's/[^a-zA-Z0-9]/-/g')"
  mkdir -p "$dir"
  echo '{"doctrine":true}' > "$dir/$sid.jsonl"
  echo "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$sid\"}"
  echo '{"type":"result","subtype":"success","result":"READY","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"code reviewer"* ]]; then
  printf '%s\n' "$prompt" >> "$CANDYLAND_REVIEW_PROMPTS"
  n=$(cat "$CANDYLAND_REVIEW_COUNT" 2>/dev/null || echo 0)
  n=$((n+1)); echo "$n" > "$CANDYLAND_REVIEW_COUNT"
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff"}}]}}'
  if [[ "$n" -le 1 ]]; then
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_FINDINGS {\"blockers\":[{\"file\":\"a.txt\",\"line\":1,\"issue\":\"needs a guard\"}]}"}]}}'
  else
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"REVIEW_CLEAN"}]}}'
  fi
  echo '{"type":"result","subtype":"success","result":"reviewed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"review findings"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file":"a.txt"}}]}}'
  printf 'fixed per review\n' >> "a.txt"
  echo '{"type":"result","subtype":"success","result":"fixed","usage":{"output_tokens":1}}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\"id\":\"a\",\"title\":\"task a\",\"files\":[\"a.txt\"],\"test\":\"t\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "content" > "a.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`

// forkRunFixtures wires the argv/prompt/round fixtures plus the delivery
// harness for the template-fork run tests, returning the conductor and the
// fixture paths (argv capture, reviewer prompts).
func forkRunFixtures(t *testing.T) (*Conductor, string, string) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "argv.txt")
	prompts := filepath.Join(t.TempDir(), "reviewer-prompts.txt")
	t.Setenv("CANDYLAND_ARGV_CAPTURE", capture)
	t.Setenv("CANDYLAND_REVIEW_PROMPTS", prompts)
	t.Setenv("CANDYLAND_REVIEW_COUNT", filepath.Join(t.TempDir(), "n"))
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "3")
	c, _ := deliveryConductor(t, templateForkRunClaude)
	writeFakeDetritus(t) // deterministic doctrine-version stamp for the registry
	return c, capture, prompts
}

// readLines returns the non-empty lines of a fixture file.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %s never written: %v", path, err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// flagValue extracts the token following a flag in a flattened argv line.
func flagValue(line, flag string) string {
	_, rest, ok := strings.Cut(line, flag+" ")
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, " ")
	return v
}

func hasForkArgs(line string) bool {
	return strings.Contains(line, "--resume ") && strings.Contains(line, "--fork-session")
}

// classifySpawns buckets the captured argv lines by the spawn's role prompt.
func classifySpawns(lines []string) (creations, tls, coders, reviewers, fixes []string) {
	for _, ln := range lines {
		switch {
		case strings.Contains(ln, "pre-loading doctrine"):
			creations = append(creations, ln)
		case strings.Contains(ln, "code reviewer"):
			reviewers = append(reviewers, ln)
		case strings.Contains(ln, "review findings"):
			fixes = append(fixes, ln)
		case strings.Contains(ln, "tech lead"):
			tls = append(tls, ln)
		default:
			coders = append(coders, ln)
		}
	}
	return creations, tls, coders, reviewers, fixes
}

// With templates available, every run-level agent forks its role's template:
// the tech lead, the coder, the fix pass, and the reviewer on EVERY round —
// the reviewer with the slim bootstrap (no kb_get), resuming exactly the
// reviewer template's session id. The run still delivers its PR.
func TestRunAgentsForkDoctrineTemplates(t *testing.T) {
	c, capture, prompts := forkRunFixtures(t)
	t.Setenv("CANDYLAND_SESSION_REUSE", "1")               // re-enable over the harness default
	t.Setenv("CANDYLAND_CLAUDE_PROJECTS_DIR", t.TempDir()) // the stub writes transcripts here

	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)
	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("forked run did not finish cleanly: status=%q error=%q", r.Status, r.Error)
	}
	if r.PrURL == "" {
		t.Error("a template-forked run must still deliver its PR")
	}

	creations, tls, coders, reviewers, fixes := classifySpawns(readLines(t, capture))
	// One template creation per role that spawned: tech-lead, coder, reviewer, fix.
	if len(creations) != 4 {
		t.Errorf("want 4 template creations (tech-lead, coder, reviewer, fix), got %d:\n%s", len(creations), strings.Join(creations, "\n"))
	}
	if len(tls) != 1 || len(coders) != 1 || len(reviewers) != 2 || len(fixes) != 1 {
		t.Fatalf("spawn shape: tl=%d coder=%d reviewer=%d fix=%d, want 1/1/2/1\ncoder-bucket lines:\n%s", len(tls), len(coders), len(reviewers), len(fixes), strings.Join(coders, "\n---\n"))
	}
	for _, ln := range tls {
		if !hasForkArgs(ln) {
			t.Errorf("the tech-lead spawn must fork its template, argv was %q", ln)
		}
		if !strings.Contains(ln, "You are the tech lead. Call the brief_get tool FIRST") {
			t.Errorf("the tech-lead prompt must stay techLeadBootstrap, argv was %q", ln)
		}
	}
	for _, ln := range coders {
		if !hasForkArgs(ln) {
			t.Errorf("the coder spawn must fork its template, argv was %q", ln)
		}
	}
	for _, ln := range fixes {
		if !hasForkArgs(ln) {
			t.Errorf("the fix spawn must fork its template, argv was %q", ln)
		}
	}
	// EVERY reviewer round forks the reviewer template with the slim bootstrap.
	var reviewerTemplate string
	for _, ln := range creations {
		if strings.Contains(ln, "core/review-rigor") {
			reviewerTemplate = flagValue(ln, "--session-id")
		}
	}
	if reviewerTemplate == "" {
		t.Fatal("no reviewer template creation recorded")
	}
	for round, ln := range reviewers {
		if !hasForkArgs(ln) {
			t.Errorf("reviewer round %d must fork the template, argv was %q", round+1, ln)
		}
		if got := flagValue(ln, "--resume"); got != reviewerTemplate {
			t.Errorf("reviewer round %d resumes %q, want the reviewer template %q", round+1, got, reviewerTemplate)
		}
		if strings.Contains(ln, "kb_get") {
			t.Errorf("a forked reviewer must get the slim bootstrap (no kb_get), argv was %q", ln)
		}
	}
	for i, p := range readLines(t, prompts) {
		if p != reviewBootstrapSlim {
			t.Errorf("reviewer prompt %d is not reviewBootstrapSlim:\n got %q", i+1, p)
		}
	}
}

// The kill switch: with CANDYLAND_SESSION_REUSE=0 no template is created and
// every spawn is byte-for-byte today's cold shape — no fork args anywhere, and
// the reviewer prompt is exactly the full kb_get reviewBootstrap on every round.
func TestRunAgentsColdWithoutSessionReuse(t *testing.T) {
	c, capture, prompts := forkRunFixtures(t)
	t.Setenv("CANDYLAND_SESSION_REUSE", "0")

	id := c.Create(run.Spec{Prompt: "do the thing"})
	c.Begin(id)
	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("cold run did not finish cleanly: status=%q error=%q", r.Status, r.Error)
	}

	lines := readLines(t, capture)
	creations, _, _, reviewers, _ := classifySpawns(lines)
	if len(creations) != 0 {
		t.Errorf("the kill switch must prevent template creation, got %d creation spawns", len(creations))
	}
	for _, ln := range lines {
		if strings.Contains(ln, "--resume") || strings.Contains(ln, "--fork-session") {
			t.Errorf("no spawn may carry fork args with reuse off, argv was %q", ln)
		}
	}
	if len(reviewers) != 2 {
		t.Fatalf("want 2 reviewer rounds, got %d", len(reviewers))
	}
	for i, p := range readLines(t, prompts) {
		if p != reviewBootstrap {
			t.Errorf("reviewer prompt %d must be byte-for-byte reviewBootstrap:\n got %q", i+1, p)
		}
	}
}

func TestStreamOnceNoFallbackWithoutPrompt(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "fork")
	t.Setenv("CANDYLAND_FORK_FIXTURE", fixture)
	c, repo := deliveryConductor(t, forkAlwaysFailsClaude)
	id := c.Create(run.Spec{Prompt: "fork"})

	out := streamOnce(context.Background(), c, id, "a", "forked prompt", repo, nil,
		spawnOpts{forkFrom: "sess-tpl"})

	if out.runErr == nil {
		t.Error("the fork failure must surface when no fallback is configured")
	}
	if got := len(invocationArgs(t, fixture)); got != 1 {
		t.Fatalf("no fallbackPrompt: want exactly 1 invocation, got %d", got)
	}
}
