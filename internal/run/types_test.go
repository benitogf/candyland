package run

import (
	"math"
	"testing"
)

// weightedFrom mirrors the weighted formula independently so the test pins the
// contract (output basis: input×0.2 + cacheRead×0.02 + cacheCreation×0.25 +
// output×1.0, then /1000 to ktokens), not the implementation.
func weightedFrom(input, cacheRead, cacheCreation, output int) int {
	return int((0.2*float64(input) + 0.02*float64(cacheRead) + 0.25*float64(cacheCreation) + 1.0*float64(output)) / 1000)
}

func TestWeightedTokens(t *testing.T) {
	got := WeightedTokens(1000, 2000, 400, 3000)
	want := weightedFrom(1000, 2000, 400, 3000) // (200 + 40 + 100 + 3000)/1000 = 3
	if got != want {
		t.Fatalf("WeightedTokens = %d, want %d", got, want)
	}
	if want != 3 {
		t.Fatalf("formula drift: want 3, got %d", want)
	}
}

func TestWeightedTokensZero(t *testing.T) {
	if got := WeightedTokens(0, 0, 0, 0); got != 0 {
		t.Fatalf("WeightedTokens(0…) = %d, want 0", got)
	}
}

// A cache read is far cheaper than a fresh input token, and output far dearer —
// the whole point of weighting over a flat count.
func TestWeightOrdering(t *testing.T) {
	if !(WeightCacheRead < WeightInput && WeightInput < WeightCacheCreation && WeightCacheCreation < WeightOutput) {
		t.Fatalf("weights out of order: read=%v input=%v create=%v output=%v",
			WeightCacheRead, WeightInput, WeightCacheCreation, WeightOutput)
	}
}

// Output is reconstructed from the /1000-scaled Tokens display counter — the only
// output signal carried on an agent.
func TestAgentWeightedTokens(t *testing.T) {
	a := Agent{InputTokens: 1000, CacheReadTokens: 2000, CacheCreationTokens: 400, Tokens: 3}
	if got, want := a.WeightedTokens(), weightedFrom(1000, 2000, 400, 3000); got != want {
		t.Fatalf("Agent.WeightedTokens = %d, want %d", got, want)
	}
}

func TestAgentTokenAccounting(t *testing.T) {
	a := Agent{InputTokens: 1000, CacheReadTokens: 2000, CacheCreationTokens: 400, Tokens: 3}
	acc := a.TokenAccounting()
	if acc.InputTokens != 1000 || acc.CacheReadTokens != 2000 || acc.CacheCreationTokens != 400 {
		t.Fatalf("accounting split wrong: %+v", acc)
	}
	if acc.OutputTokens != 3000 {
		t.Fatalf("OutputTokens = %d, want 3000", acc.OutputTokens)
	}
	if acc.WeightedTokens != 3 {
		t.Fatalf("WeightedTokens = %d, want 3", acc.WeightedTokens)
	}
	wantCost := 3 * CostPerKtokOutput
	if math.Abs(acc.CostUsd-wantCost) > 1e-9 {
		t.Fatalf("CostUsd = %v, want %v", acc.CostUsd, wantCost)
	}
}

func TestSumTokenAccounting(t *testing.T) {
	agents := []Agent{
		{InputTokens: 1000, CacheReadTokens: 2000, CacheCreationTokens: 400, Tokens: 3},
		{InputTokens: 500, CacheReadTokens: 0, CacheCreationTokens: 0, Tokens: 1},
	}
	acc := SumTokenAccounting(agents)
	if acc.InputTokens != 1500 || acc.CacheReadTokens != 2000 || acc.CacheCreationTokens != 400 || acc.OutputTokens != 4000 {
		t.Fatalf("summed split wrong: %+v", acc)
	}
	want := weightedFrom(1500, 2000, 400, 4000)
	if acc.WeightedTokens != want {
		t.Fatalf("summed WeightedTokens = %d, want %d", acc.WeightedTokens, want)
	}
}

func TestRunWeightedTokens(t *testing.T) {
	r := Run{Agents: []Agent{
		{InputTokens: 1000, CacheReadTokens: 2000, CacheCreationTokens: 400, Tokens: 3},
		{InputTokens: 500, Tokens: 1},
	}}
	if got, want := r.WeightedTokens(), SumTokenAccounting(r.Agents).WeightedTokens; got != want {
		t.Fatalf("Run.WeightedTokens = %d, want %d", got, want)
	}
}
