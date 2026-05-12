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
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/sessionstats"
)

// DefaultModel is the fallback model used for summarization sub-calls.
// Mirrors getSmallFastModel().
const DefaultModel = "claude-haiku-4-5-20251001"

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

	// Build a readable transcript of the conversation.
	transcript := buildTranscript(messages)

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

// buildTranscript converts messages to a readable text transcript.
func buildTranscript(messages []api.Message) string {
	var sb strings.Builder
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
				text := block.ResultContent
				if len(text) > 500 {
					text = text[:500] + "… [truncated]"
				}
				sb.WriteString("Tool result: " + text + "\n\n")
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
