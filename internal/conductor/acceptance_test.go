package conductor

import (
	"reflect"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// #63 Blocker 1: parseAcceptanceCommands must not misfire either way — it must not run
// prose checklist items as commands (false positive), and it must anchor on a REAL
// "Acceptance" heading, not the first heading that merely contains the word (false
// negative / mis-anchor).
func TestParseAcceptanceCommands(t *testing.T) {
	// (1) A prose checklist item (backtick span PLUS other text) yields NO command.
	prose := "# Task\n\n## Acceptance\n- [ ] `techLeadBootstrap` contains \"ENTIRE scope\"\n"
	if got := parseAcceptanceCommands(prose); len(got) != 0 {
		t.Errorf("prose checklist item must yield no command, got %v", got)
	}
	// (2) A real acceptance section: fenced sh block (each non-blank line) + a
	// whole-backtick checklist item → exactly those commands, in order.
	real := "## Acceptance criteria\nSome intro prose.\n```sh\ngo build ./...\ngo test ./...\n```\n- [ ] `golangci-lint run`\n\n## Next\n```sh\nnot-a-command\n```\n"
	want := []string{"go build ./...", "go test ./...", "golangci-lint run"}
	if got := parseAcceptanceCommands(real); !reflect.DeepEqual(got, want) {
		t.Errorf("real acceptance section commands = %v, want %v", got, want)
	}
	// (3) Mis-anchor guard: a mid-sentence heading that merely CONTAINS "acceptance"
	// must not win over the real "## Acceptance" heading below it.
	misanchor := "### E. #63 optional — pre-`done` acceptance executor\n```sh\nfalse\n```\n\n## Acceptance\n```sh\ntrue\n```\n"
	if got := parseAcceptanceCommands(misanchor); !reflect.DeepEqual(got, []string{"true"}) {
		t.Errorf("parser must anchor on the real ## Acceptance heading, got %v", got)
	}
	// A doc with no acceptance section at all yields nil.
	if got := parseAcceptanceCommands("# Title\njust prose\n"); got != nil {
		t.Errorf("no acceptance section must yield nil, got %v", got)
	}
	// (4) Fence-blind anchor guard: a `# acceptance …` line INSIDE a ```text block must
	// NOT anchor the section — only the real `## Acceptance` heading outside a fence does.
	fenced := "```text\n# Acceptance flow diagram\n```\n- [ ] `should-not-run`\n\n## Acceptance\n```sh\ntrue\n```\n"
	if got := parseAcceptanceCommands(fenced); !reflect.DeepEqual(got, []string{"true"}) {
		t.Errorf("a `#` line inside a fenced block must not anchor the section, got %v", got)
	}
	// (5) Unclosed sh fence: a leaked `##` heading ends the section so trailing prose is
	// not run as commands.
	unclosed := "## Acceptance\n```sh\ngo test ./...\n## Rollout\nrm -rf /important/data\nmore prose\n"
	if got := parseAcceptanceCommands(unclosed); !reflect.DeepEqual(got, []string{"go test ./..."}) {
		t.Errorf("an unclosed sh fence must not run trailing prose past a heading, got %v", got)
	}
	// (6) `#` comment lines inside an sh fence are skipped (not collected), so they don't
	// inflate the command count — but real commands around them still collect.
	comments := "## Acceptance\n```sh\n# set up the checks\ngo build ./...\n# then test\ngo test ./...\n```\n"
	if got := parseAcceptanceCommands(comments); !reflect.DeepEqual(got, []string{"go build ./...", "go test ./..."}) {
		t.Errorf("bash `#` comments in an sh fence must be skipped, got %v", got)
	}
	// (7) Word-anchored heading: "## Acceptances …" must NOT match; "## Acceptance
	// criteria" must.
	if got := parseAcceptanceCommands("## Acceptances and other notes\n```sh\nnope\n```\n"); got != nil {
		t.Errorf("`## Acceptances` (word not \"acceptance\") must not anchor, got %v", got)
	}
	if got := parseAcceptanceCommands("## Acceptance criteria\n```sh\nyep\n```\n"); !reflect.DeepEqual(got, []string{"yep"}) {
		t.Errorf("`## Acceptance criteria` must anchor, got %v", got)
	}
}

// acceptanceClaude drives a full clean delivery (tech lead → coder → clean review →
// clean fix pass). It is used with a prompt that carries an `## Acceptance` section so
// the pre-`done` acceptance pass runs on the integrated branch.
var acceptanceClaude = stubClaude(
	roleCleanReviewer,
	role("review findings", coderFixWrite),
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

// coderFixWrite is a fix-pass body that makes a real edit (so fixReviewFindings
// commits), letting the acceptance re-run be the decider.
const coderFixWrite = "echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Edit\",\"input\":{\"file\":\"a.txt\"}}]}}'\n" +
	"printf 'fix attempt\\n' >> a.txt\n" +
	"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"fixed\",\"usage\":{\"output_tokens\":1}}'\n"

// #63: a run whose `## Acceptance` section carries a command that FAILS on the
// integrated branch must NOT reach a clean `done` — the failing check folds into the
// bounded remediation, re-runs, and (still failing) terminates the run blocked.
func TestAcceptanceFailureBlocksDelivery(t *testing.T) {
	c, _ := deliveryConductor(t, acceptanceClaude)
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "3")
	prompt := "do the thing\n\n## Acceptance\n```sh\nfalse\n```\n"
	id := c.Create(run.Spec{Prompt: prompt})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool {
		return r.Status == "blocked" || r.Status == "done" || r.Status == "delivery-failed"
	}, 60*time.Second)
	if r.Status == "done" {
		t.Fatalf("a failing acceptance check must not reach clean done (error=%q)", r.Error)
	}
	if r.Status != "blocked" {
		t.Fatalf("a failing acceptance check must terminate blocked, got %q (error=%q)", r.Status, r.Error)
	}
	if r.PrURL != "" {
		t.Errorf("a failing acceptance check must not open a PR, got %q", r.PrURL)
	}
}

// acceptanceNoDiffCleanClaude mirrors acceptanceClaude but its fix pass makes NO edit
// and instead re-stamps an evidence-cited REVIEW_CLEAN — the #71 no-diff accept shape.
var acceptanceNoDiffCleanClaude = stubClaude(
	roleCleanReviewer,
	role("review findings",
		emitText("Verified: the change is wired and the suite is green.")+
			emitText("REVIEW_CLEAN")+emitResult("resolved", 1)),
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

// #71 guard: the no-diff accept path must not let a fix pass TALK its way past a
// failing acceptance check. The accepted clean-stamped resolution returns fixOK, but
// executeAcceptance re-runs the command — still failing, the run blocks and no PR
// opens; the command re-run, not the fix pass's verdict, is the ground truth.
func TestAcceptanceFailureNoDiffCleanStampStillBlocks(t *testing.T) {
	c, _ := deliveryConductor(t, acceptanceNoDiffCleanClaude)
	t.Setenv("CANDYLAND_REVIEW_ROUNDS", "3")
	prompt := "do the thing\n\n## Acceptance\n```sh\nfalse\n```\n"
	id := c.Create(run.Spec{Prompt: prompt})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool {
		return r.Status == "blocked" || r.Status == "done" || r.Status == "delivery-failed"
	}, 60*time.Second)
	if r.Status == "done" {
		t.Fatalf("a clean-stamped no-diff fix must not deliver past a failing acceptance check (error=%q)", r.Error)
	}
	if r.Status != "blocked" {
		t.Fatalf("a still-failing acceptance check must terminate blocked, got %q (error=%q)", r.Status, r.Error)
	}
	if r.PrURL != "" {
		t.Errorf("a still-failing acceptance check must not open a PR, got %q", r.PrURL)
	}
}

// #63 regression: a run whose prompt has NO runnable acceptance section delivers
// exactly as before (clean done + PR opened).
func TestNoAcceptanceSectionDeliversAsBefore(t *testing.T) {
	c, _ := deliveryConductor(t, acceptanceClaude)
	id := c.Create(run.Spec{Prompt: "do the thing (no acceptance section here)"})
	c.Begin(id)

	r := waitFor(t, c, id, func(r run.Run) bool { return r.Status == "done" }, 40*time.Second)
	if r.Status != "done" || r.Error != "" {
		t.Fatalf("a run with no acceptance section must deliver clean, got status=%q error=%q", r.Status, r.Error)
	}
	if r.PrURL == "" {
		t.Error("a clean run with no acceptance section must open a PR")
	}
}
