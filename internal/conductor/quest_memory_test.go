package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/candyland/internal/run"
)

// === Quest-lead template forking + decision memory in briefs =================
//
// The quest lead forks its doctrine template every tick (slim bootstrap, full
// bootstrap as the in-attempt fallback) and its tick brief carries the durable
// triage ledger of prior ticks; an escalation decider's brief carries the host
// record's prior decisions. Every rendering is a pure function pinned here, and
// the spawn seam is pinned against the stub claude — including the byte-for-byte
// cold path when no template is available.

// --- prior-ticks ledger (pure) ----------------------------------------------

func TestPriorTicksSectionEmpty(t *testing.T) {
	if got := priorTicksSection(run.Quest{}); got != "" {
		t.Fatalf("empty history must render nothing, got %q", got)
	}
	// A brief with no history is byte-identical to one without the section.
	brief := questBriefPrompt(run.Quest{Objective: "tidy"}, "t1")
	if strings.Contains(brief, "PRIOR TICKS") {
		t.Errorf("first-tick brief must carry no PRIOR TICKS section:\n%s", brief)
	}
	if !strings.HasSuffix(brief, "TICK: t1\n") {
		t.Errorf("first-tick brief must end exactly where it did before:\n%s", brief)
	}
}

// The ledger renders one line per recorded work item — title (recovered from
// the tick's triage-decision lines) → decision → outcome (child run id and the
// tick's recorded failure, when there was one).
func TestPriorTicksSectionRendersLedger(t *testing.T) {
	q := run.Quest{
		Ticks: []run.Tick{
			{ID: "t1",
				TriageDecisions: []string{"risky rename: skip", "fix the lint: do now", "flaky check: do now"},
				Blockers:        []string{"flaky check: tests timed out"}},
			{ID: "t2",
				TriageDecisions: []string{"flaky check: do now"},
				Blockers:        []string{`giving up on "flaky check" after 2 attempts`}},
			{ID: "t3",
				TriageDecisions: []string{"broken doc link: do now"},
				Blockers:        []string{"broken doc link: child run lost"}},
		},
		WorkItems: []run.WorkItem{
			{ID: "t1-s0", SourceTick: "t1", Decision: "skip", Disposition: "skipped"},
			{ID: "t1-w0", SourceTick: "t1", Decision: "do", Disposition: "completed", ChildRunID: "r7"},
			// t1's second accepted item ("flaky check") failed transiently — the tick
			// loop records no ledger entry until it durably resolves (t2's give-up).
			{ID: "t2-w0", SourceTick: "t2", Decision: "do", Disposition: "blocked"},
			{ID: "t3-w0", SourceTick: "t3", Decision: "do", Disposition: "blocked", ChildRunID: "r9"},
		},
	}
	want := "PRIOR TICKS (already triaged — do not re-surface, do not contradict):\n" +
		"- risky rename → skip → not launched\n" +
		"- fix the lint → do → done (run r7)\n" +
		"- flaky check → do → failed: giving up on \"flaky check\" after 2 attempts\n" +
		"- broken doc link → do → failed (run r9): child run lost\n"
	if got := priorTicksSection(q); got != want {
		t.Fatalf("ledger section:\n got %q\nwant %q", got, want)
	}
	// The brief appends the section after its existing content.
	brief := questBriefPrompt(q, "t4")
	if !strings.HasSuffix(brief, want) {
		t.Errorf("brief must end with the ledger section:\n%s", brief)
	}
	if !strings.Contains(brief, "TICK: t4\nPRIOR TICKS") {
		t.Errorf("the section must follow the existing brief content:\n%s", brief)
	}
}

// The section is bounded to the most recent maxPriorTickLines items, oldest
// dropped, accounted by a (+N earlier items) note.
func TestPriorTicksSectionBound(t *testing.T) {
	const total = maxPriorTickLines + 5
	var decs []string
	var items []run.WorkItem
	for i := range total {
		decs = append(decs, fmt.Sprintf("item-%02d: skip", i))
		items = append(items, run.WorkItem{
			ID: fmt.Sprintf("t1-s%d", i), SourceTick: "t1", Decision: "skip", Disposition: "skipped",
		})
	}
	got := priorTicksSection(run.Quest{Ticks: []run.Tick{{ID: "t1", TriageDecisions: decs}}, WorkItems: items})

	if !strings.Contains(got, "(+5 earlier items)\n") {
		t.Errorf("dropped items must be accounted, got:\n%s", got)
	}
	if strings.Contains(got, "item-04 →") {
		t.Errorf("the OLDEST items must be dropped, got:\n%s", got)
	}
	if !strings.Contains(got, "- item-05 → skip → not launched\n") {
		t.Errorf("the oldest KEPT item must be item-05, got:\n%s", got)
	}
	if n := strings.Count(got, "\n- ") + boolCount(strings.HasPrefix(got, "- ")); n != maxPriorTickLines {
		t.Errorf("want exactly %d item lines, got %d:\n%s", maxPriorTickLines, n, got)
	}
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Title recovery falls back honestly when the id/decision derivation misses:
// evidence, then classification, then the ledger id.
func TestWorkItemTitleFallbacks(t *testing.T) {
	tick := run.Tick{ID: "t1", TriageDecisions: []string{"real title: do now"}}
	cases := []struct {
		name string
		w    run.WorkItem
		want string
	}{
		{"derived from triage line", run.WorkItem{ID: "t1-w0", SourceTick: "t1"}, "real title"},
		{"foreign id → evidence", run.WorkItem{ID: "weird", SourceTick: "t1", Evidence: "stale import"}, "stale import"},
		{"no evidence → classification", run.WorkItem{ID: "weird", SourceTick: "t1", Classification: "cleanup"}, "cleanup"},
		{"nothing → id", run.WorkItem{ID: "t1-w5", SourceTick: "t1"}, "t1-w5"}, // index beyond the recorded lines
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workItemTitle(tc.w, tick); got != tc.want {
				t.Fatalf("workItemTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- prior decisions in the escalation brief (pure) --------------------------

// With no prior escalations the decider's brief is byte-for-byte the
// question-only form it has always been.
func TestEscalationBriefPromptNoPriors(t *testing.T) {
	want := "ESCALATED DECISION (decide autonomously; never ask a human, never stop the flow):\npick a path"
	if got := escalationBriefPrompt("pick a path", nil); got != want {
		t.Fatalf("no-priors brief changed:\n got %q\nwant %q", got, want)
	}
}

func TestEscalationBriefPromptRendersPriors(t *testing.T) {
	prior := []run.Escalation{
		{Question: "keep or drop the flag", Answer: "keep it"},
		{Question: "split the module", Answer: "no — one unit"},
	}
	got := escalationBriefPrompt("decide the next step", prior)
	for _, want := range []string{
		"decide the next step",
		"PRIOR DECISIONS on this record (do not contradict without stating why):\n",
		"- keep or drop the flag → keep it\n",
		"- split the module → no — one unit\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q:\n%s", want, got)
		}
	}
}

// Bounded to the most recent maxPriorDecisionLines, oldest dropped with a note.
func TestEscalationBriefPromptBound(t *testing.T) {
	var prior []run.Escalation
	for i := range maxPriorDecisionLines + 2 {
		prior = append(prior, run.Escalation{
			Question: fmt.Sprintf("q%02d", i), Answer: fmt.Sprintf("a%02d", i),
		})
	}
	got := escalationBriefPrompt("decide", prior)
	if !strings.Contains(got, "(+2 earlier decisions)\n") {
		t.Errorf("dropped decisions must be accounted:\n%s", got)
	}
	if strings.Contains(got, "q01 →") {
		t.Errorf("the oldest decisions must be dropped:\n%s", got)
	}
	if !strings.Contains(got, "- q02 → a02\n") || !strings.Contains(got, "- q11 → a11\n") {
		t.Errorf("the most recent %d must be kept:\n%s", maxPriorDecisionLines, got)
	}
	if n := strings.Count(got, "\n- "); n != maxPriorDecisionLines {
		t.Errorf("want exactly %d decision lines, got %d:\n%s", maxPriorDecisionLines, n, got)
	}
}

// The decider's REAL brief (written to the bus before the spawn) carries the
// host record's prior decisions, fetched by the same id-kind switch that
// recorded them.
func TestEscalationBriefCarriesPriorDecisionsOnBus(t *testing.T) {
	c, repo := deliveryConductor(t, decisionClaude)
	qid := c.CreateQuest(run.QuestSpec{Objective: "tidy", Folders: []string{repo}})
	c.UpdateQuest(qid, func(q *run.Quest) {
		q.Escalations = append(q.Escalations,
			run.Escalation{Question: "keep or drop the flag", Answer: "keep it"},
			run.Escalation{Question: "split the module", Answer: "no — one unit"},
		)
	})
	q, _ := c.GetQuest(qid)

	if _, resolved := c.escalateQuestDecision(t.Context(), q, "discovery stuck — decide", repo, nil); !resolved {
		t.Fatal("the stub decider emits a DECISION — the escalation must resolve")
	}

	obj, err := c.server.Storage.Get(bus.BriefKey(questLeadID))
	if err != nil {
		t.Fatalf("decider brief not written: %v", err)
	}
	var br bus.Brief
	if err := json.Unmarshal(obj.Data, &br); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"discovery stuck — decide",
		"PRIOR DECISIONS on this record (do not contradict without stating why):",
		"- keep or drop the flag → keep it",
		"- split the module → no — one unit",
	} {
		if !strings.Contains(br.Prompt, want) {
			t.Errorf("decider brief missing %q:\n%s", want, br.Prompt)
		}
	}
	// The new resolution is recorded ON TOP of the priors, never replacing them.
	if got, _ := c.GetQuest(qid); len(got.Escalations) != 3 {
		t.Errorf("want 3 recorded escalations (2 prior + 1 new), got %d", len(got.Escalations))
	}
}

// --- quest-lead template forking (spawn seam) --------------------------------

// wantColdQuestLeadBootstrap pins TODAY'S full quest-lead bootstrap as a
// literal: the fork refactor split the constant but must not change a byte of
// it, and the no-template spawn must carry exactly this prompt.
const wantColdQuestLeadBootstrap = "You are the quest lead driving one tick of an iterative work loop. " +
	"Call the brief_get tool FIRST to read the quest's objective, scope, safety boundary, and verification — it is no longer on your command line." + briefGetToolHint + ". " +
	"Load and APPLY the detritus doctrine via the kb_get tool: kb_get name=\"core/loop\" (loop fundamentals: cadence, skip-streak, durability), " +
	"kb_get name=\"core/todo-audit\" (how to discover, prioritize, and fork-gate work items), and kb_get name=\"core/completion\" (the three dispositions and the definition of done). " +
	"Do NOT improvise your own rubric — use the doctrine you loaded. " +
	"Discover the next safe, in-scope work item(s): if the brief names a TARGET PR, that PR IS the subject — you MUST actually fetch and read it (its diff and review comments, e.g. `gh pr diff <n>` / `gh pr view <n>`) and base every finding on what you read; otherwise explore the folder for concrete work. Then TRIAGE each (is it safe? in scope? a single self-contained change?). " +
	"Never emit WORKITEMS_NONE for a TARGET PR without having read that PR first. " +
	"Before your verdict, emit EXACTLY ONE line `STATE ` followed by a JSON object " +
	`{"orientation":"one or two lines — the active focus","learned":"context worth carrying to the next tick (repo facts established, dead ends, open threads)","nextTick":"concrete first move for the next tick"}` +
	" — this is your cross-tick state block: the next tick's brief replays it back to you so you resume from it instead of re-deriving your bearings cold. " +
	"Then emit EXACTLY ONE verdict line and stop: either `WORKITEMS_NONE` (no safe in-scope work remains this tick) " +
	"OR `WORKITEMS ` followed by a JSON array " + `[{"title":"…","evidence":"why it's needed","classification":"category","decision":"do|skip|block"}]` +
	" listing only items you triaged as safe and in scope (decision \"do\"); use \"skip\"/\"block\" for items you surfaced but will not act on. " +
	"A missing prerequisite you do not own — an instrument, artifact, or change another unit is responsible for — is recorded as a BLOCKER (decision \"block\", its evidence naming the owning unit); NEVER re-derive it as this quest's own work item (that duplicates another unit's work). " +
	"Do not ask questions and do not defer." +
	incidentDoctrine

func TestQuestLeadBootstrapUnchangedBySplit(t *testing.T) {
	if questLeadBootstrap != wantColdQuestLeadBootstrap {
		t.Fatalf("questLeadBootstrap changed:\n got %q\nwant %q", questLeadBootstrap, wantColdQuestLeadBootstrap)
	}
	// The fork variant drops ONLY the kb_get doctrine loads and re-points the
	// rubric line at the already-loaded session; everything else is shared.
	if strings.Contains(questLeadForkBootstrap, "kb_get") {
		t.Error("the fork bootstrap must not instruct kb_get loads")
	}
	if !strings.Contains(questLeadForkBootstrap, "apply the doctrine already loaded in this session") {
		t.Error("the fork bootstrap must point at the doctrine loaded in the template session")
	}
	for _, shared := range []string{questLeadIntro, questLeadDiscoverRules} {
		if !strings.Contains(questLeadForkBootstrap, shared) {
			t.Error("the fork bootstrap must keep the shared bootstrap text verbatim")
		}
	}
}

// questForkStub speaks all three contracts a forking quest-lead tick uses: the
// --version probe, a template-creation spawn (echo the minted --session-id,
// reply READY), and the quest-lead spawn itself — whose argv and exact prompt
// it records for assertion.
const questForkStub = `#!/usr/bin/env bash
if [[ "$1" == "--version" ]]; then echo "9.9.9 (stub)"; exit 0; fi
prompt="$2"
if [[ "$prompt" == *"reusable session template"* ]]; then
  echo spawn >> "$CANDYLAND_TEMPLATE_FIXTURE"
  sid=""
  prev=""
  for a in "$@"; do
    if [[ "$prev" == "--session-id" ]]; then sid="$a"; fi
    prev="$a"
  done
  dir="$CANDYLAND_CLAUDE_PROJECTS_DIR/$(pwd | sed 's/[^a-zA-Z0-9]/-/g')"
  mkdir -p "$dir"
  echo '{"doctrine":true}' > "$dir/$sid.jsonl"
  echo "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$sid\"}"
  echo '{"type":"result","subtype":"success","result":"READY","usage":{"output_tokens":1}}'
  exit 0
fi
echo "$@" >> "$CANDYLAND_QUEST_LEAD_FIXTURE.args"
printf '%s' "$prompt" > "$CANDYLAND_QUEST_LEAD_FIXTURE.prompt"
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"WORKITEMS_NONE"}]}}'
echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
`

// questForkConductor wires the fork-aware stub plus the template fixtures.
// Session reuse stays at writeFakeClaude's default OFF; the fork test turns it
// on itself.
func questForkConductor(t *testing.T) (*Conductor, string, string) {
	t.Helper()
	c, repo := deliveryConductor(t, questForkStub)
	writeFakeDetritus(t)
	t.Setenv("CANDYLAND_TEMPLATE_FIXTURE", filepath.Join(t.TempDir(), "template"))
	t.Setenv("CANDYLAND_CLAUDE_PROJECTS_DIR", t.TempDir())
	fixture := filepath.Join(t.TempDir(), "quest-lead")
	t.Setenv("CANDYLAND_QUEST_LEAD_FIXTURE", fixture)
	return c, repo, fixture
}

// With a template available the quest-lead spawn FORKS it (--resume <template>
// --fork-session) and carries the slim bootstrap; the template was created by
// exactly one creation spawn.
func TestQuestLeadForksTemplate(t *testing.T) {
	c, repo, fixture := questForkConductor(t)
	t.Setenv("CANDYLAND_SESSION_REUSE", "1")
	qid := c.CreateQuest(run.QuestSpec{Objective: "tidy", Folders: []string{repo}})

	out := c.streamQuestLead(context.Background(), qid, repo, nil)
	if out.startErr != nil || out.stalled {
		t.Fatalf("quest lead did not run cleanly: startErr=%v stalled=%v", out.startErr, out.stalled)
	}
	if !strings.Contains(out.text, "WORKITEMS_NONE") {
		t.Fatalf("quest lead output lost: %q", out.text)
	}

	// The persisted template entry names the session the spawn must fork.
	obj, err := c.server.Storage.Get(templateKey(RoleQuestLead, repo))
	if err != nil {
		t.Fatalf("quest-lead template not created: %v", err)
	}
	var e sessionTemplate
	if err := json.Unmarshal(obj.Data, &e); err != nil {
		t.Fatal(err)
	}
	if got := spawnCount(t, os.Getenv("CANDYLAND_TEMPLATE_FIXTURE")); got != 1 {
		t.Errorf("want exactly 1 template-creation spawn, got %d", got)
	}

	argv := invocationArgs(t, fixture)
	if len(argv) != 1 {
		t.Fatalf("want exactly 1 quest-lead spawn, got %d: %v", len(argv), argv)
	}
	if !strings.Contains(argv[0], "--resume "+e.SessionID+" --fork-session") {
		t.Errorf("quest-lead argv must fork the template session %q, argv was %q", e.SessionID, argv[0])
	}
	prompt, err := os.ReadFile(fixture + ".prompt")
	if err != nil {
		t.Fatal(err)
	}
	if string(prompt) != questLeadForkBootstrap {
		t.Errorf("fork spawn prompt must be the slim bootstrap:\n got %q\nwant %q", prompt, questLeadForkBootstrap)
	}

	// Tick ≥2 — the acceptance criterion's letter: a later tick's fresh spawn
	// forks the SAME template (cached, no second creation) with the slim
	// bootstrap. This is the exact spot the deleted cross-tick doctrineLoaded
	// state used to diverge.
	out = c.streamQuestLead(context.Background(), qid, repo, nil)
	if out.startErr != nil || out.stalled {
		t.Fatalf("tick-2 quest lead did not run cleanly: startErr=%v stalled=%v", out.startErr, out.stalled)
	}
	if got := spawnCount(t, os.Getenv("CANDYLAND_TEMPLATE_FIXTURE")); got != 1 {
		t.Errorf("tick 2 must reuse the cached template, want 1 creation total, got %d", got)
	}
	argv = invocationArgs(t, fixture)
	if len(argv) != 2 {
		t.Fatalf("want exactly 2 quest-lead spawns after two ticks, got %d: %v", len(argv), argv)
	}
	if !strings.Contains(argv[1], "--resume "+e.SessionID+" --fork-session") {
		t.Errorf("tick-2 argv must fork the same template session %q, argv was %q", e.SessionID, argv[1])
	}
	prompt, err = os.ReadFile(fixture + ".prompt")
	if err != nil {
		t.Fatal(err)
	}
	if string(prompt) != questLeadForkBootstrap {
		t.Errorf("tick-2 fork spawn prompt must be the slim bootstrap:\n got %q\nwant %q", prompt, questLeadForkBootstrap)
	}
}

// With the kill switch off the tick is byte-for-byte today's: no template
// spawn, no fork args, and the full bootstrap on argv exactly as pinned.
func TestQuestLeadColdWhenReuseDisabled(t *testing.T) {
	c, repo, fixture := questForkConductor(t) // reuse stays OFF (harness default)
	qid := c.CreateQuest(run.QuestSpec{Objective: "tidy", Folders: []string{repo}})

	out := c.streamQuestLead(context.Background(), qid, repo, nil)
	if out.startErr != nil || out.stalled {
		t.Fatalf("quest lead did not run cleanly: startErr=%v stalled=%v", out.startErr, out.stalled)
	}

	if got := spawnCount(t, os.Getenv("CANDYLAND_TEMPLATE_FIXTURE")); got != 0 {
		t.Errorf("kill switch off: want 0 template spawns, got %d", got)
	}
	argv := invocationArgs(t, fixture)
	if len(argv) != 1 {
		t.Fatalf("want exactly 1 quest-lead spawn, got %d: %v", len(argv), argv)
	}
	if strings.Contains(argv[0], "--resume") || strings.Contains(argv[0], "--fork-session") {
		t.Errorf("cold spawn must carry no fork args, argv was %q", argv[0])
	}
	prompt, err := os.ReadFile(fixture + ".prompt")
	if err != nil {
		t.Fatal(err)
	}
	if string(prompt) != wantColdQuestLeadBootstrap {
		t.Errorf("cold spawn prompt must be today's full bootstrap byte-for-byte:\n got %q\nwant %q", prompt, wantColdQuestLeadBootstrap)
	}
}
