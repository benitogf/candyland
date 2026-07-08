package conductor

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// Quests are the iterative-loop sibling of runs: a quest repeatedly discovers and
// triages work and launches child runs over many ticks. This file owns the quest
// data model's persistence — Create/Get/Update/List against the same ooo storage
// runs use, keyed quests/<id>. The tick loop, discover/triage/launch logic, and
// delivery wiring are later phases; this is model + storage + CRUD only.

// publishQuest writes a quest object to ooo (key quests/<id>); subscribers update
// live. It mirrors publish (the run write path): nil-guarded for the serverless
// test conductor, and surfaces a dropped write since the server is Silenced.
func (c *Conductor) publishQuest(q run.Quest) {
	if c.server == nil {
		return // tests construct a serverless conductor and read state via GetQuest
	}
	b, err := json.Marshal(q)
	if err != nil {
		log.Printf("candyland: marshal quest %s: %v", q.ID, err)
		return
	}
	if _, err := c.server.Storage.Set("quests/"+q.ID, json.RawMessage(b)); err != nil {
		log.Printf("candyland: publish quest %s: %v", q.ID, err)
	}
}

// CreateQuest registers a new quest (status: running) and persists it, returning
// the minted id. It mirrors Create: it mints a sequential id, captures the launch
// objective once onto OriginalObjective (never rewritten — the quest analogue of
// Run.OriginalIntent), stamps TraceVersion + timestamps, defaults Deliver to "pr",
// and carries the parent
// CampaignID from the spec. The tick loop that drives the quest is a later phase;
// CreateQuest only seeds and persists the initial state.
func (c *Conductor) CreateQuest(spec run.QuestSpec) string {
	c.mu.Lock()
	c.questSeq++
	id := fmt.Sprintf("q%d", c.questSeq)
	c.mu.Unlock()

	deliver := spec.Deliver
	if deliver == "" {
		deliver = run.DeliverPR
	}
	convergence := spec.Convergence
	if convergence == "" {
		convergence = run.ConvergeConverge // bounded quest by default
	}
	title := strings.TrimSpace(spec.Title)
	if title == "" {
		title = deriveTitle(spec.Objective)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	q := run.Quest{
		ID:                id,
		Title:             title,
		CampaignID:        spec.CampaignID,
		PartitionItemID:   spec.PartitionItemID,
		OriginalObjective: spec.Objective,
		Objective:         spec.Objective,
		Folders:           spec.Folders,
		Scope:             spec.Scope,
		Safety:            spec.Safety,
		Verify:            spec.Verify,
		Stop:              spec.Stop,
		Status:            "running",
		TokenBudget:       spec.TokenBudget,
		Deliver:           deliver,
		Convergence:       convergence,
		TargetPR:          spec.TargetPR,
		// Empty (non-nil) slices marshal to [] not null — the UI reads these as
		// arrays (.map/.length), matching how runs keep Agents/Tasks non-nil.
		WorkItems:    []run.WorkItem{},
		Ticks:        []run.Tick{},
		Agents:       []run.Agent{},
		CreatedAt:    now,
		UpdatedAt:    now,
		TraceVersion: run.TraceVersion,
	}
	c.publishQuest(q)
	log.Printf("candyland: quest %s created (campaign %q, deliver %s)", id, q.CampaignID, deliver)
	return id
}

// GetQuest returns a deep copy of the persisted quest state. Quests have no
// in-memory runtime (no executor goroutine yet — the tick loop is a later phase),
// so the read goes straight to storage; the copy is deep so the caller can't
// mutate the slices the storage layer handed back.
func (c *Conductor) GetQuest(id string) (run.Quest, bool) {
	if c.server == nil {
		return run.Quest{}, false
	}
	obj, err := c.server.Storage.Get("quests/" + id)
	if err != nil {
		return run.Quest{}, false
	}
	var q run.Quest
	if err := json.Unmarshal(obj.Data, &q); err != nil {
		return run.Quest{}, false
	}
	return cloneQuest(q), true
}

// cloneQuest deep-copies the slices a quest carries so a returned quest is safe to
// mutate without touching the next read's backing arrays.
func cloneQuest(q run.Quest) run.Quest {
	q.Folders = append([]string(nil), q.Folders...)
	q.Verify = append([]string(nil), q.Verify...)
	items := make([]run.WorkItem, len(q.WorkItems))
	copy(items, q.WorkItems)
	q.WorkItems = items
	ticks := make([]run.Tick, len(q.Ticks))
	for i, t := range q.Ticks {
		t.TriageDecisions = append([]string(nil), t.TriageDecisions...)
		t.LaunchedRunIDs = append([]string(nil), t.LaunchedRunIDs...)
		t.PRs = append([]run.PR(nil), t.PRs...)
		t.Blockers = append([]string(nil), t.Blockers...)
		ticks[i] = t
	}
	q.Ticks = ticks
	q.Agents = cloneAgents(q.Agents)
	return q
}

// UpdateQuest applies a read-modify-write to a persisted quest and re-publishes it,
// stamping UpdatedAt. It mirrors how runs are mutated through a single write path,
// but against storage directly (a quest has no in-memory runtime yet). Returns
// false for an unknown quest. The tick loop calls this to record discovery/triage
// progress in a later phase; here it is the durable mutation primitive.
func (c *Conductor) UpdateQuest(id string, mutate func(*run.Quest)) bool {
	if c.server == nil {
		return false
	}
	c.storeMu.Lock() // serialize Get→mutate→publish so concurrent updates can't lose writes
	defer c.storeMu.Unlock()
	obj, err := c.server.Storage.Get("quests/" + id)
	if err != nil {
		return false
	}
	var q run.Quest
	if err := json.Unmarshal(obj.Data, &q); err != nil {
		return false
	}
	mutate(&q)
	q.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	c.publishQuest(q)
	return true
}

// ListQuests returns every persisted quest, deep-copied. It scans the quests/*
// keyspace, mirroring how ReconcileOrphans walks runs/*.
func (c *Conductor) ListQuests() []run.Quest {
	if c.server == nil {
		return nil
	}
	keys, err := c.server.Storage.Keys()
	if err != nil {
		log.Printf("candyland: list quests: %v", err)
		return nil
	}
	var quests []run.Quest
	for _, k := range keys {
		if !strings.HasPrefix(k, "quests/") {
			continue
		}
		obj, err := c.server.Storage.Get(k)
		if err != nil {
			continue
		}
		var q run.Quest
		if err := json.Unmarshal(obj.Data, &q); err != nil {
			continue
		}
		quests = append(quests, cloneQuest(q))
	}
	return quests
}

// QuestBranch derives the shared branch a quest's child runs accumulate their
// commits on (the same name in each impacted repo — never a scalar branch on the
// spec). It is the single definition of the format, mirroring CampaignBranch:
//
//   - A feedback/review quest (target PR) works that PR's HEAD branch, not an owned
//     branch — convergence does not apply, so this returns "".
//   - A campaign-child quest (CampaignID set) integrates onto the CAMPAIGN branch
//     campaign/<campaignID> and opens no PR — regardless of its convergence policy.
//   - A standalone CONVERGE quest accumulates its child runs onto quest/<id> and
//     opens ONE PR per repo at terminal.
//   - A standalone perFinding quest (adventure) has no shared branch — each child
//     run opens its own PR — so this returns "".
func QuestBranch(q run.Quest) string {
	if isTargetedReviewQuest(q) {
		return "" // feedback/review works the target PR's head branch
	}
	if q.CampaignID != "" {
		return "campaign/" + q.CampaignID // campaign-child integrates onto the campaign branch
	}
	if q.Convergence == run.ConvergePerFinding {
		return "" // adventure: a PR per finding, no shared branch
	}
	return "quest/" + q.ID // standalone bounded quest: accumulate on quest/<id>
}

// reconcileQuestSeq seeds the quest-id sequence past the highest persisted id, so a
// post-restart CreateQuest can't mint an id that overwrites an existing quest. It
// mirrors the seq-seeding ReconcileOrphans does for runs. Safe to call alongside
// ReconcileOrphans after server.Start().
func (c *Conductor) reconcileQuestSeq() {
	if c.server == nil {
		return
	}
	keys, err := c.server.Storage.Keys()
	if err != nil {
		return
	}
	maxSeq := 0
	for _, k := range keys {
		rest, ok := strings.CutPrefix(k, "quests/q")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(rest); err == nil && n > maxSeq {
			maxSeq = n
		}
	}
	c.mu.Lock()
	if maxSeq > c.questSeq {
		c.questSeq = maxSeq
	}
	c.mu.Unlock()
}
