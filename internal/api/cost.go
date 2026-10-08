package api

import "strings"

// perMillionTokenPrice holds input and output prices per 1M tokens (USD).
// cacheRead overrides the default cache-read rate (10% of input) for models
// whose bundle pricing tier uses a different discount; zero means default.
type perMillionTokenPrice struct {
	input     float64
	output    float64
	cacheRead float64
	// long, when set, replaces the rates for requests whose prompt (input +
	// cache read + cache write) exceeds longAbove tokens (bundle long_prompt).
	long      *perMillionTokenPrice
	longAbove int
}

// modelPrices maps model ID prefixes to per-token pricing, mirroring the
// pricing_tiers table in the decoded Claude Code bundle (wire sync 2.1.292).
// Entries are matched in order, so longer prefixes must come first
// ("claude-opus-5-5" before "claude-opus-5"). Cache write tokens are priced
// at 125% of input (the 5m tier). Only Claude and direct Anthropic API models
// carry a known price; OpenAI-compatible providers return 0 since we don't
// bill those ourselves.
var modelPrices = []struct {
	prefix string
	price  perMillionTokenPrice
}{
	{"claude-fable-5-1", perMillionTokenPrice{10.00, 50.00, 0.25, nil, 0}},
	{"claude-fable", perMillionTokenPrice{10.00, 50.00, 0, nil, 0}},
	{"claude-mythos-5-1", perMillionTokenPrice{10.00, 50.00, 0.25, nil, 0}},
	{"claude-mythos", perMillionTokenPrice{10.00, 50.00, 0, nil, 0}},
	{"claude-opus-5-5", perMillionTokenPrice{4.00, 20.00, 0.20, nil, 0}},
	{"claude-opus-5", perMillionTokenPrice{5.00, 25.00, 0, nil, 0}},
	{"claude-opus-4-1", perMillionTokenPrice{15.00, 75.00, 0, nil, 0}},
	{"claude-opus-4-0", perMillionTokenPrice{15.00, 75.00, 0, nil, 0}},
	{"claude-opus-4-2025", perMillionTokenPrice{15.00, 75.00, 0, nil, 0}}, // Opus 4.0 dated first-party ID
	{"claude-opus-4", perMillionTokenPrice{5.00, 25.00, 0, nil, 0}},
	{"claude-sonnet-5", perMillionTokenPrice{2.00, 10.00, 0, nil, 0}},
	{"claude-sonnet-4", perMillionTokenPrice{3.00, 15.00, 0, nil, 0}},
	{"claude-haiku-5-5", perMillionTokenPrice{0.10, 0.50, 0.01,
		&perMillionTokenPrice{input: 0.50, output: 2.50, cacheRead: 0.05}, 100_000}},
	{"claude-haiku-4", perMillionTokenPrice{1.00, 5.00, 0, nil, 0}},
	{"claude-3-7-sonnet", perMillionTokenPrice{3.00, 15.00, 0, nil, 0}},
	{"claude-3-5-sonnet", perMillionTokenPrice{3.00, 15.00, 0, nil, 0}},
	{"claude-3-5-haiku", perMillionTokenPrice{0.80, 4.00, 0, nil, 0}},
	{"claude-3-opus", perMillionTokenPrice{15.00, 75.00, 0, nil, 0}},
	{"claude-3-sonnet", perMillionTokenPrice{3.00, 15.00, 0, nil, 0}},
	{"claude-3-haiku", perMillionTokenPrice{0.25, 1.25, 0, nil, 0}},
}

// PricePer1M returns the known per-1M-token input/output price for model, if
// any. Same prefix table CostUSDForModel uses — the one place Claude pricing
// is hand-maintained, since the Anthropic Models API doesn't return price.
// ok is false for a model this table has no entry for (e.g. one that just
// appeared via a live catalog fetch before anyone added a price row here).
func PricePer1M(model string) (input, output float64, ok bool) {
	lc := strings.ToLower(model)
	for _, entry := range modelPrices {
		if strings.HasPrefix(lc, entry.prefix) {
			return entry.price.input, entry.price.output, true
		}
	}
	return 0, 0, false
}

// CostUSDForModel computes the approximate cost in USD for the given usage
// against the named model. Returns 0 for unknown or OpenAI-compatible models.
func CostUSDForModel(model string, u Usage) float64 {
	lc := strings.ToLower(model)
	var p perMillionTokenPrice
	found := false
	for _, entry := range modelPrices {
		if strings.HasPrefix(lc, entry.prefix) {
			p = entry.price
			found = true
			break
		}
	}
	if !found {
		return 0
	}
	if p.long != nil && u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens > p.longAbove {
		p = *p.long
	}
	const perM = 1_000_000.0
	inputCost := float64(u.InputTokens) / perM * p.input
	outputCost := float64(u.OutputTokens) / perM * p.output
	cacheReadRate := p.cacheRead
	if cacheReadRate == 0 {
		cacheReadRate = p.input * 0.10
	}
	cacheReadCost := float64(u.CacheReadInputTokens) / perM * cacheReadRate
	cacheWriteCost := float64(u.CacheCreationInputTokens) / perM * (p.input * 1.25)
	return inputCost + outputCost + cacheReadCost + cacheWriteCost
}
