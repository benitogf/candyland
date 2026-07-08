package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// === Task 1: real session-id capture + true resume =========================

// argvLoggingLimitStub is a stub `claude` that, on its FIRST spawn, emits an init
// line carrying session_id=$SID, then dies with a usage-limit banner (a limit death
// must not burn an attempt — it resumes). On later spawns it does real work. Every
// spawn appends its full argv to $ARGV_LOG so the test can assert the resume argv.
func argvLoggingLimitStub(sid, marker, argvLog string, emitInit bool) string {
	init := ""
	if emitInit {
		init = "  echo '{\"type\":\"assistant\",\"session_id\":\"" + sid + "\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"starting\"}]}}'\n"
	}
	return "#!/usr/bin/env bash\n" +
		"printf '%s\\n' \"$*\" >> \"" + argvLog + "\"\n" +
		"prompt=\"$2\"\n" +
		"if [[ ! -f \"" + marker + "\" ]]; then\n" +
		"  touch \"" + marker + "\"\n" +
		init +
		"  echo \"Claude AI usage limit reached|$(( $(date +%s) + 1 ))\" >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"echo '{\"type\":\"assistant\",\"session_id\":\"" + sid + "\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Write\",\"input\":{\"file\":\"x\"}}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"resumed green\",\"usage\":{\"output_tokens\":3}}'\n"
}

// A forked spawn interrupted by a usage limit resumes the CAPTURED work session (the
// init line's session_id), with resumeContinuePrompt — NOT the template it forked
// from, and NOT --fork-session (a resume, not a fork).
func TestForkedSpawnResumesCapturedSession(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hit")
	argvLog := filepath.Join(dir, "argv.log")
	writeExec(t, argvLoggingLimitStub("work-sess", marker, argvLog, true))
	t.Setenv("CANDYLAND_AGENT_TIMEOUT_MS", "500")
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000")

	c := New(nil)
	id := c.Create(run.Spec{Prompt: "do the thing"})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out := streamOnce(ctx, c, id, "a", "ORIGINAL", t.TempDir(), nil,
		spawnOpts{forkFrom: "tmpl-1", fallbackPrompt: "FULL BOOTSTRAP"})
	if out.startErr != nil || out.runErr != nil || out.stalled {
		t.Fatalf("resume spawn must complete green: %+v", out)
	}
	if out.sessionID != "work-sess" {
		t.Fatalf("attemptOutcome.sessionID must be the captured init session, got %q", out.sessionID)
	}
	lines := spineArgvLines(t, argvLog)
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 spawns (fork death + resume), got %v", lines)
	}
	first, resume := lines[0], lines[len(lines)-1]
	if !strings.Contains(first, "--resume tmpl-1") || !strings.Contains(first, "--fork-session") {
		t.Errorf("first spawn must fork the template, got %q", first)
	}
	if !strings.Contains(resume, "--resume work-sess") {
		t.Errorf("resume must continue the captured work session, got %q", resume)
	}
	if strings.Contains(resume, "--fork-session") {
		t.Errorf("a resume is not a fork — must not carry --fork-session, got %q", resume)
	}
	if !strings.Contains(resume, resumeContinuePrompt) {
		t.Errorf("resume must carry resumeContinuePrompt, got %q", resume)
	}
}

// With NO init line (the work session id never arrived), the resume falls back to
// today's behavior byte-for-byte: redo cold on the TEMPLATE id with the full bootstrap.
func TestNoInitLineTemplateRedo(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hit")
	argvLog := filepath.Join(dir, "argv.log")
	writeExec(t, argvLoggingLimitStub("unused", marker, argvLog, false))
	t.Setenv("CANDYLAND_AGENT_TIMEOUT_MS", "500")
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000")

	c := New(nil)
	id := c.Create(run.Spec{Prompt: "do the thing"})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	streamOnce(ctx, c, id, "a", "ORIGINAL", t.TempDir(), nil,
		spawnOpts{forkFrom: "tmpl-1", fallbackPrompt: "FULL BOOTSTRAP"})
	lines := spineArgvLines(t, argvLog)
	resume := lines[len(lines)-1]
	if !strings.Contains(resume, "--resume tmpl-1") {
		t.Errorf("no-init redo must resume the TEMPLATE id, got %q", resume)
	}
	if strings.Contains(resume, resumeContinuePrompt) {
		t.Errorf("no-init redo keeps the full-bootstrap fallback, not resumeContinuePrompt, got %q", resume)
	}
	if !strings.Contains(resume, "FULL BOOTSTRAP") {
		t.Errorf("no-init redo must carry the fallback bootstrap, got %q", resume)
	}
}

// === Task 6: repairVerdict resumes the agent's own session ONCE, maxTurns 2 ==

func TestRepairVerdictResumesSessionOnce(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	stub := "#!/usr/bin/env bash\n" +
		"printf '%s\\n' \"$*\" >> \"" + argvLog + "\"\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"WORKITEMS_NONE\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"usage\":{\"output_tokens\":1}}'\n"
	writeExec(t, stub)

	c := New(nil)
	out, did := c.repairVerdict(context.Background(), "q1", "quest-lead", "sess-9", "WORKITEMS", t.TempDir(), nil, "", "")
	if !did {
		t.Fatal("repairVerdict must run when a session id is known")
	}
	if _, none, ok := parseWorkItems(out.allText); !ok || !none {
		t.Fatalf("the repaired verdict must re-parse: allText=%q", out.allText)
	}
	lines := spineArgvLines(t, argvLog)
	if len(lines) != 1 {
		t.Fatalf("repair must be exactly ONE spawn, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "--resume sess-9") || !strings.Contains(lines[0], "--max-turns 2") {
		t.Errorf("repair must resume the session with --max-turns 2, got %q", lines[0])
	}
	// No session id → no spawn at all.
	if _, did := c.repairVerdict(context.Background(), "q1", "quest-lead", "", "WORKITEMS", t.TempDir(), nil, "", ""); did {
		t.Error("repairVerdict must not spawn when no session id is known")
	}
}

// A repair that also fails, with a usage-limit banner in its terminal, is reported as
// a limit interruption — never misattributed as agent noncompliance.
func TestNoVerdictReasonLimitVsClean(t *testing.T) {
	limit := attemptOutcome{runErr: errStub, stderr: "Claude AI usage limit reached|123"}
	if got := noVerdictReason(limit, "quest-lead", "WORKITEMS"); !strings.Contains(got, "usage limit interrupted") {
		t.Errorf("a limit terminal must read as a limit interruption, got %q", got)
	}
	clean := attemptOutcome{lastText: "I looked but found nothing to emit"}
	if got := noVerdictReason(clean, "quest-lead", "WORKITEMS"); got != "produced no WORKITEMS verdict" {
		t.Errorf("a clean non-limit terminal must read as produced-no-verdict, got %q", got)
	}
}

// === Task 5: two-layer intent parse + render ===============================

func TestParseIntentConflicts(t *testing.T) {
	notes := parseIntentConflicts(`preamble
INTENT_CONFLICT {"issue":"the diff drops auth the root requires"}
INTENT_CONFLICT {"issue":""}
INTENT_CONFLICT not json
REVIEW_CLEAN`)
	if len(notes) != 1 {
		t.Fatalf("only the one well-formed non-empty conflict should parse, got %+v", notes)
	}
	if notes[0].Issue != "the diff drops auth the root requires" {
		t.Errorf("issue not parsed: %+v", notes[0])
	}
	if len(parseIntentConflicts("no conflict here")) != 0 {
		t.Error("absent INTENT_CONFLICT lines must parse to nothing")
	}
}

// === Task 10: captureIncidents dedups a double-parsed line ==================

func TestCaptureIncidentsDedup(t *testing.T) {
	c, _ := newQuestServer(t)
	id := c.Create(run.Spec{Prompt: "x"})
	// The same INCIDENT line appears twice (the allText result-echo double-parse).
	line := `INCIDENT {"summary":"worked around a stale lockfile","detail":"refetched","severity":"warn"}`
	c.captureIncidents(id, "a", line+"\n"+line)
	r, _ := c.Get(id)
	if len(r.Incidents) != 1 {
		t.Fatalf("a duplicated incident within one capture must record ONCE, got %+v", r.Incidents)
	}
	// Two genuinely distinct incidents still both record.
	c.captureIncidents(id, "a", `INCIDENT {"summary":"one"}`+"\n"+`INCIDENT {"summary":"two"}`)
	r, _ = c.Get(id)
	if len(r.Incidents) != 3 {
		t.Fatalf("distinct incidents must record, got %+v", r.Incidents)
	}
}

// === rootIntentFor walks up the hierarchy =================================

func TestRootIntentFor(t *testing.T) {
	c, _ := newQuestServer(t)
	// Standalone run → its own intent.
	rid := c.Create(run.Spec{Prompt: "add csv export"})
	if got := c.rootIntentFor(rid); !strings.Contains(got, "csv export") {
		t.Errorf("standalone run root intent = %q", got)
	}
	// Campaign → OriginalInput.
	cid := c.CreateCampaign(run.CampaignSpec{Input: "overhaul billing"})
	if got := c.rootIntentFor(cid); got != "overhaul billing" {
		t.Errorf("campaign root intent = %q", got)
	}
	// Campaign-child quest → the campaign's input (highest ancestor).
	qid := c.CreateQuest(run.QuestSpec{Objective: "migrate the ledger", CampaignID: cid})
	if got := c.rootIntentFor(qid); got != "overhaul billing" {
		t.Errorf("campaign-child quest root intent must be the campaign input, got %q", got)
	}
	// Standalone quest → its own objective.
	sqid := c.CreateQuest(run.QuestSpec{Objective: "keep lint clean"})
	if got := c.rootIntentFor(sqid); got != "keep lint clean" {
		t.Errorf("standalone quest root intent = %q", got)
	}
}

// === Task 8: remediationQuests fold structural review findings =============

func TestRemediationQuestsIncludeStructural(t *testing.T) {
	brief := run.IntentBrief{RestatedGoal: "g", Commitments: []run.Commitment{{ID: "c1", Statement: "ship the API"}}}
	review := run.IntentReview{Verdicts: []run.CommitmentVerdict{{CommitmentID: "c1", Verdict: "missed", Evidence: []string{"no handler"}}}}
	structural := []reviewFinding{{Issue: "the feature is not wired from main"}}
	qs := remediationQuests(run.Campaign{}, brief, review, structural)
	if len(qs) != 2 {
		t.Fatalf("expected one quest per missed commitment + one per structural finding, got %+v", qs)
	}
	joined := qs[0].Objective + qs[1].Title + qs[1].Objective
	if !strings.Contains(joined, "not wired from main") {
		t.Errorf("a structural finding must become a remediation quest, got %+v", qs)
	}
}

// === reviewReverifyPrompt carries the just-fixed blockers ==================

func TestReviewReverifyPromptCarriesBlockers(t *testing.T) {
	p := reviewReverifyPrompt([]reviewFinding{{File: "a.go", Line: 3, Issue: "off by one"}})
	if !strings.Contains(p, reviewReverifyBootstrap) {
		t.Error("reverify prompt must embed the reverify bootstrap")
	}
	if !strings.Contains(p, "a.go:3 off by one") {
		t.Errorf("reverify prompt must list the cited blockers, got %q", p)
	}
}

// === Task 2: splitFindings splits by the on-branch predicate ===============

func TestSplitFindings(t *testing.T) {
	repo := newGitRepo(t)
	ctx := context.Background()
	// README.md exists on main; ghost.go does not.
	citable, structural := splitFindings(ctx, repo, "main", []reviewFinding{
		{File: "README.md", Issue: "cited, present"},
		{File: "ghost.go", Issue: "cited, absent"},
		{File: "", Issue: "no file at all"},
	})
	if len(citable) != 1 || citable[0].File != "README.md" {
		t.Errorf("only the present-file finding is citable, got %+v", citable)
	}
	if len(structural) != 2 {
		t.Errorf("an absent file and an empty file are both structural, got %+v", structural)
	}
}

// === Task 3: reviewer session continuity across rounds =====================

// reviewerArgvStub emits a fixed session_id and REVIEW_FINDINGS, logging argv.
func reviewerArgvStub(argvLog string) string {
	return "#!/usr/bin/env bash\n" +
		"printf '%s\\n' \"$*\" >> \"" + argvLog + "\"\n" +
		"echo '{\"type\":\"assistant\",\"session_id\":\"rev-1\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"REVIEW_CLEAN\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"usage\":{\"output_tokens\":1}}'\n"
}

// Round 1 forks/cold-spawns and captures the session; round ≥ 2 RESUMES it with the
// reverify prompt (no --fork-session). CANDYLAND_REVIEW_CONTINUITY=0 forks every round.
func TestReviewerContinuityRoundTwoResumes(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	writeExec(t, reviewerArgvStub(argvLog))

	c := New(nil)
	var sess string
	// Round 1: cold spawn (no template) captures the session id.
	c.spawnReviewer(context.Background(), "q1", "repo", t.TempDir(), nil, 1, "", false, "", "", nil, &sess)
	if sess != "rev-1" {
		t.Fatalf("round 1 must capture the reviewer session, got %q", sess)
	}
	// Round 2: resume the captured session with the reverify prompt. (The prompt
	// carries newlines, so assert on the whole argv log; --resume rev-1 and the
	// reverify text appear only in the round-2 spawn.)
	os.Remove(argvLog)
	c.spawnReviewer(context.Background(), "q1", "repo", t.TempDir(), nil, 2, "", false, "", "",
		[]reviewFinding{{File: "a.go", Line: 2, Issue: "x"}}, &sess)
	log2 := readFileStr(t, argvLog)
	if !strings.Contains(log2, "--resume rev-1") || strings.Contains(log2, "--fork-session") {
		t.Errorf("round 2 must resume the round-1 session (no fork), got %q", log2)
	}
	if !strings.Contains(log2, "Re-verify with FRESH evidence") {
		t.Errorf("round 2 must carry the reverify prompt, got %q", log2)
	}

	// Kill switch: every round forks (never resumes).
	t.Setenv("CANDYLAND_REVIEW_CONTINUITY", "0")
	os.Remove(argvLog)
	var sess2 string
	c.spawnReviewer(context.Background(), "q1", "repo", t.TempDir(), nil, 1, "", false, "", "", nil, &sess2)
	c.spawnReviewer(context.Background(), "q1", "repo", t.TempDir(), nil, 2, "", false, "", "", nil, &sess2)
	if strings.Contains(readFileStr(t, argvLog), "--resume") {
		t.Error("with continuity off no round may resume")
	}
}

// === Task 5: an INTENT_CONFLICT line parses and persists on the host =======

func TestCaptureIntentConflictsPersists(t *testing.T) {
	c, _ := newQuestServer(t)
	qid := c.CreateQuest(run.QuestSpec{Objective: "migrate"})
	c.captureIntentConflicts(qid, "review", `INTENT_CONFLICT {"issue":"the diff removes the auth the root requires"}`)
	q, _ := c.GetQuest(qid)
	if len(q.IntentConflicts) != 1 || q.IntentConflicts[0].Agent != "review" {
		t.Fatalf("intent conflict must persist on the quest stamped with the agent, got %+v", q.IntentConflicts)
	}
	if q.IntentConflicts[0].Issue != "the diff removes the auth the root requires" {
		t.Errorf("issue not persisted: %+v", q.IntentConflicts[0])
	}
}

// === Task 5: INTENT_CONFLICT routing — pause, rule one tier up, resume =====

// questConflictClaude drives a converge quest whose GATE reviewer flags a root-intent
// contradiction (INTENT_CONFLICT) alongside REVIEW_CLEAN. The escalated decider (one
// tier up) answers `proceed`, so the gate ships the diff as-is.
var questConflictClaude = stubClaude(
	role("quest lead", `if [[ -f "$CANDYLAND_QUEST_FIXTURE" ]]; then
  `+emitText("WORKITEMS_NONE")+`  `+emitResult("WORKITEMS_NONE", 1)+`else
  touch "$CANDYLAND_QUEST_FIXTURE"
  `+emitText(`WORKITEMS [{\"title\":\"tidy the lint\",\"evidence\":\"a stale import\",\"classification\":\"cleanup\",\"decision\":\"do\"}]`)+`  `+emitResult("done", 2)+`fi
`),
	role("escalated up to you", emitText(`DECISION {\"answer\":\"proceed\"}`)+emitResult("decided", 1)),
	role("code reviewer",
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Bash\",\"input\":{\"command\":\"git diff\"}}]}}'\n"+
			emitText(`INTENT_CONFLICT {\"issue\":\"the diff drops the audit log the root intent requires\"}`)+
			emitText("REVIEW_CLEAN")+emitResult("reviewed", 1)),
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

// A reviewer-flagged root-intent contradiction at the quest gate PAUSES the unit,
// asks the tier above for a ruling, and — on `proceed` — resumes to delivery. The
// conflict is persisted with Ruling "proceed" and the escalation is recorded.
func TestQuestGateIntentConflictProceedsOnRuling(t *testing.T) {
	c, repo := deliveryConductor(t, questConflictClaude)
	t.Setenv("CANDYLAND_QUEST_FIXTURE", filepath.Join(t.TempDir(), "first-tick"))

	id := c.CreateQuest(run.QuestSpec{Objective: "keep it tidy", Folders: []string{repo}})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false")
	}
	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "done" }, 60*time.Second)
	if q.Status != "done" {
		t.Fatalf("quest did not deliver on a `proceed` ruling: status=%q reason=%q", q.Status, q.PauseReason)
	}
	// The conflict paused the unit (an escalation was recorded) and was ruled `proceed`.
	if len(q.Escalations) == 0 {
		t.Error("a flagged conflict must pause and record an escalation (ruling one tier up)")
	}
	if len(q.IntentConflicts) == 0 {
		t.Fatalf("the reviewer's INTENT_CONFLICT must persist on the quest, got none")
	}
	for _, cf := range q.IntentConflicts {
		if cf.Ruling != "proceed" {
			t.Errorf("conflict ruling = %q, want proceed", cf.Ruling)
		}
	}
	// `proceed` ships the diff: a terminal PR opened.
	if len(q.PRs) != 1 || q.PRs[0].URL == "" {
		t.Errorf("a `proceed` ruling must deliver the PR, got %+v", q.PRs)
	}
}

// A `fix` ruling converts each contradiction into work and stamps Ruling "fix". This
// exercises routeQuestIntentConflicts directly (a full re-gate loop with an always-
// conflicting reviewer never converges; the routing decision is the unit under test).
func TestRouteQuestIntentConflictsFixRuling(t *testing.T) {
	c, repo := deliveryConductor(t, stubClaude(
		role("escalated up to you", emitText(`DECISION {\"answer\":\"fix it — reconcile the contradiction\"}`)+emitResult("decided", 1)),
		coder(emitResult("noop", 1)), // fallback so stubClaude emits a valid else branch
	))
	id := c.CreateQuest(run.QuestSpec{Objective: "migrate", Folders: []string{repo}})
	c.UpdateQuest(id, func(q *run.Quest) {
		q.IntentConflicts = append(q.IntentConflicts, run.IntentConflictNote{Agent: "review", Issue: "drops auth the root requires"})
	})
	proceed, issues := c.routeQuestIntentConflicts(context.Background(), id, "quest/"+id)
	if proceed {
		t.Fatal("a `fix` ruling must NOT proceed to delivery")
	}
	if len(issues) != 1 || issues[0] != "drops auth the root requires" {
		t.Fatalf("fix issues = %+v, want the one conflict issue", issues)
	}
	q, _ := c.GetQuest(id)
	if len(q.IntentConflicts) != 1 || q.IntentConflicts[0].Ruling != "fix" {
		t.Fatalf("conflict must be stamped Ruling=fix, got %+v", q.IntentConflicts)
	}
	if len(q.Escalations) == 0 {
		t.Error("routing must record the escalation (it paused and asked)")
	}
}

// --- helpers ---------------------------------------------------------------

func writeExec(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_CLAUDE", fake)
	t.Setenv("CANDYLAND_SESSION_REUSE", "0")
}

func readFileStr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func spineArgvLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}
