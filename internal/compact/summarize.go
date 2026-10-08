package compact

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/api"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/transcript"
	"github.com/icehunter/conduit/internal/truncate"
)

const (
	// summaryOutputTokens is the summarizer's max_tokens and the output
	// reserve subtracted from its window.
	summaryOutputTokens = 8192
	// summaryPromptReserveTokens covers the system prompt and wrapper text.
	summaryPromptReserveTokens = 4096
	// minTranscriptChars keeps a tiny configured window from emptying the input.
	minTranscriptChars = 4000

	toolResultFull   = 500
	toolResultShrunk = 200
	toolInputChars   = 300

	// charsPerToken is deliberately pessimistic: code and CJK text run well
	// under 4 chars per token, and overflowing the summarizer is worse than
	// summarizing a little less.
	charsPerToken = 3
)

// summarizerCapChars is the character budget for the summarizer's transcript:
// (window - output - prompt reserve) tokens at 4 chars per token, minus the
// text sent alongside the transcript.
func summarizerCapChars(o Options, extraChars int) int {
	window := o.SummarizerWindow
	if window <= 0 {
		window = internalmodel.ContextWindowFor(o.Model)
	}
	tokens := window - summaryOutputTokens - summaryPromptReserveTokens
	if tokens < 0 {
		tokens = 0
	}
	chars := tokens*charsPerToken - extraChars
	if chars < minTranscriptChars {
		chars = minTranscriptChars
	}
	return chars
}

// transcriptEntries flattens msgs to one string per block. Tool results longer
// than resultMax are shortened (JSON-aware when the result is JSON).
func transcriptEntries(msgs []api.Message, resultMax int) []string {
	var out []string
	for _, msg := range msgs {
		role := "User"
		if msg.Role == "assistant" {
			role = "Assistant"
		}
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if strings.TrimSpace(block.Text) != "" {
					out = append(out, role+": "+block.Text)
				}
			case "tool_use":
				entry := role + " [tool call: " + block.Name
				if len(block.Input) > 0 {
					if raw, err := json.Marshal(block.Input); err == nil {
						entry += " " + transcript.Truncate(string(raw), toolInputChars)
					}
				}
				out = append(out, entry+"]")
			case "tool_result":
				label := "Tool result: "
				if block.IsError {
					label = "Tool result (error): "
				}
				out = append(out, label+shortenResult(transcript.ToolResultText(block), resultMax))
			case "image":
				out = append(out, role+": [image]")
			}
		}
	}
	return out
}

// shortenResult caps a tool result at max runes and marks that it was cut so
// the summarizer knows context was elided.
func shortenResult(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	trimmed := strings.TrimSpace(text)
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed)) {
		text = string(truncate.TruncateJSONStringLeaves([]byte(trimmed), max))
		return transcript.Truncate(text, 2*max) + "… [truncated]"
	}
	return transcript.Truncate(text, max) + " [truncated]"
}

// lastUserTextIndex returns the index of the last user message that carries
// text, or -1.
func lastUserTextIndex(msgs []api.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		for _, b := range msgs[i].Content {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return i
			}
		}
	}
	return -1
}

// activeTaskSection repeats the last user request at the end of the transcript
// (Task 3.7) so a long run of tool output after it cannot hide the live task.
// It is empty when that request is already the final message.
func activeTaskSection(msgs []api.Message) string {
	idx := lastUserTextIndex(msgs)
	if idx < 0 || idx >= len(msgs)-1 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("--- Active task (current user request) ---\n")
	for _, block := range msgs[idx].Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			sb.WriteString("User: " + block.Text + "\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// buildTranscript converts messages to a readable text transcript with no
// size cap other than the per-result limit.
func buildTranscript(messages []api.Message, _ *api.Message) string {
	return joinTranscript(transcriptEntries(messages, toolResultFull), activeTaskSection(messages))
}

func joinTranscript(entries []string, active string) string {
	s := strings.Join(entries, "\n\n")
	if active != "" {
		s += "\n\n" + active
	}
	return strings.TrimSpace(s)
}

// fitTranscript builds the summarizer transcript within maxChars runes. Tool
// results shrink from 500 to 200 chars first; only then are the oldest entries
// dropped (with a marker), so the newest work is what survives.
func fitTranscript(msgs []api.Message, maxChars int) string {
	active := activeTaskSection(msgs)
	for _, resultMax := range []int{toolResultFull, toolResultShrunk} {
		s := joinTranscript(transcriptEntries(msgs, resultMax), active)
		if utf8.RuneCountInString(s) <= maxChars {
			return s
		}
	}

	entries := transcriptEntries(msgs, toolResultShrunk)
	room := maxChars - utf8.RuneCountInString(active) - 80
	if room < 0 {
		room = 0
	}
	start := len(entries)
	used := 0
	for start > 0 {
		n := utf8.RuneCountInString(entries[start-1]) + 2
		if used+n > room && start < len(entries) {
			break
		}
		used += n
		start--
	}
	kept := entries[start:]
	if len(kept) == 1 {
		kept[0] = transcript.Truncate(kept[0], max(room, minTranscriptChars/2))
	}
	var sb strings.Builder
	if start > 0 {
		fmt.Fprintf(&sb, "[… %d earlier entries omitted …]\n\n", start)
	}
	return sb.String() + joinTranscript(kept, active)
}
