// Package compact implements conversation history summarization.
//
// Mirrors src/services/compact/compact.ts. Makes a secondary API call to
// a fast model asking it to summarize the conversation, then replaces the
// history with a single user message containing the summary.
package compact

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/sessionstats"
	"github.com/icehunter/conduit/internal/truncate"
)

// DefaultModel is the fallback model used for summarization sub-calls.
// Mirrors getSmallFastModel().
const DefaultModel = "claude-haiku-4-5-20251001"

// thrashThreshold is the minimum token-savings fraction to not be considered
// thrashing. If both of the last 2 compactions saved less than this fraction,
// the next compaction is skipped.
const thrashThreshold = 0.10

// systemPrompt tells the compaction model exactly what to produce.
// Structured template inspired by crush's summary.md with opencode's
// <previous-summary> merging support.
const systemPrompt = `You are summarizing a coding conversation to preserve context for continuing work later.

**Critical**: This summary will be the ONLY context available when the conversation resumes. Assume all previous messages will be lost. Be thorough.

CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.

If the prompt includes a <previous-summary> block, treat it as the current anchored summary. Update it with the new history by preserving still-true details, removing stale details, and merging in new facts.

Your response must contain exactly two XML blocks:

<analysis>
Chronologically analyze each message in the conversation. For each section identify:
- The user's explicit requests and intents
- The assistant's approach and key decisions
- Technical details: file names, code snippets, function signatures, edits
- Errors encountered and how they were resolved
- Specific user feedback, especially requests to do things differently
</analysis>

<summary>
A dense, accurate summary using these required sections:

## Current State
- What task is being worked on (exact user request)
- Current progress and what's been completed
- What's being worked on right now (incomplete work)
- What remains to be done (specific next steps, not vague)

## Files & Changes
- Files that were modified (with brief description of changes)
- Files that were read/analyzed (why they're relevant)
- Key files not yet touched but will need changes
- File paths and line numbers for important code locations

## Technical Context
- Architecture decisions made and why
- Patterns being followed (with examples)
- Libraries/frameworks being used
- Commands that worked (exact commands with context)
- Commands that failed (what was tried and why it didn't work)

## Strategy & Approach
- Overall approach being taken
- Why this approach was chosen over alternatives
- Key insights or gotchas discovered
- Assumptions made
- Any blockers or risks identified

## Exact Next Steps
Be specific. Don't write "implement authentication" - write:
1. Add JWT middleware to src/middleware/auth.js:15
2. Update login handler in src/routes/user.js:45 to return token
3. Test with: npm test -- auth.test.js

Keep every section, preserve exact file paths and identifiers when known, and prefer terse bullets over paragraphs.
</summary>`

// compactionRecord tracks the savings ratio of a single compaction run.
type compactionRecord struct {
	tokensBefore int
	tokensAfter  int
}

// savingsFraction returns the fraction of tokens saved (0.0–1.0).
func (r compactionRecord) savingsFraction() float64 {
	if r.tokensBefore <= 0 {
		return 0
	}
	saved := r.tokensBefore - r.tokensAfter
	return float64(saved) / float64(r.tokensBefore)
}

// Compactor wraps the stateless CompactWithModel call with an anti-thrashing
// guard (Task 3.5). Create one per session and reuse it across compaction calls.
type Compactor struct {
	// history holds the last 2 compaction records for thrash detection.
	history [2]compactionRecord
	// count is the number of compactions recorded so far (capped at 2).
	count int
}

// NewCompactor returns a ready-to-use Compactor.
func NewCompactor() *Compactor {
	return &Compactor{}
}

// recordResult registers a compaction outcome. tokensBefore and tokensAfter
// are the approximate token counts of the conversation before and after.
func (c *Compactor) recordResult(tokensBefore, tokensAfter int) {
	// Shift history: index 0 = oldest of the two; index 1 = newest.
	c.history[0] = c.history[1]
	c.history[1] = compactionRecord{tokensBefore: tokensBefore, tokensAfter: tokensAfter}
	if c.count < 2 {
		c.count++
	}
}

// isThrashing returns true when both of the last 2 compactions saved <10%.
// Returns false if fewer than 2 compactions have been recorded.
func (c *Compactor) isThrashing() bool {
	if c.count < 2 {
		return false
	}
	for _, r := range c.history {
		if r.savingsFraction() >= thrashThreshold {
			return false
		}
	}
	return true
}

// Compact summarizes messages and updates the thrash guard.
// tokensBefore should be the caller's estimate of current context size.
// Pass 0 if unavailable — the thrash guard will use the transcript length.
func (c *Compactor) Compact(ctx context.Context, client *api.Client, messages []api.Message, customInstructions string, tokensBefore int) (*Result, error) {
	return c.CompactWithModel(ctx, client, DefaultModel, messages, customInstructions, tokensBefore)
}

// CompactWithModel is like Compact but lets the caller choose the model.
func (c *Compactor) CompactWithModel(ctx context.Context, client *api.Client, model string, messages []api.Message, customInstructions string, tokensBefore int) (*Result, error) {
	// Task 3.5: anti-thrashing guard.
	if c.isThrashing() {
		log.Printf("compact: thrashing guard: last 2 compactions saved <%.0f%%; skipping", thrashThreshold*100)
		return nil, nil //nolint:nilnil // intentional: nil result + nil error means "skipped"
	}

	result, err := CompactWithModel(ctx, client, model, messages, customInstructions)
	if err != nil {
		return nil, err
	}

	// Update thrash history.
	tb := tokensBefore
	if tb <= 0 {
		// Estimate from transcript length as fallback.
		tb = estimateTranscriptTokens(messages)
	}
	ta := 0
	if result != nil && len(result.NewHistory) > 0 {
		ta = estimateTranscriptTokens(result.NewHistory)
	}
	c.recordResult(tb, ta)

	return result, nil
}

// estimateTranscriptTokens returns a rough token count (len/4 heuristic) for
// all text content in messages.
func estimateTranscriptTokens(msgs []api.Message) int {
	total := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				total += len(b.Text) / 4
			case "tool_result":
				total += len(b.ResultContent) / 4
			case "tool_use":
				for _, v := range b.Input {
					total += len(fmt.Sprintf("%v", v)) / 4
				}
			}
		}
	}
	return total
}

// Result is the output of a successful compaction.
type Result struct {
	// Summary is the extracted summary text.
	Summary string
	// NewHistory is the replacement conversation history (one user message).
	NewHistory []api.Message
}

// Compact summarizes messages using the Anthropic API and returns a Result.
// The caller should replace their history with Result.NewHistory.
func Compact(ctx context.Context, client *api.Client, messages []api.Message, customInstructions string) (*Result, error) {
	return CompactWithModel(ctx, client, DefaultModel, messages, customInstructions)
}

// CompactWithModel summarizes messages using the requested model.
func CompactWithModel(ctx context.Context, client *api.Client, model string, messages []api.Message, customInstructions string) (*Result, error) {
	if len(messages) == 0 {
		return nil, errors.New("no messages to compact")
	}
	if strings.TrimSpace(model) == "" {
		model = DefaultModel
	}

	// Check if the first message contains a previous summary (incremental compaction).
	previousSummary := extractPreviousSummary(messages)

	// Task 3.7: active-task anchor.
	// Always include the last user message in the summarization input, regardless
	// of token math. This prevents the live task from being dropped by compaction.
	lastUserMsg := findLastUserMessage(messages)

	// Build a readable transcript of the conversation.
	transcript := buildTranscript(messages, lastUserMsg)

	var userMsg strings.Builder
	if previousSummary != "" {
		// Include previous summary for incremental merging.
		userMsg.WriteString("<previous-summary>\n")
		userMsg.WriteString(previousSummary)
		userMsg.WriteString("\n</previous-summary>\n\n")
		userMsg.WriteString("Please update the above summary with the following new conversation:\n\n")
	} else {
		userMsg.WriteString("Please summarize the following conversation:\n\n")
	}
	userMsg.WriteString(transcript)
	if customInstructions != "" {
		userMsg.WriteString("\n\nAdditional instructions: ")
		userMsg.WriteString(customInstructions)
	}

	req := &api.MessageRequest{
		Model:     model,
		MaxTokens: 8192,
		System: []api.SystemBlock{{
			Type: "text",
			Text: systemPrompt,
		}},
		Messages: []api.Message{{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: userMsg.String()}},
		}},
		Stream: true,
	}

	stream, err := client.StreamMessage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("compact: stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	// Drain text from the stream.
	var sb strings.Builder
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("compact: read: %w", err)
		}
		if ev.Type == "content_block_delta" {
			cbd, err := ev.AsContentBlockDelta()
			if err == nil && cbd.Delta.Type == "text_delta" {
				sb.WriteString(cbd.Delta.Text)
			}
		}
	}

	raw := sb.String()
	summary := extractSummary(raw)
	// Do not fall back to the raw response — it would dump the entire model
	// output into session storage and leak as the session title.
	// An empty summary is safe: loop.go only fires OnCompact when summary != "".
	if summary == "" {
		return &Result{Summary: "", NewHistory: nil}, nil
	}

	newHistory := []api.Message{{
		Role: "user",
		Content: []api.ContentBlock{{
			Type: "text",
			Text: "<summary>\n" + summary + "\n</summary>\n\nAbove is a summary of our conversation so far. Please continue from here.",
		}},
	}}

	// Record compaction
	sessionstats.SessionMetrics.RecordCompact()

	return &Result{Summary: summary, NewHistory: newHistory}, nil
}

// findLastUserMessage returns a pointer to the last user message that contains
// meaningful text (not just tool_result blocks). Returns nil if none found.
func findLastUserMessage(messages []api.Message) *api.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return &messages[i]
			}
		}
	}
	return nil
}

// buildTranscript converts messages to a readable text transcript.
// anchorMsg, when non-nil, is always appended at the end even if it was
// already included in the normal traversal (deduplication is handled).
func buildTranscript(messages []api.Message, anchorMsg *api.Message) string {
	var sb strings.Builder

	// Determine the index of the anchor message so we can mark it.
	anchorIdx := -1
	if anchorMsg != nil {
		for i := range messages {
			if &messages[i] == anchorMsg {
				anchorIdx = i
				break
			}
		}
	}

	for _, msg := range messages {
		role := "User"
		if msg.Role == "assistant" {
			role = "Assistant"
		}
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if strings.TrimSpace(block.Text) != "" {
					sb.WriteString(role + ": " + block.Text + "\n\n")
				}
			case "tool_use":
				sb.WriteString(role + " [tool call: " + block.Name + "]\n\n")
			case "tool_result":
				// Task 3.6: JSON-aware truncation instead of raw byte-slice.
				// TruncateJSONStringLeaves handles valid JSON gracefully;
				// for plain text it falls back to byte truncation. Always
				// append a marker when the original exceeded the limit so
				// the summarizer knows context was elided.
				const resultMaxLen = 500
				text := block.ResultContent
				if len(text) > resultMaxLen {
					truncated := truncate.TruncateJSONStringLeaves([]byte(text), resultMaxLen)
					text = string(truncated) + "… [truncated]"
				}
				sb.WriteString("Tool result: " + text + "\n\n")
			}
		}
	}

	// Task 3.7: if the anchor message was not already the last written message,
	// append it explicitly so the live task is always visible to the summarizer.
	if anchorMsg != nil && anchorIdx >= 0 && anchorIdx < len(messages)-1 {
		// Check whether the anchor was already the final substantive message.
		// Re-append it with a clear marker so the summarizer notices.
		sb.WriteString("\n--- Active task (current user request) ---\n")
		for _, block := range anchorMsg.Content {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				sb.WriteString("User: " + block.Text + "\n\n")
			}
		}
	}

	return strings.TrimSpace(sb.String())
}

// extractSummary pulls the text between <summary> and </summary>.
func extractSummary(s string) string {
	start := strings.Index(s, "<summary>")
	if start < 0 {
		return ""
	}
	start += len("<summary>")
	end := strings.Index(s[start:], "</summary>")
	if end < 0 {
		return strings.TrimSpace(s[start:])
	}
	return strings.TrimSpace(s[start : start+end])
}

// extractPreviousSummary checks if the first message contains a previous
// compaction summary (wrapped in <summary> tags), returning it for incremental
// merging. Returns empty string if no previous summary exists.
func extractPreviousSummary(messages []api.Message) string {
	if len(messages) == 0 {
		return ""
	}
	// Previous summaries are always in the first user message after a compaction.
	first := messages[0]
	if first.Role != "user" {
		return ""
	}
	for _, block := range first.Content {
		if block.Type != "text" {
			continue
		}
		// Look for <summary> block indicating a previous compaction.
		if strings.Contains(block.Text, "<summary>") {
			return extractSummary(block.Text)
		}
	}
	return ""
}
