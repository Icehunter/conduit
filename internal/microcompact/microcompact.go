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
	"crypto/md5" //nolint:gosec // md5 used for deduplication only, not security
	"fmt"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/sessionstats"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tokens"
)

// ClearedMessage is the placeholder substituted for cleared tool_result text.
// Kept for backwards compatibility and idempotency checks — informative
// 1-liners produced by informativePlaceholder are used for new clears.
const ClearedMessage = "[Old tool result content cleared]"

// imageClearedMsg is the placeholder text block substituted for cleared image blocks.
const imageClearedMsg = "[image content cleared]"

// documentClearedMsg is the placeholder text block substituted for cleared document blocks.
const documentClearedMsg = "[document content cleared]"

// DefaultThreshold matches CC's gapThresholdMinutes default. The server's
// prompt cache has a 1h TTL — past that, the prefix would be re-tokenized
// regardless, so clearing is free.
const DefaultThreshold = 60 * time.Minute

// DefaultKeepRecent matches CC's keepRecent default. The model retains
// enough recent tool context to keep working while older noisy results
// (long file reads, big greps) shrink to the placeholder.
const DefaultKeepRecent = 5

// TokenBudgetFraction is the fraction of the context window kept as the
// recency budget for token-based keep (Task 3.8).
const TokenBudgetFraction = 0.20

// dupPrefix is the prefix used for deduplication markers, used to detect
// already-processed blocks on subsequent Apply calls (idempotency).
const dupPrefix = "[duplicate tool output"

// KeepRecent returns the configured keepRecent value, falling back to default.
func KeepRecent() int {
	cfg, err := settings.LoadConduitConfig()
	if err != nil || cfg.Compaction == nil || cfg.Compaction.KeepRecent <= 0 {
		return DefaultKeepRecent
	}
	return cfg.Compaction.KeepRecent
}

// Result describes what changed.
type Result struct {
	Messages      []api.Message
	TokensSaved   int
	Cleared       int // number of tool_results replaced
	Kept          int // number of tool_results kept intact
	ImagesCleared int // number of image/document blocks replaced
	Triggered     bool
}

// Options configures micro-compaction behaviour.
type Options struct {
	// LiveZoneBoundary, when > 0, prevents micro-compaction from mutating
	// messages at index < LiveZoneBoundary so the provider's cached prefix
	// stays byte-identical. Messages in the live zone (index >= boundary)
	// are still eligible for compaction.
	// Zero means no boundary (all messages eligible, original behaviour).
	LiveZoneBoundary int
}

// Apply runs time-based microcompact on messages. lastAssistantTime is
// the timestamp of the most recent assistant message; if zero, Apply is
// a no-op (no history yet). Returns the original slice unchanged when
// the gap is below threshold or there's nothing eligible to clear.
func Apply(messages []api.Message, lastAssistantTime time.Time, threshold time.Duration, keepRecent int) Result {
	return ApplyWithContext(messages, lastAssistantTime, threshold, keepRecent, 0)
}

// ApplyWithOptions is like Apply but respects Options. Apply remains unchanged
// for backward compatibility.
func ApplyWithOptions(messages []api.Message, lastAssistantTime time.Time, threshold time.Duration, keepRecent int, opts Options) Result {
	r := ApplyWithContext(messages, lastAssistantTime, threshold, keepRecent, 0)
	boundary := opts.LiveZoneBoundary
	if boundary <= 0 || !r.Triggered {
		return r
	}
	if boundary >= len(messages) {
		// Entire history is the cached prefix; nothing in the live zone to compact.
		return Result{Messages: messages}
	}
	// Restore messages before the boundary to their original (byte-identical) form
	// so the provider's KV cache prefix stays intact.
	out := make([]api.Message, len(r.Messages))
	copy(out, r.Messages)
	for i := range boundary {
		out[i] = messages[i]
	}
	r.Messages = out
	// Recheck Triggered: it's possible all compaction happened in the protected
	// prefix and the live zone was untouched.
	anyChanged := false
	for i := boundary; i < len(out); i++ {
		if messagesEqual(out[i], messages[i]) {
			continue
		}
		anyChanged = true
		break
	}
	if !anyChanged {
		r.Triggered = false
	}
	return r
}

// ApplyWithContext is Apply with an optional contextWindow for token-budget
// keep sizing. Pass 0 to fall back to the fixed keepRecent count.
func ApplyWithContext(messages []api.Message, lastAssistantTime time.Time, threshold time.Duration, keepRecent, contextWindow int) Result {
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
		// No tool_results to clear — every tool_result is in the keep window.
		// Still continue to the image/document pass: images can need clearing
		// even in conversations with few or no tool invocations.
		return applyImagePass(messages, keepRecent, Result{Messages: messages, Kept: len(ids)})
	}

	// Task 3.8: token-budget keep sizing.
	// If contextWindow is set, compute how many of the most recent tool_use
	// IDs fit within contextWindow * TokenBudgetFraction tokens. The
	// candidate pool is the tool_result content (estimated via len/4).
	// We walk backwards through IDs, accumulating token estimates,
	// and stop when the budget is exceeded. The result is floored at keepRecent.
	if contextWindow > 0 {
		budget := int(float64(contextWindow) * TokenBudgetFraction)
		// Build a quick map from tool_use_id -> result content for estimation.
		resultContent := make(map[string]string, len(ids))
		for _, m := range messages {
			if m.Role != "user" {
				continue
			}
			for _, b := range m.Content {
				if b.Type == "tool_result" && b.ToolUseID != "" {
					resultContent[b.ToolUseID] = b.ResultContent
				}
			}
		}
		tokensUsed := 0
		keepN := 0
		for i := len(ids) - 1; i >= 0; i-- {
			est := len(resultContent[ids[i]]) / 4 // fast heuristic
			// 1.5x soft ceiling: stop if adding this message would exceed 1.5x budget
			// to avoid splitting a message pair mid-way.
			if keepN > 0 && tokensUsed+est > (budget*3/2) {
				break
			}
			tokensUsed += est
			keepN++
			if tokensUsed >= budget {
				break
			}
		}
		if keepN > keepRecent {
			keepRecent = keepN
		}
		// Re-check: if all IDs fit in the budget, no tool_results to clear.
		if len(ids) <= keepRecent {
			return applyImagePass(messages, keepRecent, Result{Messages: messages, Kept: len(ids)})
		}
	}

	// Build a keep-set from the recent IDs and a clear-candidate set.
	keepSet := make(map[string]bool, keepRecent)
	for _, id := range ids[len(ids)-keepRecent:] {
		keepSet[id] = true
	}
	// clearCandidates is the set of tool_use IDs eligible for clearing (not in keep window).
	clearCandidates := make(map[string]bool, len(ids)-keepRecent)
	for _, id := range ids[:len(ids)-keepRecent] {
		clearCandidates[id] = true
	}

	// Build tool name lookup: tool_use_id -> tool Name, from assistant messages.
	toolNames := make(map[string]string, len(ids))
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_use" && b.ID != "" {
				toolNames[b.ID] = b.Name
			}
		}
	}

	// Task 3.2: md5 deduplication pass (candidates only).
	// Among the clear-candidate tool_results, find content hashes that appear
	// more than once. On the SECOND+ occurrence, replace with a dedupe marker
	// referencing the first occurrence (by 1-based message index).
	//
	// Deduplication applies ONLY to candidates (not kept results) so that
	// idempotency is preserved for the keep window and so the clearing pass
	// correctly counts cleared items.
	//
	// Two-phase: first walk to record first-seen index; second walk to replace.
	type firstSeen struct {
		msgIdx int // 1-based message index
	}
	firstSeenMap := make(map[[16]byte]firstSeen)
	msgIdx := 0
	for _, m := range messages {
		msgIdx++
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			if b.Type != "tool_result" || !clearCandidates[b.ToolUseID] {
				continue
			}
			if b.IsError || isAlreadyCleared(b.ResultContent) {
				continue
			}
			h := md5.Sum([]byte(b.ResultContent)) //nolint:gosec
			if _, seen := firstSeenMap[h]; !seen {
				firstSeenMap[h] = firstSeen{msgIdx: msgIdx}
			}
		}
	}

	// Second phase: walk again and apply dedup replacements.
	seenDups := make(map[[16]byte]bool)
	msgIdx = 0
	dupCleared := 0
	dupSaved := 0
	dedupOut := make([]api.Message, len(messages))
	for i, m := range messages {
		dedupOut[i] = m
		msgIdx++
		if m.Role != "user" {
			continue
		}
		var newContent []api.ContentBlock
		for j, b := range m.Content {
			if b.Type != "tool_result" || !clearCandidates[b.ToolUseID] {
				continue
			}
			if b.IsError || isAlreadyCleared(b.ResultContent) {
				continue
			}
			h := md5.Sum([]byte(b.ResultContent)) //nolint:gosec
			if seenDups[h] {
				// This is a duplicate -- replace with a marker.
				if newContent == nil {
					newContent = make([]api.ContentBlock, len(m.Content))
					copy(newContent, m.Content)
				}
				firstIdx := firstSeenMap[h].msgIdx
				dupSaved += tokens.Estimate(b.ResultContent)
				newContent[j].ResultContent = fmt.Sprintf(
					"[duplicate tool output -- same content as message %d]", firstIdx,
				)
				dupCleared++
			} else {
				seenDups[h] = true
			}
		}
		if newContent != nil {
			dedupOut[i].Content = newContent
		}
	}

	saved := 0
	cleared := 0
	// Copy-on-write per message: only allocate a new content slice if a
	// block actually changed in that message.
	out := make([]api.Message, len(dedupOut))
	for i, m := range dedupOut {
		out[i] = m
		if m.Role != "user" {
			continue
		}
		var newContent []api.ContentBlock
		for j, b := range m.Content {
			// Skip non-tool_result blocks.
			if b.Type != "tool_result" {
				continue
			}
			// Never clear error tool_results -- they contain critical debugging info.
			if b.IsError {
				continue
			}
			// Skip if in keep window or already cleared/deduplicated.
			if keepSet[b.ToolUseID] || isAlreadyCleared(b.ResultContent) {
				continue
			}
			if newContent == nil {
				newContent = make([]api.ContentBlock, len(m.Content))
				copy(newContent, m.Content)
			}
			saved += tokens.Estimate(newContent[j].ResultContent)
			// Task 3.3: informative per-tool 1-liner placeholder.
			newContent[j].ResultContent = informativePlaceholder(b, toolNames[b.ToolUseID])
			cleared++
		}
		if newContent != nil {
			out[i].Content = newContent
		}
	}

	totalCleared := cleared + dupCleared
	totalSaved := saved + dupSaved

	// Pass 2: elide old image/document blocks.
	// RecordMicrocompact is called once inside applyImagePass with the combined
	// tool_result + image savings, avoiding a double-count on the call counter.
	return applyImagePass(out, keepRecent, Result{
		Messages:    out,
		TokensSaved: totalSaved,
		Cleared:     totalCleared,
		Kept:        keepRecent,
		Triggered:   totalCleared > 0,
	})
}

// applyImagePass elides old image and document blocks in user messages,
// keeping the last keepRecent such blocks intact. It merges the result into
// base (accumulating TokensSaved, ImagesCleared, Triggered) and returns it.
// Messages are taken from base.Messages so tool_result pass output flows through.
func applyImagePass(messages []api.Message, keepRecent int, base Result) Result {
	// Collect (msgIdx, blockIdx) for every image/document block across all user messages.
	type imgRef struct {
		msgIdx   int // index into messages
		blockIdx int // index into message.Content
	}
	var imgRefs []imgRef
	for i, m := range messages {
		if m.Role != "user" {
			continue
		}
		for j, b := range m.Content {
			if b.Type == "image" || b.Type == "document" {
				imgRefs = append(imgRefs, imgRef{msgIdx: i, blockIdx: j})
			}
		}
	}

	if len(imgRefs) <= keepRecent {
		// All image/document blocks fit within the keep window.
		return base
	}

	// Work on a copy of the messages slice so we don't mutate the caller's slice.
	out := make([]api.Message, len(messages))
	copy(out, messages)

	imagesSaved := 0
	imagesCleared := 0

	// Track which message indices have already had their content slice copied
	// (copy-on-write: allocate once per message).
	copiedMsg := make(map[int]bool)
	toClear := imgRefs[:len(imgRefs)-keepRecent]
	for _, ref := range toClear {
		b := out[ref.msgIdx].Content[ref.blockIdx]
		// Idempotency: if the block is already a text placeholder, skip it.
		// (Blocks already cleared are type "text", not "image"/"document".)
		if b.Type == "text" {
			continue
		}
		// Determine placeholder text and estimate token savings.
		placeholder := imageClearedMsg
		if b.Type == "document" {
			placeholder = documentClearedMsg
		}
		dataLen := 0
		if b.Source != nil {
			dataLen = len(b.Source.Data)
		}
		imagesSaved += dataLen / 4 // base64 bytes → token heuristic

		// Copy-on-write: allocate a new content slice for this message only once.
		if !copiedMsg[ref.msgIdx] {
			newContent := make([]api.ContentBlock, len(out[ref.msgIdx].Content))
			copy(newContent, out[ref.msgIdx].Content)
			out[ref.msgIdx].Content = newContent
			copiedMsg[ref.msgIdx] = true
		}
		out[ref.msgIdx].Content[ref.blockIdx] = api.ContentBlock{
			Type: "text",
			Text: placeholder,
		}
		imagesCleared++
	}

	// Record one combined metric per Apply call: tool_result savings (carried in
	// base.TokensSaved) plus image savings from this pass. This prevents the
	// double-count that would occur if both the tool_result pass and this pass
	// called RecordMicrocompact independently.
	combinedSaved := base.TokensSaved + imagesSaved
	if base.Triggered || imagesCleared > 0 {
		sessionstats.SessionMetrics.RecordMicrocompact(combinedSaved)
	}

	return Result{
		Messages:      out,
		TokensSaved:   base.TokensSaved + imagesSaved,
		Cleared:       base.Cleared,
		Kept:          base.Kept,
		ImagesCleared: imagesCleared,
		Triggered:     base.Triggered || imagesCleared > 0,
	}
}

// messagesEqual reports whether two messages have identical content for the
// purpose of detecting live-zone compaction changes. It checks only the fields
// that micro-compaction touches (ResultContent, Type, Text, ToolUseID, ID, Name).
// It is NOT a general-purpose deep equality check — fields like Thinking,
// Signature, Input, Source, IsError, and CacheControl are intentionally omitted
// because the compactor never mutates them.
func messagesEqual(a, b api.Message) bool {
	if a.Role != b.Role || len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		ba, bb := a.Content[i], b.Content[i]
		if ba.Type != bb.Type ||
			ba.Text != bb.Text ||
			ba.ResultContent != bb.ResultContent ||
			ba.ToolUseID != bb.ToolUseID ||
			ba.ID != bb.ID ||
			ba.Name != bb.Name {
			return false
		}
	}
	return true
}

// isAlreadyCleared returns true if content has already been replaced by a
// placeholder from a prior Apply call (either the legacy ClearedMessage or
// a deduplication marker or an informative placeholder).
func isAlreadyCleared(content string) bool {
	return content == ClearedMessage ||
		strings.HasPrefix(content, dupPrefix) ||
		strings.HasPrefix(content, "[Bash]") ||
		strings.HasPrefix(content, "[Read]") ||
		strings.HasPrefix(content, "[Grep]") ||
		strings.HasPrefix(content, "[WebFetch]") ||
		strings.HasPrefix(content, "[tool]") ||
		// Generic per-tool prefix: "[ToolName] result cleared"
		(len(content) > 1 && content[0] == '[' && strings.Contains(content, "] result cleared"))
}

// informativePlaceholder returns a per-tool 1-liner summarizing cleared content.
// toolName is the Name field of the corresponding tool_use block.
func informativePlaceholder(b api.ContentBlock, toolName string) string {
	content := b.ResultContent
	nChars := len(content)
	nLines := strings.Count(content, "\n")
	if nChars > 0 && !strings.HasSuffix(content, "\n") {
		nLines++ // count last line even without trailing newline
	}

	switch toolName {
	case "Bash":
		// Extract exit code if present in common formats:
		//   "Exit code: N" or "exit code N" at end of content.
		exitCode := extractBashExitCode(content)
		if exitCode >= 0 {
			return fmt.Sprintf("[Bash] result -> exit %d, %d lines output", exitCode, nLines)
		}
		return fmt.Sprintf("[Bash] result -> %d lines output", nLines)

	case "Read", "FileRead":
		// Extract filename from content first line or common "Reading <path>" prefix.
		path := extractReadPath(content)
		if path != "" {
			return fmt.Sprintf("[Read] %s (%d lines)", path, nLines)
		}
		return fmt.Sprintf("[Read] result (%d lines)", nLines)

	case "Grep", "ripgrep":
		// Count non-empty lines as matches.
		matches := 0
		for line := range strings.SplitSeq(content, "\n") {
			if strings.TrimSpace(line) != "" {
				matches++
			}
		}
		return fmt.Sprintf("[Grep] result -> %d matches", matches)

	case "WebFetch":
		return fmt.Sprintf("[WebFetch] result -> %d chars", nChars)

	default:
		if toolName == "" {
			return fmt.Sprintf("[tool] result cleared (%d chars)", nChars)
		}
		return fmt.Sprintf("[%s] result cleared (%d chars)", toolName, nChars)
	}
}

// extractBashExitCode scans content for an exit code marker.
// Returns -1 if not found.
func extractBashExitCode(content string) int {
	// Look for "Exit code: N" or "exit code N" anywhere in the content.
	lower := strings.ToLower(content)
	for _, prefix := range []string{"exit code: ", "exit code ", "exitcode:"} {
		idx := strings.LastIndex(lower, prefix)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(content[idx+len(prefix):])
		// Parse leading digits.
		n := 0
		parsed := false
		for _, c := range rest {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
				parsed = true
			} else {
				break
			}
		}
		if parsed {
			return n
		}
	}
	return -1
}

// extractReadPath tries to extract a file path from Read tool output.
// Common formats: first line is the path, or the content starts with the
// file contents directly.
func extractReadPath(content string) string {
	// Look for a line that looks like a file path (starts with / or ./).
	firstLine, _, _ := strings.Cut(content, "\n")
	firstLine = strings.TrimSpace(firstLine)
	if strings.HasPrefix(firstLine, "/") || strings.HasPrefix(firstLine, "./") || strings.HasPrefix(firstLine, "../") {
		return firstLine
	}
	return ""
}
