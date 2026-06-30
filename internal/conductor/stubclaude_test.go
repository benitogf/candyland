package conductor

import "strings"

// === Deterministic stub-`claude` harness ==================================
//
// The conductor never calls a real model in tests. Every run/quest/campaign
// flow is regression-tested against a STUB `claude` — an executable bash script
// the executor spawns instead of the real CLI (the executor honours the
// CANDYLAND_CLAUDE override; see writeFakeClaude). The stub speaks the same
// contract the real claude does:
//
//   - it is invoked as `claude -p <prompt> --output-format stream-json …`, so
//     the role bootstrap is in `$2` (the conventional `prompt="$2"` line);
//   - it writes newline-delimited stream-json envelopes to stdout — `assistant`
//     turns carrying `text`/`tool_use` content and a terminal `result` line;
//   - it signals each stage with the SAME fenced verdict lines a real agent
//     emits and the conductor parses: PARTITION / TEST / REVIEW_CLEAN for a run,
//     WORKITEMS(/_NONE) for a quest lead, INTENT_BRIEF / INTENT_REVIEW for a
//     campaign supervisor.
//
// Because the spawned process is real and the I/O contract is real, these tests
// exercise the genuine executor (partition → worktrees → integrate → push → PR,
// or the quest/campaign supervisor loops) with NO Anthropic tokens — the only
// thing replaced is the model's judgement, which the stub scripts deterministically.
//
// # Writing a deterministic regression test
//
// Build a stub by composing per-role fragments with stubClaude(...) and hand it
// to deliveryConductor (single repo), multiRepoConductor (N repos), or the
// quest/campaign helpers. A role fragment is the bash that runs when the spawn's
// prompt matches a role; stubClaude dispatches on `$prompt` in role order and
// runs the first match, falling through to the coder fragment (the default, no
// role keyword). Example:
//
//	script := stubClaude(
//	    roleCleanReviewer,                       // any "code reviewer" spawn → REVIEW_CLEAN
//	    role("tech lead", emitPartition(`[{"id":"a","files":["a.txt"],"test":"t"}]`)),
//	    coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),  // default branch
//	)
//	c, repo := deliveryConductor(t, script)
//
// To script per-tick or per-stage behaviour (e.g. fail a gate ONCE then pass),
// branch inside a fragment on a marker file whose path the test sets via t.Setenv
// — the established CANDYLAND_*_FIXTURE convention (touch on first call, test
// `-f` on later calls). See questTickClaude / campaignClaude for worked oracles.
//
// Keep stubs minimal: emit only the envelopes and verdict lines the asserted
// transition needs. The fenced-line conventions are pinned independently by the
// parse* tests (TestParseWorkItems, TestParseCampaignVerdicts, …), so a stub and
// a real agent can never drift on the contract silently.

// stubClaude composes role fragments into a complete, executable stub-`claude`
// bash script. Fragments are tried in order against the spawn prompt ($2); the
// first whose keyword matches runs, and a keyword-less fragment (see coder) is
// the default that catches every coder spawn. The returned script is ready to
// pass to writeFakeClaude / deliveryConductor.
func stubClaude(fragments ...stubFragment) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nprompt=\"$2\"\n")
	first := true
	var fallback string
	for _, f := range fragments {
		if f.keyword == "" { // the default (coder) branch — emitted last
			fallback = f.body
			continue
		}
		kw := "if"
		if !first {
			kw = "elif"
		}
		first = false
		b.WriteString(kw + " [[ \"$prompt\" == *\"" + f.keyword + "\"* ]]; then\n")
		b.WriteString(indent(f.body))
	}
	if first { // no keyworded fragment — the whole script is just the fallback
		b.WriteString(fallback)
		return b.String()
	}
	b.WriteString("else\n")
	b.WriteString(indent(fallback))
	b.WriteString("fi\n")
	return b.String()
}

// stubFragment is one role branch: a keyword matched against the spawn prompt
// and the bash body that runs on a match. An empty keyword marks the default
// (coder) branch.
type stubFragment struct {
	keyword string
	body    string
}

// role builds a fragment that runs `body` when the spawn prompt contains
// `keyword` (e.g. "tech lead", "intent lead", "code reviewer").
func role(keyword, body string) stubFragment { return stubFragment{keyword: keyword, body: body} }

// coder builds the DEFAULT fragment — the branch that runs for any spawn whose
// prompt matches no other role, i.e. an implementation coder. Its body is the
// concatenation of the given step bodies.
func coder(steps ...string) stubFragment { return stubFragment{body: strings.Join(steps, "")} }

func indent(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// --- reusable step bodies (emit the stream-json + fenced verdict lines) ------

func emitText(text string) string {
	return "echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"" + text + "\"}]}}'\n"
}

func emitResult(result string, outTokens int) string {
	return "echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"" + result + "\",\"usage\":{\"output_tokens\":" + itoa(outTokens) + "}}'\n"
}

// emitPartition emits the tech-lead PARTITION verdict (the JSON array of tasks)
// followed by a terminal result line.
func emitPartition(tasksJSON string) string {
	return emitText("PARTITION "+escapeJSON(tasksJSON)) + emitResult("ok", 1)
}

// emitTest emits a coder's TEST verdict with the given pass/fail counts.
func emitTest(pass, fail int) string {
	return emitText(`TEST {\"pass\":`+itoa(pass)+`,\"fail\":`+itoa(fail)+`}`) + emitResult("green", 2)
}

// writeWorktreeFile makes the coder write a real file in its worktree (cwd) so
// there is a genuine edit to commit and merge. The file name is PID-suffixed so
// parallel coders sharing a branch don't collide.
func writeWorktreeFile(name string) string {
	return emitText("implementing") +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Write\",\"input\":{\"file\":\"" + name + "\"}}]}}'\n" +
		"echo \"work by $$\" > \"" + name + "\"\n"
}

// roleCleanReviewer is the common code-reviewer fragment: it inspects the diff
// and returns REVIEW_CLEAN, so the run/quest proceeds to open its PR.
var roleCleanReviewer = role("code reviewer",
	"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Bash\",\"input\":{\"command\":\"git diff\"}}]}}'\n"+
		emitText("REVIEW_CLEAN")+emitResult("reviewed", 1))

// escapeJSON backslash-escapes the double quotes in a JSON literal so it can be
// embedded inside a single-quoted echo of a stream-json envelope.
func escapeJSON(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	if neg {
		d = append([]byte{'-'}, d...)
	}
	return string(d)
}
