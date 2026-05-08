package api

import "strings"

// modelBetaDenylist maps model family prefix → beta header values that
// must NOT be sent to that family. The prefix is matched case-insensitively
// against the start of the model ID (after stripping any "models/" path prefix).
//
// Keep entries in alphabetical order by model prefix for readability.
var modelBetaDenylist = map[string][]string{
	// Haiku does not support the 1M-context or interleaved-thinking betas;
	// sending them causes the API to reject the request outright.
	"claude-haiku": {
		"context-1m-2025-08-07",
		"interleaved-thinking-2025-05-14",
	},
	"claude-3-haiku": {
		"context-1m-2025-08-07",
		"interleaved-thinking-2025-05-14",
	},
	"claude-3-5-haiku": {
		"context-1m-2025-08-07",
		"interleaved-thinking-2025-05-14",
	},
}

// filterBetasForModel returns a copy of betas with any entries that are
// denylisted for the given model removed. Safe to call with an empty or
// nil betas slice; always returns a non-nil slice.
func filterBetasForModel(betas []string, model string) []string {
	deny := betaDenylistForModel(model)
	if len(deny) == 0 {
		return betas
	}
	out := make([]string, 0, len(betas))
	for _, b := range betas {
		if !deny[b] {
			out = append(out, b)
		}
	}
	return out
}

// betaDenylistForModel returns a set of beta headers to suppress for model.
func betaDenylistForModel(model string) map[string]bool {
	// Strip any "models/" path prefix that some providers add.
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	lower := strings.ToLower(model)
	for prefix, denied := range modelBetaDenylist {
		if strings.HasPrefix(lower, prefix) {
			set := make(map[string]bool, len(denied))
			for _, d := range denied {
				set[d] = true
			}
			return set
		}
	}
	return nil
}
