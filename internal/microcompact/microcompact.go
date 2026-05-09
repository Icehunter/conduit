// Package microcompact implements time-based micro-compaction: when the
// gap since the last assistant message exceeds a threshold (cache TTL),
// replace older tool_result content with a placeholder before sending the
// next request. The cache has expired anyway, so clearing tool results
// shrinks what gets re-cached without changing functional context — recent
// tool_results are kept intact.
//
// Mirrors src/services/compact/microCompact.ts maybeTimeBasedMicrocompact.
// CC's cache-editing path (cache_edits API) is Anthropic-internal beta and
// not implemented here — time-based is the public-build version.
package microcompact

import (
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tokens"
)

// ClearedMessage is the placeholder substituted for cleared tool_result text.
const ClearedMessage = "[Old tool result content cleared]"

// DefaultThreshold matches CC's gapThresholdMinutes default. The server's
// prompt cache has a 1h TTL — past that, the prefix would be re-tokenized
// regardless, so clearing is free.
const DefaultThreshold = 60 * time.Minute

// DefaultKeepRecent matches CC's keepRecent default. The model retains
// enough recent tool context to keep working while older noisy results
// (long file reads, big greps) shrink to the placeholder.
const DefaultKeepRecent = 5

// Result describes what changed.
type Result struct {
	Messages    []api.Message
	TokensSaved int
	Cleared     int // number of tool_results replaced
	Kept        int // number of tool_results kept intact
	Triggered   bool
}

// Apply runs time-based microcompact on messages. lastAssistantTime is
// the timestamp of the most recent assistant message; if zero, Apply is
// a no-op (no history yet). Returns the original slice unchanged when
// the gap is below threshold or there's nothing eligible to clear.
func Apply(messages []api.Message, lastAssistantTime time.Time, threshold time.Duration, keepRecent int) Result {
	if lastAssistantTime.IsZero() {
		return Result{Messages: messages}
	}
	if time.Since(lastAssistantTime) < threshold {
		return Result{Messages: messages}
	}
	if keepRecent < 1 {
		// Floor at 1 — clearing every tool_result leaves the model with
		// zero working context, which is never the right call.
		keepRecent = 1
	}

	// Collect tool_use IDs in order, walking the assistant messages.
	var ids []string
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_use" && b.ID != "" {
				ids = append(ids, b.ID)
			}
		}
	}
	if len(ids) <= keepRecent {
		// Nothing to clear — every tool_result is in the keep window.
		return Result{Messages: messages, Kept: len(ids)}
	}

	keepSet := make(map[string]bool, keepRecent)
	for _, id := range ids[len(ids)-keepRecent:] {
		keepSet[id] = true
	}

	saved := 0
	cleared := 0
	// Copy-on-write per message: only allocate a new content slice if a
	// block actually changed in that message.
	out := make([]api.Message, len(messages))
	for i, m := range messages {
		out[i] = m
		if m.Role != "user" {
			continue
		}
		var newContent []api.ContentBlock
		for j, b := range m.Content {
			if b.Type != "tool_result" || keepSet[b.ToolUseID] || b.ResultContent == ClearedMessage {
				continue
			}
			if newContent == nil {
				newContent = make([]api.ContentBlock, len(m.Content))
				copy(newContent, m.Content)
			}
			saved += tokens.Estimate(newContent[j].ResultContent)
			newContent[j].ResultContent = ClearedMessage
			cleared++
		}
		if newContent != nil {
			out[i].Content = newContent
		}
	}

	return Result{
		Messages:    out,
		TokensSaved: saved,
		Cleared:     cleared,
		Kept:        keepRecent,
		Triggered:   cleared > 0,
	}
}
