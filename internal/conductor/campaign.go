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

// Campaigns are the program-level container above quests and runs: Candyland owns
// the full intent→delivery cycle (validation, decomposition into child quests/runs,
// review, per-repo delivery). This file owns the campaign data model's persistence —
// Create/Get/Update/List against the same ooo storage runs and quests use, keyed
// campaigns/<id>. The supervisor/intent-lead flow, gate execution, and intent review
// are later phases; this is model + storage + CRUD only.

// publishCampaign writes a campaign object to ooo (key campaigns/<id>); subscribers
// update live. It mirrors publishQuest (the quest write path): nil-guarded for the
// serverless test conductor, and surfaces a dropped write since the server is Silenced.
func (c *Conductor) publishCampaign(cam run.Campaign) {
	if c.server == nil {
		return // tests construct a serverless conductor and read state via GetCampaign
	}
	b, err := json.Marshal(cam)
	if err != nil {
		log.Printf("candyland: marshal campaign %s: %v", cam.ID, err)
		return
	}
	if _, err := c.server.Storage.Set("campaigns/"+cam.ID, json.RawMessage(b)); err != nil {
		log.Printf("candyland: publish campaign %s: %v", cam.ID, err)
	}
}

// CreateCampaign registers a new campaign (status: running) and persists it,
// returning the minted id. It mirrors CreateQuest: it mints a sequential id,
// captures the launch input once onto OriginalInput (never rewritten — the campaign
// analogue of Run.OriginalIntent), stamps TraceVersion + timestamps, and defaults
// AutonomyLevel to L2 when empty — NEVER L1: a report-only campaign would strand
// with no PR (settled decision). The supervisor/intent-lead flow that drives the
// campaign is a later phase; CreateCampaign only seeds and persists initial state.
func (c *Conductor) CreateCampaign(spec run.CampaignSpec) string {
	c.mu.Lock()
	c.campaignSeq++
	id := fmt.Sprintf("c%d", c.campaignSeq)
	c.mu.Unlock()

	autonomy := spec.AutonomyLevel
	if autonomy == "" || autonomy == run.AutonomyReportOnly {
		// Campaigns default to L2 and are never L1 (a report-only campaign would
		// strand with no PR) — settled decision.
		autonomy = run.AutonomyGatePR
	}
	now := time.Now().UTC().Format(time.RFC3339)

	cam := run.Campaign{
		ID:            id,
		OriginalInput: spec.Input,
		Folders:       spec.Folders,
		Status:        "running",
		AutonomyLevel: autonomy,
		TokenBudget:   spec.TokenBudget,
		// Empty (non-nil) slices marshal to [] not null — the UI reads these as
		// arrays (.map/.length), matching how quests keep WorkItems/Ticks non-nil.
		QuestIDs:     []string{},
		RunIDs:       []string{},
		CreatedAt:    now,
		UpdatedAt:    now,
		TraceVersion: run.TraceVersion,
	}
	c.publishCampaign(cam)
	log.Printf("candyland: campaign %s created (autonomy %s, budget %d)", id, autonomy, spec.TokenBudget)
	return id
}

// GetCampaign returns a deep copy of the persisted campaign state. Campaigns have
// no in-memory runtime (the supervisor flow is a later phase), so the read goes
// straight to storage; the copy is deep so the caller can't mutate the slices the
// storage layer handed back.
func (c *Conductor) GetCampaign(id string) (run.Campaign, bool) {
	if c.server == nil {
		return run.Campaign{}, false
	}
	obj, err := c.server.Storage.Get("campaigns/" + id)
	if err != nil {
		return run.Campaign{}, false
	}
	var cam run.Campaign
	if err := json.Unmarshal(obj.Data, &cam); err != nil {
		return run.Campaign{}, false
	}
	return cloneCampaign(cam), true
}

// cloneCampaign deep-copies the slices a campaign carries so a returned campaign is
// safe to mutate without touching the next read's backing arrays.
func cloneCampaign(cam run.Campaign) run.Campaign {
	cam.Folders = append([]string(nil), cam.Folders...)
	cam.QuestIDs = append([]string(nil), cam.QuestIDs...)
	cam.RunIDs = append([]string(nil), cam.RunIDs...)
	cam.PRs = append([]run.PR(nil), cam.PRs...)
	cam.ReviewRouting = append([]string(nil), cam.ReviewRouting...)

	b := cam.IntentBrief
	b.ScopeByDomain = append([]string(nil), b.ScopeByDomain...)
	b.ResolvedQuestions = append([]string(nil), b.ResolvedQuestions...)
	b.OpenQuestions = append([]string(nil), b.OpenQuestions...)
	b.DraftTasks = append([]string(nil), b.DraftTasks...)
	b.Dependencies = append([]string(nil), b.Dependencies...)
	b.ReviewRouting = append([]string(nil), b.ReviewRouting...)
	b.Commitments = append([]run.Commitment(nil), b.Commitments...)
	cam.IntentBrief = b

	verdicts := make([]run.CommitmentVerdict, len(cam.IntentReview.Verdicts))
	for i, v := range cam.IntentReview.Verdicts {
		v.Evidence = append([]string(nil), v.Evidence...)
		verdicts[i] = v
	}
	cam.IntentReview.Verdicts = verdicts
	return cam
}

// UpdateCampaign applies a read-modify-write to a persisted campaign and
// re-publishes it, stamping UpdatedAt. It mirrors UpdateQuest: a single durable
// mutation primitive against storage directly (a campaign has no in-memory runtime
// yet). Returns false for an unknown campaign. The supervisor flow calls this to
// record brief/gate/delivery progress in a later phase; here it is the primitive.
func (c *Conductor) UpdateCampaign(id string, mutate func(*run.Campaign)) bool {
	if c.server == nil {
		return false
	}
	obj, err := c.server.Storage.Get("campaigns/" + id)
	if err != nil {
		return false
	}
	var cam run.Campaign
	if err := json.Unmarshal(obj.Data, &cam); err != nil {
		return false
	}
	mutate(&cam)
	cam.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	c.publishCampaign(cam)
	return true
}

// ListCampaigns returns every persisted campaign, deep-copied. It scans the
// campaigns/* keyspace, mirroring how ListQuests walks quests/*.
func (c *Conductor) ListCampaigns() []run.Campaign {
	if c.server == nil {
		return nil
	}
	keys, err := c.server.Storage.Keys()
	if err != nil {
		log.Printf("candyland: list campaigns: %v", err)
		return nil
	}
	var campaigns []run.Campaign
	for _, k := range keys {
		if !strings.HasPrefix(k, "campaigns/") {
			continue
		}
		obj, err := c.server.Storage.Get(k)
		if err != nil {
			continue
		}
		var cam run.Campaign
		if err := json.Unmarshal(obj.Data, &cam); err != nil {
			continue
		}
		campaigns = append(campaigns, cloneCampaign(cam))
	}
	return campaigns
}

// CampaignBranch derives a campaign's shared per-repo branch. A campaign delivers
// one PR per repo at the end (after intent review); its child quests/runs commit
// onto this shared per-repo branch and open no PR. The branch is DERIVED from the
// campaign id (campaign/<id>) — never a scalar branch name (settled decision, the
// same format QuestBranch derives for a campaign-owned quest). The repo argument is
// the impacted repo this branch is for; the format is repo-independent today (one
// branch name reused across repos), with the parameter present so a later phase can
// scope per-repo if needed without changing call sites. Returns "" for an unset id.
func CampaignBranch(cam run.Campaign, repo string) string {
	if cam.ID == "" {
		return ""
	}
	return "campaign/" + cam.ID
}

// reconcileCampaignSeq seeds the campaign-id sequence past the highest persisted id,
// so a post-restart CreateCampaign can't mint an id that overwrites an existing
// campaign. It mirrors reconcileQuestSeq. Safe to call alongside ReconcileOrphans
// after server.Start().
func (c *Conductor) reconcileCampaignSeq() {
	if c.server == nil {
		return
	}
	keys, err := c.server.Storage.Keys()
	if err != nil {
		return
	}
	maxSeq := 0
	for _, k := range keys {
		rest, ok := strings.CutPrefix(k, "campaigns/c")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(rest); err == nil && n > maxSeq {
			maxSeq = n
		}
	}
	c.mu.Lock()
	if maxSeq > c.campaignSeq {
		c.campaignSeq = maxSeq
	}
	c.mu.Unlock()
}
