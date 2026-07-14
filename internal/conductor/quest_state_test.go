package conductor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// parseLeadState is the quest lead's cross-tick STATE block convention (the
// core/loop State-block homologue): a `STATE <json>` line the conductor persists
// on the quest record and replays into the next tick's brief. Pin the contract the
// stub and a real quest lead share, exactly as TestParseWorkItems pins WORKITEMS.
func TestParseLeadState(t *testing.T) {
	// valid line → parsed.
	st, ok := parseLeadState(`some preamble
STATE {"orientation":"o","learned":"l","nextTick":"n"}
WORKITEMS_NONE`)
	if !ok {
		t.Fatal("a valid STATE line must parse")
	}
	if st.Orientation != "o" || st.Learned != "l" || st.NextTick != "n" {
		t.Fatalf("fields not parsed: %+v", st)
	}

	// missing line → false.
	if _, ok := parseLeadState("no state here\nWORKITEMS_NONE"); ok {
		t.Error("text with no STATE line must report ok=false")
	}

	// invalid JSON → false (tolerant: never fails a tick).
	if _, ok := parseLeadState(`STATE {not valid json`); ok {
		t.Error("an invalid STATE JSON must report ok=false, not a parse")
	}

	// last STATE line wins.
	st, ok = parseLeadState(`STATE {"orientation":"first"}
STATE {"orientation":"second"}`)
	if !ok || st.Orientation != "second" {
		t.Fatalf("last STATE line must win, got %+v (ok=%v)", st, ok)
	}
}

// The 4000-char cap truncates learned first, then nextTick, then orientation —
// suffixing "…" on any field it cuts, so a runaway state block stays a brief.
func TestParseLeadStateTruncates(t *testing.T) {
	big := func(n int) string { return strings.Repeat("x", n) }

	// Only learned overflows → learned truncated, others untouched.
	st, ok := parseLeadState(`STATE {"orientation":"orient","learned":"` + big(5000) + `","nextTick":"next"}`)
	if !ok {
		t.Fatal("must parse")
	}
	if total := len(st.Orientation) + len(st.Learned) + len(st.NextTick); total > leadStateCap {
		t.Fatalf("capped total = %d, want ≤ %d", total, leadStateCap)
	}
	if st.Orientation != "orient" || st.NextTick != "next" {
		t.Errorf("only learned should truncate first, got orientation=%q nextTick=%q", st.Orientation, st.NextTick)
	}
	if !strings.HasSuffix(st.Learned, "…") {
		t.Error("a truncated field must be suffixed with …")
	}

	// learned fully consumed AND nextTick overflows → nextTick truncated too,
	// orientation still intact.
	st, ok = parseLeadState(`STATE {"orientation":"keep","learned":"` + big(3000) + `","nextTick":"` + big(3000) + `"}`)
	if !ok {
		t.Fatal("must parse")
	}
	if total := len(st.Orientation) + len(st.Learned) + len(st.NextTick); total > leadStateCap {
		t.Fatalf("capped total = %d, want ≤ %d", total, leadStateCap)
	}
	if st.Orientation != "keep" {
		t.Errorf("orientation is truncated LAST — must survive here, got %q", st.Orientation)
	}

	// orientation alone overflows → orientation truncated last-resort.
	st, ok = parseLeadState(`STATE {"orientation":"` + big(5000) + `","learned":"","nextTick":""}`)
	if !ok {
		t.Fatal("must parse")
	}
	if len(st.Orientation) > leadStateCap {
		t.Fatalf("orientation not capped, len=%d", len(st.Orientation))
	}
	if !strings.HasSuffix(st.Orientation, "…") {
		t.Error("a truncated orientation must be suffixed with …")
	}
}

// leadStateSection renders the lead's state block into the next tick's brief, and
// is empty (byte-identical to today) when no block has been recorded — the same
// empty-case contract priorTicksSection follows.
func TestQuestBriefRendersLeadState(t *testing.T) {
	base := run.Quest{Objective: "keep it tidy"}
	without := questBriefPrompt(base, "t2")
	if strings.Contains(without, "YOUR STATE BLOCK") {
		t.Fatal("a nil LeadState must render no state section")
	}

	q := base
	q.LeadState = &run.QuestLeadState{
		Orientation: "auditing the lint",
		Learned:     "pkg foo already clean",
		NextTick:    "sweep pkg bar",
		SourceTick:  "t1",
	}
	with := questBriefPrompt(q, "t2")
	want := "YOUR STATE BLOCK (you wrote this at the end of tick t1 — resume from it, do not re-derive):\n" +
		"ORIENTATION: auditing the lint\n" +
		"LEARNED: pkg foo already clean\n" +
		"NEXT-TICK PLAN: sweep pkg bar\n"
	if !strings.Contains(with, want) {
		t.Fatalf("brief missing the state section verbatim:\n%s", with)
	}

	// With the section removed, the brief must be byte-identical to the nil brief —
	// the section is purely additive.
	if got := strings.Replace(with, want, "", 1); got != without {
		t.Fatalf("brief with LeadState is not the nil brief plus the section:\n got %q\nwant %q", got, without)
	}
}

// persistLeadState overwrites the quest's one mutable cross-tick section when a
// tick carries a STATE line, and leaves the previous block in place when it does
// not — never an error path (the never-fail-a-tick contract).
func TestPersistLeadStateOverwriteSemantics(t *testing.T) {
	c, _ := newQuestServer(t)
	id := c.CreateQuest(run.QuestSpec{Objective: "x", Folders: []string{"/repo"}})

	// A tick with a STATE line persists it, stamped with the source tick + time.
	c.persistLeadState(id, "t1", `STATE {"orientation":"o1","learned":"l1","nextTick":"n1"}`)
	q, _ := c.GetQuest(id)
	if q.LeadState == nil || q.LeadState.Orientation != "o1" {
		t.Fatalf("first STATE not persisted: %+v", q.LeadState)
	}
	if q.LeadState.SourceTick != "t1" || q.LeadState.UpdatedAt == "" {
		t.Errorf("persisted state must be stamped with source tick + time, got %+v", q.LeadState)
	}

	// A tick WITHOUT a STATE line keeps the previous block (no overwrite, no error).
	c.persistLeadState(id, "t2", "no state line this tick\nWORKITEMS_NONE")
	q, _ = c.GetQuest(id)
	if q.LeadState == nil || q.LeadState.Orientation != "o1" || q.LeadState.SourceTick != "t1" {
		t.Fatalf("a stateless tick must keep the previous block, got %+v", q.LeadState)
	}

	// A later tick with a STATE line overwrites it (the one mutable section).
	c.persistLeadState(id, "t3", `STATE {"orientation":"o3","learned":"l3","nextTick":"n3"}`)
	q, _ = c.GetQuest(id)
	if q.LeadState == nil || q.LeadState.Orientation != "o3" || q.LeadState.SourceTick != "t3" {
		t.Fatalf("a later STATE line must overwrite: %+v", q.LeadState)
	}
}

// questUnboundedClaude drives a quest whose lead surfaces one accepted item for the
// first 25 ticks (tracked via a counter file) then WORKITEMS_NONE — proving the
// loop runs past the OLD tick bound of 20 and still terminates naturally (unbounded
// ≠ infinite). Each tick also emits a STATE line the conductor persists.
var questUnboundedClaude = stubClaude(
	role("quest lead", `n=$(cat "$CANDYLAND_QUEST_COUNTER" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$CANDYLAND_QUEST_COUNTER"
`+emitText(`STATE {\"orientation\":\"tick $n\",\"learned\":\"l\",\"nextTick\":\"n\"}`)+`if [ "$n" -le 25 ]; then
  `+emitText(`WORKITEMS [{\"title\":\"tidy\",\"evidence\":\"e\",\"classification\":\"cleanup\",\"decision\":\"do\"}]`)+`  `+emitResult("done", 1)+`else
  `+emitText("WORKITEMS_NONE")+`  `+emitResult("none", 1)+`fi
`),
	roleCleanReviewer,
	role("tech lead", emitPartition(`[{"id":"a","title":"do the item","files":["a.txt"],"test":"t"}]`)),
	coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),
)

// The loop is UNBOUNDED (janitor parity): a quest that keeps surfacing work for 25
// ticks — exceeding the old cap of 20 — runs all 26 ticks and finishes naturally on
// WORKITEMS_NONE. Proves the tick bound is gone AND that unbounded still terminates.
// The lead's STATE block is persisted along the way (rich observability).
func TestQuestLoopIsUnbounded(t *testing.T) {
	c, repo := deliveryConductor(t, questUnboundedClaude)
	t.Setenv("CANDYLAND_QUEST_COUNTER", filepath.Join(t.TempDir(), "counter"))
	t.Setenv("CANDYLAND_QUEST_ITEM_ATTEMPTS", "100") // the static title must not trip the per-item thrash cap

	id := c.CreateQuest(run.QuestSpec{
		Objective:   "keep it tidy forever",
		Folders:     []string{repo},
		Convergence: run.ConvergePerFinding, // no shared branch → no dedup collapsing the static title
	})
	if !c.BeginQuest(id) {
		t.Fatal("BeginQuest returned false for a fresh quest")
	}

	q := waitForQuest(t, c, id, func(q run.Quest) bool { return q.Status == "done" }, 120*time.Second)
	if q.Status != "done" {
		t.Fatalf("quest did not finish: status=%q reason=%q ticks=%d", q.Status, q.PauseReason, len(q.Ticks))
	}
	if len(q.Ticks) != 26 {
		t.Fatalf("unbounded loop must run 25 work ticks + 1 WORKITEMS_NONE = 26 ticks, got %d", len(q.Ticks))
	}
	if q.LeadState == nil || !strings.HasPrefix(q.LeadState.Orientation, "tick ") {
		t.Errorf("the lead's STATE block must be persisted, got %+v", q.LeadState)
	}
}
