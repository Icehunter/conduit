// Package tokens estimates Claude token counts using cl100k_base —
// the same tokenizer Claude approximates for billing.
//
// Mirrors src/services/tokenEstimation.ts and src/utils/tokens.ts —
// both use a tiktoken cl100k encoder for pre-flight estimates.
//
// The encoder is lazy-initialized on first call. If tiktoken init fails
// (e.g. offline first run with no cached encoding), Estimate falls back
// to the chars/4 heuristic so callers always get a usable number.
package tokens

import (
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

var (
	encoder    *tiktoken.Tiktoken
	encoderErr error
	encoderMu  sync.Once
)

// Estimate returns the cl100k_base token count for s. Falls back to a
// chars/4 estimate if the encoder isn't available.
func Estimate(s string) int {
	if s == "" {
		return 0
	}
	encoderMu.Do(func() {
		encoder, encoderErr = tiktoken.GetEncoding("cl100k_base")
	})
	if encoderErr != nil || encoder == nil {
		return len([]rune(s)) / 4
	}
	return len(encoder.Encode(s, nil, nil))
}

// EstimateMany sums Estimate over a slice. Convenience for callers tallying
// over a list of message contents.
func EstimateMany(parts []string) int {
	total := 0
	for _, p := range parts {
		total += Estimate(p)
	}
	return total
}
