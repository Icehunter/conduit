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
	"regexp"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/sessionstats"
	"github.com/icehunter/conduit/internal/sse"
	"github.com/icehunter/conduit/internal/transcript"
)

// DefaultModel is the fallback model used for summarization sub-calls: CC
// 2.1.293's latest_per_family.haiku (1M context, $0.10/$0.50 per MTok).
const DefaultModel = "claude-haiku-5-5"

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
	// Summary is the model-written (or fallback) summary text, without the
	// compaction-context sections.
	Summary string
	// NewHistory is the replacement conversation history: one user message
	// holding the summary and compaction context, followed by the verbatim
	// tail. A nil error always means len(NewHistory) >= 1.
	NewHistory []api.Message
	// TailLen is how many messages at the end of NewHistory are the verbatim
	// tail carried over unchanged.
	TailLen int
	// Fallback is true when the summarizer returned nothing usable and
	// Summary came from LocalSummary.
	Fallback bool
	// Usage is the summarizer call's token usage.
	Usage api.Usage
}

// Options configures CompactWithOptions.
type Options struct {
	// Model is the summarizer model; empty uses DefaultModel.
	Model string
	// Kind is KindAuto, KindManual or KindReactive; empty means KindManual.
	Kind string
	// CustomInstructions are appended to the summarizer prompt.
	CustomInstructions string
	// SessionID is sent as WireHints.SessionID and names the session in the
	// context-recovery footer.
	SessionID string
	// SummarizerWindow is the summarizer model's context window in tokens;
	// zero derives it from Model.
	SummarizerWindow int
	// MainWindow is the main model's context window; the verbatim tail is
	// capped at a fifth of it. Zero means no window cap.
	MainWindow int
	// TailTokens is the verbatim tail budget; zero means 20000.
	TailTokens int
	// RecoveryTools names the tools available to the model after compaction.
	// Only session_search and CCRRetrieve are mentioned in the footer, and
	// only when listed.
	RecoveryTools []string
	// RequireTail makes a history with no verbatim tail "nothing to compact".
	// A history ending in a user message and holding no assistant message has
	// that message in the part to be summarized: the request that must go out
	// next would lose it. Reactive compaction sets this.
	RequireTail bool
}

// Compaction kinds, sent as x-claude-code-compaction on the summarization
// request and as x-claude-code-context-compacted on the next main request.
const (
	KindAuto   = "auto"
	KindManual = "manual"
	// KindReactive is a compaction forced by a context-overflow error. The
	// summarization request still goes out as KindAuto (CC has no reactive
	// compaction kind there); the next main request carries "reactive".
	KindReactive = "reactive"
)

// FallbackNotice is shown when Result.Fallback is set.
const FallbackNotice = "Summary model unavailable — used a local summary."

// wireKind is the x-claude-code-compaction value for the summarization request.
func wireKind(kind string) string {
	if kind == KindReactive {
		return KindAuto
	}
	return kind
}

// Compact summarizes messages using the Anthropic API and returns a Result.
// The caller should replace their history with Result.NewHistory.
func Compact(ctx context.Context, client *api.Client, messages []api.Message, customInstructions string) (*Result, error) {
	return CompactWithModel(ctx, client, DefaultModel, messages, customInstructions, KindManual)
}

// CompactWithModel summarizes messages using the requested model. kind is
// KindAuto or KindManual. It is CompactWithOptions with default budgets.
func CompactWithModel(ctx context.Context, client *api.Client, model string, messages []api.Message, customInstructions, kind string) (*Result, error) {
	return CompactWithOptions(ctx, client, messages, Options{Model: model, Kind: kind, CustomInstructions: customInstructions})
}

// CompactWithOptions summarizes everything before a verbatim tail of recent
// messages and returns the replacement history.
//
// A nil error guarantees len(Result.NewHistory) >= 1: when the model returns
// no <summary> tag the rest of its answer (minus <analysis>) is used, and when
// nothing usable is left a deterministic LocalSummary stands in (Fallback).
// A stream that ends before message_stop is an error, and so is a history with
// nothing before the tail to compact (see IsNothingToCompact).
func CompactWithOptions(ctx context.Context, client *api.Client, messages []api.Message, o Options) (*Result, error) {
	if len(messages) == 0 {
		return nil, errors.New("no messages to compact")
	}
	if strings.TrimSpace(o.Model) == "" {
		o.Model = DefaultModel
	}
	if o.Kind == "" {
		o.Kind = KindManual
	}

	// A previous compaction's summary is always messages[0]. It is merged by
	// the summarizer, not re-summarized as conversation.
	previousSummary, prevCtx, hasPrev := extractPreviousContext(messages)
	first := 0
	if hasPrev {
		first = 1
	}
	split := splitTail(messages, tailBudget(o))
	if split <= first || (o.RequireTail && split >= len(messages)) {
		return nil, errNothingToCompact
	}
	head, tail := messages[first:split], messages[split:]

	var userMsg strings.Builder
	if previousSummary != "" {
		userMsg.WriteString("<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n")
		userMsg.WriteString("Please update the above summary with the following new conversation:\n\n")
	} else {
		userMsg.WriteString("Please summarize the following conversation:\n\n")
	}
	extra := len(previousSummary) + len(o.CustomInstructions)
	userMsg.WriteString(fitTranscript(head, summarizerCapChars(o, extra)))
	if o.CustomInstructions != "" {
		userMsg.WriteString("\n\nAdditional instructions: " + o.CustomInstructions)
	}

	req := &api.MessageRequest{
		Model:     o.Model,
		MaxTokens: summaryOutputTokens,
		System:    []api.SystemBlock{{Type: "text", Text: systemPrompt}},
		Messages: []api.Message{{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: userMsg.String()}},
		}},
		Stream: true,
		Hints:  &api.WireHints{RequestClass: "compaction", Compaction: wireKind(o.Kind), SessionID: o.SessionID},
	}
	stream, err := client.StreamMessage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("compact: stream: %w", err)
	}
	defer func() { _ = stream.Close() }()
	out, err := readSummary(stream)
	if err != nil {
		return nil, err
	}

	summary, fallback := resolveSummary(out.text, out.stopReason, head, o)
	res := assembleResult(head, tail, prevCtx, o, summary, fallback)
	res.Usage = out.usage
	return res, nil
}

// CompactLocal is CompactWithOptions without the model call: the summary is
// the deterministic LocalSummary, so it cannot fail on the network. Callers use
// it when the summarizer is unavailable and compaction cannot wait (a context
// overflow). Result.Fallback is always true.
func CompactLocal(messages []api.Message, o Options) (*Result, error) {
	if len(messages) == 0 {
		return nil, errors.New("no messages to compact")
	}
	if o.Kind == "" {
		o.Kind = KindManual
	}
	previousSummary, prevCtx, hasPrev := extractPreviousContext(messages)
	first := 0
	if hasPrev {
		first = 1
	}
	split := splitTail(messages, tailBudget(o))
	if split <= first || (o.RequireTail && split >= len(messages)) {
		return nil, errNothingToCompact
	}
	head, tail := messages[first:split], messages[split:]
	summary := LocalSummary(head, o)
	if previousSummary != "" {
		summary = previousSummary + "\n\n" + summary
	}
	return assembleResult(head, tail, prevCtx, o, summary, true), nil
}

// assembleResult builds the replacement history: the summary message with its
// compaction context, then the verbatim tail.
func assembleResult(head, tail []api.Message, prevCtx prevContext, o Options, summary string, fallback bool) *Result {
	text := "<summary>\n" + tagEscaper.Replace(summary) + "\n</summary>\n\n"
	if block := buildContextBlock(head, prevCtx, o); block != "" {
		text += block + "\n\n"
	}
	text += "Above is a summary of our conversation so far. Please continue from here."

	newHistory := make([]api.Message, 0, 1+len(tail))
	newHistory = append(newHistory, api.Message{
		Role:    "user",
		Content: []api.ContentBlock{{Type: "text", Text: text}},
	})
	newHistory = append(newHistory, shrinkTail(tail, tailBudget(o))...)

	sessionstats.SessionMetrics.RecordCompact()

	return &Result{
		Summary:    summary,
		NewHistory: newHistory,
		TailLen:    len(tail),
		Fallback:   fallback,
	}
}

// summaryStream is what readSummary collected from the summarizer's stream.
type summaryStream struct {
	text       string
	stopReason string
	usage      api.Usage
}

// readSummary drains the stream. A stream that ends without message_stop was
// cut off and yields an error: a partial summary must not replace the history.
func readSummary(stream *api.Stream) (summaryStream, error) {
	var out summaryStream
	var sb strings.Builder
	complete := false
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return summaryStream{}, fmt.Errorf("compact: read: %w", err)
		}
		switch ev.Type {
		case "message_start":
			if ms, err := ev.AsMessageStart(); err == nil {
				out.usage = mergeUsage(out.usage, ms.Message.Usage)
			}
		case "content_block_delta":
			if cbd, err := ev.AsContentBlockDelta(); err == nil && cbd.Delta.Type == "text_delta" {
				sb.WriteString(cbd.Delta.Text)
			}
		case "message_delta":
			if md, err := ev.AsMessageDelta(); err == nil {
				if md.Delta.StopReason != "" {
					out.stopReason = md.Delta.StopReason
				}
				out.usage = mergeUsage(out.usage, md.Usage)
			}
		case "message_stop":
			complete = true
		}
	}
	if !complete {
		return summaryStream{}, fmt.Errorf("compact: read: stream ended before message_stop: %w", api.ErrStreamTruncated)
	}
	out.text = sb.String()
	return out, nil
}

// mergeUsage overlays the non-zero counters of u onto cur: message_start
// carries the input side and message_delta the final output count.
func mergeUsage(cur api.Usage, u sse.Usage) api.Usage {
	if u.InputTokens > 0 {
		cur.InputTokens = u.InputTokens
	}
	if u.OutputTokens > 0 {
		cur.OutputTokens = u.OutputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		cur.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 {
		cur.CacheReadInputTokens = u.CacheReadInputTokens
	}
	return cur
}

var (
	analysisRe     = regexp.MustCompile(`(?s)<analysis>.*?</analysis>`)
	summaryTagsRe  = regexp.MustCompile(`</?summary>`)
	truncatedNotes = "\n\n[summary truncated]"
)

// resolveSummary turns the model output into the summary text. fallback is
// true when LocalSummary had to be used.
func resolveSummary(raw, stopReason string, head []api.Message, o Options) (summary string, fallback bool) {
	summary = extractSummary(raw)
	if summary == "" {
		// No usable <summary> block: drop the <analysis> scratchpad and use
		// whatever else the model said.
		rest := analysisRe.ReplaceAllString(raw, "")
		if i := strings.Index(rest, "<analysis>"); i >= 0 {
			rest = rest[:i] // unclosed scratchpad: everything after it is analysis
		}
		rest = strings.TrimSpace(summaryTagsRe.ReplaceAllString(rest, ""))
		summary = transcript.Truncate(rest, maxRecoveredSumChars)
	}
	if summary == "" {
		return LocalSummary(head, o), true
	}
	if stopReason == "max_tokens" {
		summary += truncatedNotes
	}
	return summary, false
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

// extractPreviousSummary returns the previous compaction's summary text when
// the first message is a compaction summary, for incremental merging. Returns
// empty string if no previous summary exists.
func extractPreviousSummary(messages []api.Message) string {
	summary, _, _ := extractPreviousContext(messages)
	return summary
}
