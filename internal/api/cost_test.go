package api

import (
	"math"
	"testing"
)

// Rates mirror the pricing_tiers table in the decoded CC 2.1.292 bundle.
func TestCostUSDForModel(t *testing.T) {
	const m = 1_000_000
	tests := []struct {
		model string
		usage Usage
		want  float64
	}{
		{"claude-opus-5-5", Usage{InputTokens: m, OutputTokens: m}, 4 + 20},
		{"claude-opus-5-5", Usage{CacheReadInputTokens: m}, 0.20},
		{"claude-opus-5", Usage{InputTokens: m, OutputTokens: m}, 5 + 25},
		{"claude-sonnet-5-5", Usage{InputTokens: m, OutputTokens: m}, 2 + 10},
		{"claude-sonnet-5-5", Usage{CacheReadInputTokens: m, CacheCreationInputTokens: m}, 0.20 + 2.5},
		{"claude-fable-5-1", Usage{CacheReadInputTokens: m}, 0.25},
		{"claude-fable-5", Usage{CacheReadInputTokens: m}, 1},
		{"claude-mythos-5-1", Usage{InputTokens: m, OutputTokens: m}, 10 + 50},
		{"claude-opus-4-1-20250805", Usage{InputTokens: m}, 15},
		{"claude-opus-4-20250514", Usage{InputTokens: m}, 15},
		{"claude-opus-4-8", Usage{InputTokens: m}, 5},
		{"claude-haiku-4-5-20251001", Usage{InputTokens: m, CacheReadInputTokens: m}, 1 + 0.1},
		{"gpt-5", Usage{InputTokens: m}, 0},
	}
	for _, tt := range tests {
		got := CostUSDForModel(tt.model, tt.usage)
		if math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("CostUSDForModel(%q, %+v) = %v, want %v", tt.model, tt.usage, got, tt.want)
		}
	}
}
