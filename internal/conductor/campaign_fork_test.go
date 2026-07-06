package conductor

import (
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// === Campaign stage agents fork doctrine templates ===========================
//
// campaignSpawn threads the session-template fork into the five campaign stage
// spawns: with a registry entry for (role, primary) the returned opts fork the
// template and the prompt is the slim bootstrap (no kb_get loads) with the full
// bootstrap as the in-attempt fallback; without one (kill switch, creation
// failure) the spawn degrades to today's behavior — the full bootstrap, cold.

// campaignStageSites is the five-stage table shared by the tests: one entry per
// campaignSpawn call site with its role and its slim/full bootstrap pair.
var campaignStageSites = []struct {
	name    string
	agentID string
	role    string
	slim    string
	full    string
	verdict string
}{
	{"intent-lead brief", intentLeadID, RoleIntentLead, intentLeadBootstrapSlim, intentLeadBootstrap, "`INTENT_BRIEF `"},
	{"tech-manager decompose", techManagerID, RoleTechManager, techManagerBootstrapSlim, techManagerBootstrap, "`QUESTS `"},
	{"intent-manager gate 1", intentManagerID, RoleIntentManager, partitionReviewBootstrapSlim, partitionReviewBootstrap, "`PARTITION_REVIEW `"},
	{"tech-manager gate 2", techManagerID, RoleTechManager, techDoneBootstrapSlim, techDoneBootstrap, "`TECH_DONE `"},
	{"intent-reviewer gate 2", intentReviewerID, RoleIntentReviewer, intentReviewerBootstrapSlim, intentReviewerBootstrap, "`INTENT_REVIEW `"},
}

// With a registry entry for (role, primary), campaignSpawn forks it: forkFrom is
// the template session, the prompt is the slim bootstrap, and the full bootstrap
// rides as the fallbackPrompt. The stage runs in primary itself, so no transcript
// copy is involved. The per-role model/thinking threading is unchanged.
func TestCampaignSpawnForksTemplate(t *testing.T) {
	c, repo, _ := templateConductor(t, templateStubClaude)
	camID := c.CreateCampaign(run.CampaignSpec{Input: "build the thing", Folders: []string{repo}})

	for _, tc := range campaignStageSites {
		t.Run(tc.name, func(t *testing.T) {
			seeded, ok := c.templateFor(tc.role, repo)
			if !ok {
				t.Fatal("seeding the template must succeed")
			}
			prompt, opts := c.campaignSpawn(camID, tc.agentID, tc.role, repo, tc.slim, tc.full)
			if opts.forkFrom != seeded {
				t.Errorf("forkFrom = %q, want the seeded template %q", opts.forkFrom, seeded)
			}
			if opts.fallbackPrompt != tc.full {
				t.Errorf("fallbackPrompt must be the FULL bootstrap, got %q", opts.fallbackPrompt)
			}
			if prompt != tc.slim {
				t.Errorf("a forked spawn must use the slim bootstrap, got %q", prompt)
			}
			wantModel, wantThinking := c.agentConfig(tc.role)
			if opts.model != wantModel || opts.thinking != wantThinking {
				t.Errorf("model/thinking = %q/%q, want %q/%q", opts.model, opts.thinking, wantModel, wantThinking)
			}
		})
	}

	// The L2 telemetry pre-seed is unchanged: every stage agent landed on the
	// campaign record with its role stamped.
	cam, ok := c.GetCampaign(camID)
	if !ok {
		t.Fatal("campaign not found")
	}
	for _, tc := range campaignStageSites {
		a := findAgent(cam.Agents, tc.agentID)
		if a == nil {
			t.Errorf("agent %q not pre-seeded on the campaign", tc.agentID)
			continue
		}
		if a.Role != tc.role {
			t.Errorf("agent %q role = %q, want %q", tc.agentID, a.Role, tc.role)
		}
	}
}

// The kill switch (CANDYLAND_SESSION_REUSE=0) degrades every stage to today's
// behavior: full bootstrap as the prompt, no fork args, no fallback.
func TestCampaignSpawnKillSwitchColdStart(t *testing.T) {
	c, repo, fixture := templateConductor(t, templateStubClaude)
	camID := c.CreateCampaign(run.CampaignSpec{Input: "build the thing", Folders: []string{repo}})
	t.Setenv("CANDYLAND_SESSION_REUSE", "0")

	for _, tc := range campaignStageSites {
		t.Run(tc.name, func(t *testing.T) {
			prompt, opts := c.campaignSpawn(camID, tc.agentID, tc.role, repo, tc.slim, tc.full)
			if opts.forkFrom != "" || opts.fallbackPrompt != "" {
				t.Errorf("kill switch off: forkFrom/fallbackPrompt = %q/%q, want empty", opts.forkFrom, opts.fallbackPrompt)
			}
			if prompt != tc.full {
				t.Errorf("a cold spawn must use the FULL bootstrap, got %q", prompt)
			}
		})
	}
	if got := spawnCount(t, fixture); got != 0 {
		t.Fatalf("the kill switch must never spawn a template creation, got %d", got)
	}
}

// No usable registry entry (template creation fails) degrades the same way:
// full bootstrap, no fork args — never a run failure.
func TestCampaignSpawnNoEntryColdStart(t *testing.T) {
	c, repo, _ := templateConductor(t, templateFailingClaude)
	camID := c.CreateCampaign(run.CampaignSpec{Input: "build the thing", Folders: []string{repo}})

	prompt, opts := c.campaignSpawn(camID, intentLeadID, RoleIntentLead, repo, intentLeadBootstrapSlim, intentLeadBootstrap)
	if opts.forkFrom != "" || opts.fallbackPrompt != "" {
		t.Errorf("no entry: forkFrom/fallbackPrompt = %q/%q, want empty", opts.forkFrom, opts.fallbackPrompt)
	}
	if prompt != intentLeadBootstrap {
		t.Errorf("a cold spawn must use the FULL bootstrap, got %q", prompt)
	}
}

// The slim bootstraps drop every kb_get instruction (the forked session already
// carries the doctrine) while preserving the rest of the contract: the brief_get
// instruction, the behavioral prefix before the load sentence, and — byte for
// byte — everything from "Then emit" onward (the verdict-line contract).
func TestSlimBootstrapsDropKbGetKeepContracts(t *testing.T) {
	for _, tc := range campaignStageSites {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(tc.slim, "kb_get") {
				t.Error("the slim bootstrap still instructs kb_get")
			}
			if !strings.Contains(tc.full, "kb_get") {
				t.Error("the full bootstrap (the fallback) lost its kb_get loads")
			}
			if !strings.Contains(tc.slim, "brief_get") {
				t.Error("the slim bootstrap must keep the brief_get instruction")
			}
			if !strings.Contains(tc.slim, "already loaded in this session") {
				t.Error("the slim bootstrap must point at the doctrine already loaded in this session")
			}

			// Behavioral identity before the load sentence: the slim starts with the
			// full bootstrap's prefix up to "Load and APPLY".
			li := strings.Index(tc.full, "Load and APPLY")
			if li < 0 {
				t.Fatal("the full bootstrap has no kb_get load sentence")
			}
			if !strings.HasPrefix(tc.slim, tc.full[:li]) {
				t.Error("the slim bootstrap diverges from the full one before the load sentence")
			}

			// The verdict contract: everything from "Then emit" onward is byte-identical.
			ei := strings.Index(tc.full, "Then emit")
			if ei < 0 {
				t.Fatal("the full bootstrap has no verdict contract")
			}
			contract := tc.full[ei:]
			if !strings.Contains(contract, tc.verdict) {
				t.Fatalf("the verdict contract does not carry the %s token", tc.verdict)
			}
			if !strings.HasSuffix(tc.slim, contract) {
				t.Errorf("the slim bootstrap's verdict contract differs from the full one:\nwant suffix %q", contract)
			}
		})
	}
}
