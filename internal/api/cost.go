package api

import "strings"

// perMillionTokenPrice holds input and output prices per 1M tokens (USD).
type perMillionTokenPrice struct {
	input  float64
	output float64
}

// modelPrices maps model ID prefixes to per-token pricing. Entries are matched
// longest-prefix-first so "claude-opus-4-7" takes precedence over "claude-opus".
// Cache read tokens are priced at 10% of the base input rate; cache write tokens
// at 125%. Only Claude and direct Anthropic API models carry a known price —
// OpenAI-compatible providers return 0 since we don't bill those ourselves.
var modelPrices = []struct {
	prefix string
	price  perMillionTokenPrice
}{
	{"claude-opus-4-7", perMillionTokenPrice{15.00, 75.00}},
	{"claude-opus-4", perMillionTokenPrice{15.00, 75.00}},
	{"claude-sonnet-4-6", perMillionTokenPrice{3.00, 15.00}},
	{"claude-sonnet-4", perMillionTokenPrice{3.00, 15.00}},
	{"claude-haiku-4-5", perMillionTokenPrice{0.80, 4.00}},
	{"claude-haiku-4", perMillionTokenPrice{0.80, 4.00}},
	{"claude-3-5-sonnet", perMillionTokenPrice{3.00, 15.00}},
	{"claude-3-5-haiku", perMillionTokenPrice{0.80, 4.00}},
	{"claude-3-opus", perMillionTokenPrice{15.00, 75.00}},
	{"claude-3-sonnet", perMillionTokenPrice{3.00, 15.00}},
	{"claude-3-haiku", perMillionTokenPrice{0.25, 1.25}},
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
	const perM = 1_000_000.0
	inputCost := float64(u.InputTokens) / perM * p.input
	outputCost := float64(u.OutputTokens) / perM * p.output
	cacheReadCost := float64(u.CacheReadInputTokens) / perM * (p.input * 0.10)
	cacheWriteCost := float64(u.CacheCreationInputTokens) / perM * (p.input * 1.25)
	return inputCost + outputCost + cacheReadCost + cacheWriteCost
}
