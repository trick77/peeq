package llm

import (
	"context"
	"testing"
)

// The rate table is asserted through ModelFor rather than against a literal id,
// per the same rule the model-name assertions follow: the default and short-gate
// deployments hold the same string today, so a literal would pass for the wrong
// reason on the day they diverge.
func TestCostNanoUSD_pricesTheDefaultModel(t *testing.T) {
	m := ModelFor(context.Background())
	if !priced(m) {
		t.Fatalf("default model %q has no rate; pricing.go was not updated with client.go", m)
	}

	// 1000 prompt tokens of which 200 were served from cache, and 500 out.
	// 800*75 + 200*15 + 500*250 = 60000 + 3000 + 125000.
	got := costNanoUSD(m, Usage{PromptTokens: 1000, CachedTokens: 200, CompletionTokens: 500})
	if want := int64(188_000); got != want {
		t.Fatalf("cost = %d, want %d", got, want)
	}
}

// The discount is the whole reason CachedTokens is carried separately, so it
// gets its own test: the same prompt priced with and without a cache hit must
// differ, and differ downward.
func TestCostNanoUSD_cachedTokensAreCheaperAndNotDoubleCounted(t *testing.T) {
	m := ModelFor(context.Background())
	cold := costNanoUSD(m, Usage{PromptTokens: 1000})
	warm := costNanoUSD(m, Usage{PromptTokens: 1000, CachedTokens: 1000})

	if cold != 75_000 {
		t.Fatalf("uncached prompt = %d, want 75000", cold)
	}
	// Every token cached: the whole prompt at the cache rate, not the cache
	// rate ON TOP of the full input rate. A double count would give 90000.
	if warm != 15_000 {
		t.Fatalf("fully cached prompt = %d, want 15000", warm)
	}
	if warm >= cold {
		t.Fatalf("cached prompt (%d) is not cheaper than uncached (%d)", warm, cold)
	}
}

// reasoning_tokens live INSIDE completion_tokens. Pricing them again would
// inflate every call peeq makes, since thinking cannot be switched off.
func TestCostNanoUSD_reasoningIsNotPricedTwice(t *testing.T) {
	m := ModelFor(context.Background())
	with := costNanoUSD(m, Usage{CompletionTokens: 400, ReasoningTokens: 300})
	without := costNanoUSD(m, Usage{CompletionTokens: 400})
	if with != without {
		t.Fatalf("reasoning tokens changed the price: %d vs %d", with, without)
	}
}

func TestCostNanoUSD_clampsImpossibleCacheCounts(t *testing.T) {
	m := ModelFor(context.Background())
	// cached > prompt cannot happen against a sane endpoint, but the numbers
	// come off the wire. The input lane must not go negative and credit the
	// caller for tokens it spent.
	got := costNanoUSD(m, Usage{PromptTokens: 100, CachedTokens: 400})
	if want := int64(100 * 15); got != want {
		t.Fatalf("cost = %d, want %d", got, want)
	}
	if got := costNanoUSD(m, Usage{PromptTokens: 100, CachedTokens: -50}); got != 100*75 {
		t.Fatalf("negative cached tokens mispriced: %d", got)
	}
}

// An unknown deployment prices at zero rather than guessing. priced() is what
// lets the caller tell that zero apart from a genuinely free call.
func TestCostNanoUSD_unknownModelIsUnpricedNotFree(t *testing.T) {
	if priced("some-model-nobody-added") {
		t.Fatal("unknown model reported as priced")
	}
	if got := costNanoUSD("some-model-nobody-added", Usage{PromptTokens: 1000, CompletionTokens: 1000}); got != 0 {
		t.Fatalf("cost = %d, want 0", got)
	}
}

func TestUsage_costAddsAndSubtracts(t *testing.T) {
	var totals Totals
	totals.Add(Usage{Requests: 1, Accounted: 1, CostNanoUSD: 1_500})
	mid := totals.Snapshot()
	totals.Add(Usage{Requests: 1, Accounted: 1, CostNanoUSD: 2_500})
	end := totals.Snapshot()

	if end.CostNanoUSD != 4_000 {
		t.Fatalf("total cost = %d, want 4000", end.CostNanoUSD)
	}
	// Sub is how a pipeline step reports its own share out of a running total.
	if step := end.Sub(mid); step.CostNanoUSD != 2_500 {
		t.Fatalf("step cost = %d, want 2500", step.CostNanoUSD)
	}
}
