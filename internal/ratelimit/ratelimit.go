// Package ratelimit parses Anthropic rate-limit response headers and surfaces
// warnings when quota is running low. Mirrors src/services/claudeAiLimits.ts.
package ratelimit

import (
	"fmt"
	"net/http"
	"strconv"
)

// Info holds parsed rate-limit state from a single response.
type Info struct {
	// Request quota
	RequestsRemaining int
	RequestsLimit     int

	// Token quota (combined input+output)
	TokensRemaining int
	TokensLimit     int

	// Input-token quota (separate bucket)
	InputTokensRemaining int
	InputTokensLimit     int
}

// HasData reports whether any rate-limit header was present.
func (i Info) HasData() bool {
	return i.RequestsLimit > 0 || i.TokensLimit > 0 || i.InputTokensLimit > 0 ||
		i.RequestsRemaining > 0 || i.TokensRemaining > 0 || i.InputTokensRemaining > 0
}

// WarningMessage returns a non-empty string when any quota bucket is below
// 20% capacity. The caller should surface this in the status bar.
func (i Info) WarningMessage() string {
	type bucket struct {
		name      string
		remaining int
		limit     int
	}
	for _, b := range []bucket{
		{"requests", i.RequestsRemaining, i.RequestsLimit},
		{"tokens", i.TokensRemaining, i.TokensLimit},
		{"input-tokens", i.InputTokensRemaining, i.InputTokensLimit},
	} {
		if b.limit <= 0 {
			continue
		}
		pct := 100 * b.remaining / b.limit
		if pct < 20 {
			return fmt.Sprintf("rate limit warning: %s %d/%d remaining (%d%%)",
				b.name, b.remaining, b.limit, pct)
		}
	}
	return ""
}

// Parse reads the standard Anthropic rate-limit response headers.
// Missing or malformed headers are silently ignored.
func Parse(h http.Header) Info {
	return Info{
		RequestsRemaining:    parseInt(h.Get("anthropic-ratelimit-requests-remaining")),
		RequestsLimit:        parseInt(h.Get("anthropic-ratelimit-requests-limit")),
		TokensRemaining:      parseInt(h.Get("anthropic-ratelimit-tokens-remaining")),
		TokensLimit:          parseInt(h.Get("anthropic-ratelimit-tokens-limit")),
		InputTokensRemaining: parseInt(h.Get("anthropic-ratelimit-input-tokens-remaining")),
		InputTokensLimit:     parseInt(h.Get("anthropic-ratelimit-input-tokens-limit")),
	}
}

func parseInt(s string) int {
	if s == "" {
		return 0
	}
	v, _ := strconv.Atoi(s)
	return v
}
