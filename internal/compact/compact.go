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

	"github.com/icehunter/claude-go/internal/api"
)

// compactModel is the model used for summarization sub-calls.
// Mirrors getSmallFastModel() — Sonnet is fast and cheap for this.
const compactModel = "claude-haiku-4-5-20251001"

// systemPrompt tells the compaction model exactly what to produce.
const systemPrompt = `You are a conversation summarizer. Your task is to create a concise but complete summary of a conversation between a user and an AI assistant.

CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.

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
A dense, accurate summary that preserves all technical details needed to continue the conversation:
- What the user wanted to accomplish
- What was done, in what order
- Key decisions and their rationale
- Current state of files/code
- Any open issues or next steps
- Specific user preferences mentioned
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
	if len(messages) == 0 {
		return nil, errors.New("no messages to compact")
	}

	// Build a readable transcript of the conversation.
	transcript := buildTranscript(messages)

	userMsg := "Please summarize the following conversation:\n\n" + transcript
	if customInstructions != "" {
		userMsg += "\n\nAdditional instructions: " + customInstructions
	}

	req := &api.MessageRequest{
		Model:     compactModel,
		MaxTokens: 8192,
		System: []api.SystemBlock{{
			Type: "text",
			Text: systemPrompt,
		}},
		Messages: []api.Message{{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: userMsg}},
		}},
		Stream: true,
	}

	stream, err := client.StreamMessage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("compact: stream: %w", err)
	}
	defer stream.Close()

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
	if summary == "" {
		// Fallback: use the entire response if no <summary> tag found.
		summary = strings.TrimSpace(raw)
	}

	newHistory := []api.Message{{
		Role: "user",
		Content: []api.ContentBlock{{
			Type: "text",
			Text: "<summary>\n" + summary + "\n</summary>\n\nAbove is a summary of our conversation so far. Please continue from here.",
		}},
	}}

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
