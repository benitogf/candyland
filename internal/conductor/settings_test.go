package conductor

import (
	"slices"
	"testing"
)

// agentConfig defaults per the §9 table when no settings key is persisted: all
// models opus-4-8, thinking low for coder+fix and high for every coordinating role.
func TestAgentConfigDefaults(t *testing.T) {
	c, _ := newQuestServer(t)
	type want struct{ model, thinking string }
	cases := map[string]want{
		RoleCoder:          {"claude-opus-4-8", "low"},
		RoleFix:            {"claude-opus-4-8", "low"},
		RoleTechLead:       {"claude-opus-4-8", "high"},
		RoleQuestLead:      {"claude-opus-4-8", "high"},
		RoleIntentLead:     {"claude-opus-4-8", "high"},
		RoleTechManager:    {"claude-opus-4-8", "high"},
		RoleReviewer:       {"claude-fable-5", "high"},
		RoleIntentReviewer: {"claude-fable-5", "high"},
		RoleIntentManager:  {"claude-fable-5", "high"},
	}
	for role, w := range cases {
		model, thinking := c.agentConfig(role)
		if model != w.model {
			t.Errorf("role %q default model = %q, want %q", role, model, w.model)
		}
		if thinking != w.thinking {
			t.Errorf("role %q default thinking = %q, want %q", role, thinking, w.thinking)
		}
	}
}

// A persisted setting is read FRESH per call (no cache) and overlays the defaults;
// an absent level still falls back to its default.
func TestAgentConfigFreshPerCall(t *testing.T) {
	c, _ := newQuestServer(t)
	if m, th := c.agentConfig(RoleCoder); m != "claude-opus-4-8" || th != "low" {
		t.Fatalf("pre-change coder = %q/%q", m, th)
	}
	if err := c.SaveSettings(Settings{Levels: map[string]LevelConfig{
		RoleCoder: {Model: "claude-sonnet-5", Thinking: "high"},
	}}); err != nil {
		t.Fatal(err)
	}
	// Fresh read reflects the change immediately (no restart / cache).
	if m, th := c.agentConfig(RoleCoder); m != "claude-sonnet-5" || th != "high" {
		t.Errorf("post-change coder = %q/%q, want claude-sonnet-5/high", m, th)
	}
	// An untouched level still resolves to its default.
	if m, th := c.agentConfig(RoleReviewer); m != "claude-fable-5" || th != "high" {
		t.Errorf("untouched reviewer = %q/%q, want default fable/high", m, th)
	}
}

// ValidateSettings rejects out-of-enum model/thinking and unknown roles, but allows
// an empty field (which falls back to the default).
func TestValidateSettings(t *testing.T) {
	if err := ValidateSettings(DefaultSettings()); err != nil {
		t.Fatalf("the default table must validate: %v", err)
	}
	if err := ValidateSettings(Settings{Levels: map[string]LevelConfig{RoleCoder: {Model: "gpt-4"}}}); err == nil {
		t.Error("an unknown model must be rejected")
	}
	if err := ValidateSettings(Settings{Levels: map[string]LevelConfig{RoleCoder: {Thinking: "ultra"}}}); err == nil {
		t.Error("a thinking option outside low|medium|high|xhigh|max must be rejected")
	}
	// The extended effort ladder (xhigh, max) and the fable-5 model must validate.
	if err := ValidateSettings(Settings{Levels: map[string]LevelConfig{RoleCoder: {Model: "claude-fable-5", Thinking: "xhigh"}}}); err != nil {
		t.Errorf("claude-fable-5 + xhigh must validate: %v", err)
	}
	if err := ValidateSettings(Settings{Levels: map[string]LevelConfig{RoleTechLead: {Thinking: "max"}}}); err != nil {
		t.Errorf("max effort must validate: %v", err)
	}
	if err := ValidateSettings(Settings{Levels: map[string]LevelConfig{"nope": {Model: "claude-opus-4-8"}}}); err == nil {
		t.Error("an unknown role must be rejected")
	}
	if err := ValidateSettings(Settings{Levels: map[string]LevelConfig{RoleCoder: {Model: "claude-opus-4-8"}}}); err != nil {
		t.Errorf("an empty thinking field must be allowed (falls back to default): %v", err)
	}
}

// The configured model + thinking reach the spawned claude argv (AFTER -p), so the
// per-role selection genuinely drives the process — model → --model, thinking →
// --effort (the version-verified headless mechanism).
func TestClaudeArgsCarriesModelAndEffort(t *testing.T) {
	args := claudeArgs("do the work", nil, "", spawnOpts{model: "claude-sonnet-5", thinking: "high"})
	// -p <prompt> MUST stay first (the stub reads $2).
	if len(args) < 2 || args[0] != "-p" || args[1] != "do the work" {
		t.Fatalf("-p <prompt> must be first; argv was %v", args)
	}
	mi := slices.Index(args, "--model")
	if mi < 0 || args[mi+1] != "claude-sonnet-5" {
		t.Fatalf("--model must carry the configured model; argv was %v", args)
	}
	ei := slices.Index(args, "--effort")
	if ei < 0 || args[ei+1] != "high" {
		t.Fatalf("--effort must carry the configured thinking; argv was %v", args)
	}
	// An empty thinking omits --effort entirely (no no-op flag).
	if slices.Contains(claudeArgs("x", nil, "", spawnOpts{model: "claude-opus-4-8"}), "--effort") {
		t.Error("empty thinking must NOT add a --effort flag")
	}
}
