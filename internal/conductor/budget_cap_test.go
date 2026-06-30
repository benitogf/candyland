package conductor

import (
	"slices"
	"strconv"
	"testing"
)

// C3: the per-pass cap is REAL, not just prompt text. The review/fix identity's
// spawn must carry claude's --max-turns hard cap on its argv, set to the per-pass
// ceiling (reviewFixTurns / clampReviewBudget). This inspects the built argv
// directly (claudeArgs is the pure arg builder streamOnce uses), so it fails if the
// cap is dropped from the spawn.
func TestReviewFixSpawnCarriesMaxTurns(t *testing.T) {
	want := reviewFixTurns()
	if want != reviewFixCeiling {
		t.Fatalf("reviewFixTurns must default to the ceiling %d, got %d", reviewFixCeiling, want)
	}
	args := claudeArgs("review the diff", nil, "", want)
	i := slices.Index(args, "--max-turns")
	if i < 0 {
		t.Fatalf("the review/fix spawn must include --max-turns; argv was %v", args)
	}
	if i+1 >= len(args) || args[i+1] != strconv.Itoa(want) {
		t.Fatalf("--max-turns must be the per-pass ceiling %d; argv was %v", want, args)
	}
}

// The cap is OPTIONAL and backward-compatible: an uncapped spawn (maxTurns 0, what
// the tech-lead/coder/conflict spawns pass) must NOT carry --max-turns, preserving
// today's behavior for every existing caller.
func TestUncappedSpawnHasNoMaxTurns(t *testing.T) {
	args := claudeArgs("partition the work", nil, "", 0)
	if slices.Contains(args, "--max-turns") {
		t.Fatalf("an uncapped spawn must NOT carry --max-turns; argv was %v", args)
	}
}

// An env override below the ceiling lowers the REAL hard cap too (not just the
// displayed budget), so the enforcement and the shown ceiling stay in agreement.
func TestReviewFixMaxTurnsHonorsEnvOverride(t *testing.T) {
	t.Setenv("CANDYLAND_REVIEW_BUDGET", "7")
	if got := reviewFixTurns(); got != 7 {
		t.Fatalf("env override must lower the hard --max-turns cap to 7, got %d", got)
	}
	args := claudeArgs("review the diff", nil, "", reviewFixTurns())
	i := slices.Index(args, "--max-turns")
	if i < 0 || args[i+1] != "7" {
		t.Fatalf("the lowered cap must reach the argv; argv was %v", args)
	}
}
