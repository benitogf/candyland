package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

// A stub whose stream carries a session id (on the SECOND line — the first has
// none, pinning first-non-empty capture) and a result line with the full usage
// block. output_tokens is 4000 so the /1000 display scaling stays observable.
const sessionUsageClaude = `#!/usr/bin/env bash
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"warming"}]}}'
echo '{"type":"system","subtype":"init","session_id":"sess-first"}'
echo '{"type":"assistant","session_id":"sess-later","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}'
echo '{"type":"result","subtype":"success","session_id":"sess-later","result":"done","usage":{"output_tokens":4000,"input_tokens":12,"cache_creation_input_tokens":34,"cache_read_input_tokens":56}}'
`

// streamOnce must capture the FIRST non-empty session_id and accumulate the raw
// (unscaled) usage counts, while the existing /1000 tokens field is untouched;
// the same raw counts must land on the agent record for the dashboard/learn.
func TestStreamOnceCapturesSessionAndRawUsage(t *testing.T) {
	c, repo := deliveryConductor(t, sessionUsageClaude)
	id := c.Create(run.Spec{Prompt: "capture"})

	out := streamOnce(context.Background(), c, id, "a", "capture", repo, nil)

	if out.sessionID != "sess-first" {
		t.Errorf("sessionID = %q, want the FIRST non-empty session_id %q", out.sessionID, "sess-first")
	}
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
	if out.sessionID != "sess-cold" {
		t.Errorf("sessionID = %q, want the fallback run's %q", out.sessionID, "sess-cold")
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
