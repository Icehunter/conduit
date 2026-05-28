package session

import "github.com/icehunter/conduit/internal/api"

// SanitizeToolPairs removes orphaned tool_use / tool_result blocks from
// messages, returning a clean slice that the Anthropic API will accept without
// a 400 error.
//
// It delegates to FilterUnresolvedToolUses, which already implements the full
// four-pass orphan-removal algorithm:
//
//   - Pass 1: collect tool_use IDs that have a matching tool_result.
//   - Pass 2: drop assistant tool_use blocks with no matching tool_result.
//   - Pass 3: collect surviving tool_use IDs.
//   - Pass 4: drop user tool_result blocks whose tool_use_id has no surviving tool_use.
//
// Messages that become empty after filtering are dropped entirely.
//
// SanitizeToolPairs is the canonical entry point for callers that need
// post-microcompact cleanup (microcompact can clear tool_result content,
// turning a previously valid pair into an orphan). FilterUnresolvedToolUses
// is the lower-level implementation; both names are intentionally kept
// because "sanitize" communicates intent to compaction-adjacent callers
// while "filter" communicates intent to session-load callers.
func SanitizeToolPairs(messages []api.Message) []api.Message {
	return FilterUnresolvedToolUses(messages)
}
