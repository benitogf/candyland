package conductor

import (
	"encoding/json"
	"fmt"
)

// settings.go owns the per-role model + effort ("thinking") configuration the
// conductor threads into every agent spawn. It is a single ooo object under the
// key "settings"; the frontend renders and POSTs the EXACT JSON shape in §9 of the
// contract, and the conductor reads it FRESH per spawn (no cache) so a settings
// change takes effect on the next agent without a restart.
//
// Role keys map to the agent identities (contract §9):
//   coder → t.ID coder spawns · fix → the fix-pass (reviewerID role "fix") ·
//   tech-lead → "tl" · reviewer → reviewerID role "reviewer" · quest-lead ·
//   intent-lead · tech-manager · intent-manager · intent-reviewer.

// Role keys — the settings level names. Kept as constants so the spawn sites and
// the default table can't drift on a spelling.
const (
	RoleCoder          = "coder"
	RoleFix            = "fix"
	RoleTechLead       = "tech-lead"
	RoleReviewer       = "reviewer"
	RoleQuestLead      = "quest-lead"
	RoleIntentLead     = "intent-lead"
	RoleTechManager    = "tech-manager"
	RoleIntentManager  = "intent-manager"
	RoleIntentReviewer = "intent-reviewer"
)

// settingsKey is the single ooo object holding the whole Settings.
const settingsKey = "settings"

// defaultModel is the fallback model for spawns with no explicit selection
// (resilience.go claudeArgs) and the default for every building/coordinating
// role.
const defaultModel = "claude-opus-4-8"

// reviewDefaultModel is the default for the independent reviewing roles.
// Evidence (2026-07-08, r117–r120): opus-high in-loop reviewers returned 1–2
// round REVIEW_CLEANs on work that fable reviews then found materially
// defective — independent review defaults to the model that catches, not the
// builder's own.
const reviewDefaultModel = "claude-fable-5"

// defaultModelFor is the per-role default model (§9 Defaults): reviewer,
// intent-reviewer, and intent-manager (the gate-1 judge) review independently
// and default to fable-5; every building/coordinating role stays on opus-4-8.
// The tech manager's gate-2 done-verdict is a self-assessment cross-checked by
// the intent reviewer, so it keeps the builder default.
func defaultModelFor(role string) string {
	switch role {
	case RoleReviewer, RoleIntentReviewer, RoleIntentManager:
		return reviewDefaultModel
	}
	return defaultModel
}

// LevelConfig is one role's model + thinking (effort) selection.
type LevelConfig struct {
	Model    string `json:"model"`
	Thinking string `json:"thinking"` // low | medium | high (mapped to claude --effort)
}

// Settings is the whole per-role configuration object (contract §9 JSON shape).
type Settings struct {
	Levels map[string]LevelConfig `json:"levels"`
}

// modelOptions is the curated select of models the UI offers and the API accepts.
// It is the single source of truth for the model list: limit.go's buildModelTokenRe
// derives the model-scoped-limit allowlist from these ids, so adding a family here
// automatically teaches the usage-limit classifier about it.
var modelOptions = map[string]bool{
	"claude-opus-4-8":           true,
	"claude-fable-5":            true,
	"claude-sonnet-5":           true,
	"claude-haiku-4-5-20251001": true,
}

// thinkingOptions is the curated select of effort levels (contract §9).
var thinkingOptions = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// allRoles is every configurable level, in the contract's order.
var allRoles = []string{
	RoleCoder, RoleFix, RoleTechLead, RoleReviewer, RoleQuestLead,
	RoleIntentLead, RoleTechManager, RoleIntentManager, RoleIntentReviewer,
}

// defaultThinking is the per-role default effort (contract §9 Defaults table):
// low for coder+fix, high for every other role.
func defaultThinking(role string) string {
	if role == RoleCoder || role == RoleFix {
		return "low"
	}
	return "high"
}

// defaultLevel is one role's default LevelConfig (all models opus-4-8; thinking
// low for coder+fix, high otherwise). An unknown role also defaults to high — the
// safe (higher-effort) side for a coordinating identity.
func defaultLevel(role string) LevelConfig {
	return LevelConfig{Model: defaultModelFor(role), Thinking: defaultThinking(role)}
}

// DefaultSettings is the full default table (contract §9). Reset-to-defaults
// restores exactly this; a missing key/level falls back to it per role.
func DefaultSettings() Settings {
	levels := make(map[string]LevelConfig, len(allRoles))
	for _, role := range allRoles {
		levels[role] = defaultLevel(role)
	}
	return Settings{Levels: levels}
}

// loadSettings reads the persisted settings FRESH (no cache) and overlays them
// onto the default table, so a partial object (a level or field absent) still
// yields a complete, valid config. A serverless conductor or an absent/unreadable
// key returns the pure defaults.
func (c *Conductor) loadSettings() Settings {
	s := DefaultSettings()
	if c.server == nil {
		return s
	}
	obj, err := c.server.Storage.Get(settingsKey)
	if err != nil {
		return s
	}
	var loaded Settings
	if json.Unmarshal(obj.Data, &loaded) != nil {
		return s
	}
	for role, lc := range loaded.Levels {
		merged := s.Levels[role]
		if merged.Model == "" {
			merged = defaultLevel(role)
		}
		if lc.Model != "" {
			merged.Model = lc.Model
		}
		if lc.Thinking != "" {
			merged.Thinking = lc.Thinking
		}
		s.Levels[role] = merged
	}
	return s
}

// agentConfig returns the effective model + thinking for a role, read FRESH per
// call (contract §9) and defaulted per the table when the key/level is absent. It
// is the single seam every spawn site threads into spawnOpts and run.Agent.Model.
func (c *Conductor) agentConfig(role string) (model, thinking string) {
	s := c.loadSettings()
	lc, ok := s.Levels[role]
	if !ok || lc.Model == "" {
		lc = defaultLevel(role)
	}
	if lc.Thinking == "" {
		lc.Thinking = defaultThinking(role)
	}
	return lc.Model, lc.Thinking
}

// ValidateSettings rejects an unknown model/thinking option or role so a POST can
// never persist a value the curated selects don't offer (which would spawn claude
// with a bad --model/--effort). An empty field is allowed (it falls back to the
// default for that level) — only a NON-empty out-of-enum value is rejected.
func ValidateSettings(s Settings) error {
	for role, lc := range s.Levels {
		known := false
		for _, r := range allRoles {
			if r == role {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown role %q (allowed: %v)", role, allRoles)
		}
		if lc.Model != "" && !modelOptions[lc.Model] {
			return fmt.Errorf("role %q: unknown model %q", role, lc.Model)
		}
		if lc.Thinking != "" && !thinkingOptions[lc.Thinking] {
			return fmt.Errorf("role %q: unknown thinking %q (allowed: low, medium, high, xhigh, max)", role, lc.Thinking)
		}
	}
	return nil
}

// SaveSettings validates and persists the settings object (single ooo key). The
// live read is served by OpenFilter("settings"); the conductor reads it fresh per
// spawn via agentConfig.
func (c *Conductor) SaveSettings(s Settings) error {
	if err := ValidateSettings(s); err != nil {
		return err
	}
	if c.server == nil {
		return fmt.Errorf("no server")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = c.server.Storage.Set(settingsKey, json.RawMessage(b))
	return err
}

// CurrentSettings returns the effective settings (persisted overlaid on defaults),
// so the API can hand the frontend a complete object even before any POST.
func (c *Conductor) CurrentSettings() Settings { return c.loadSettings() }
