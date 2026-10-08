package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/icehunter/conduit/internal/api"
)

// ErrModelRefusal is returned by Run when the model declined to answer
// (stop_reason "refusal"). The loop has already emitted an EventNotice; the
// history returned with it ends at the user's prompt, which callers drop.
var ErrModelRefusal = errors.New("agent: model refused to respond")

// ErrContextWindowExceeded is returned by Run when the model stopped because
// the context window was full and no recovery was possible.
var ErrContextWindowExceeded = errors.New("agent: context window exceeded (model_context_window_exceeded)")

const (
	// maxContinuations bounds consecutive max_tokens continuations.
	maxContinuations = 3

	// maxTokensNudge is Claude Code's verbatim recovery prompt after a
	// response is cut off at the output limit.
	maxTokensNudge = "Output token limit hit. Resume directly — no apology, no recap of what you were doing. " +
		"Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces."

	// emptyResponseNudge is merged into the trailing user message to retry a
	// response that held neither text nor a tool call.
	emptyResponseNudge = "<system-reminder>\nYour previous response was empty. Reply with text or a tool call.\n</system-reminder>"

	// emptyResponseText stands in for a response that stayed empty after one
	// retry, so the history keeps its user/assistant alternation.
	emptyResponseText = "(no content)"
)

// stopAction is what Run does after handleStop.
type stopAction int

const (
	stopEnd      stopAction = iota // the turn is over; run the end-of-turn steps
	stopTools                      // run the response's tool calls
	stopContinue                   // a nudge was appended; send a new request (uses a turn)
	stopRetry                      // resend the request, with no turn used
	stopFail                       // return the error
)

// stopState is Run's per-prompt memory for handleStop.
type stopState struct {
	// continuations counts consecutive max_tokens continuations.
	continuations int
	// emptyRetries counts empty responses retried since the last non-empty one.
	emptyRetries int
}

// handleStop decides what follows a cleanly drained response, from its blocks
// and stop_reason. It appends the assistant message (and any nudge) to msgs
// except where noted, and emits notices through handler. Order matters: tool
// calls always run, even under an odd stop_reason; then refusal, a full
// context window, pause_turn, an empty response, and max_tokens.
func (l *Loop) handleStop(ctx context.Context, msgs []api.Message, res streamResult, st *stopState, rs *runState, handler func(LoopEvent)) ([]api.Message, stopAction, error) {
	asst := api.Message{Role: "assistant", Content: res.Blocks}

	if hasToolUse(res.Blocks) {
		st.continuations, st.emptyRetries = 0, 0
		return append(msgs, asst), stopTools, nil
	}

	switch res.StopReason {
	case "refusal":
		// The refusal text is not kept: replaying it would only prime the next
		// request, and the prompt that triggered it is dropped by the caller.
		handler(LoopEvent{Type: EventNotice, Notice: "The model declined to answer this request. Rephrase it or try a different model."})
		return msgs, stopFail, ErrModelRefusal

	case "model_context_window_exceeded":
		msgs = appendNonEmpty(msgs, asst)
		c := api.Classified{Class: api.ClassContextOverflow, Reason: "model_context_window_exceeded"}
		if recovered, ok := l.recoverOverflow(ctx, c, msgs, rs, handler); ok {
			return append(recovered, nudgeMessage(maxTokensNudge)), stopContinue, nil
		}
		return msgs, stopFail, ErrContextWindowExceeded

	case "pause_turn":
		msgs = appendNonEmpty(msgs, asst)
		handler(LoopEvent{Type: EventNotice, Notice: "The model paused its turn. Send a message to continue."})
		return msgs, stopEnd, nil
	}

	if !hasContent(res.Blocks) {
		if st.emptyRetries == 0 {
			st.emptyRetries++
			return mergeNudge(msgs, emptyResponseNudge), stopRetry, nil
		}
		hint := "The model returned an empty response. Try again, or switch models with /model."
		if onlyThinking(res.Blocks) {
			hint += " It spent the reply thinking; lower the thinking budget with /effort."
		}
		handler(LoopEvent{Type: EventNotice, Notice: hint})
		st.emptyRetries = 0
		return append(msgs, api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: emptyResponseText}}}), stopEnd, nil
	}
	st.emptyRetries = 0

	if res.StopReason == "max_tokens" {
		msgs = append(msgs, asst)
		if st.continuations >= maxContinuations {
			handler(LoopEvent{Type: EventNotice, Notice: "Output limit reached repeatedly. Send a message to continue, or ask for a shorter answer."})
			st.continuations = 0
			return msgs, stopEnd, nil
		}
		st.continuations++
		handler(LoopEvent{Type: EventNotice, Notice: fmt.Sprintf("Output limit reached — continuing (%d/%d)", st.continuations, maxContinuations)})
		return append(msgs, nudgeMessage(maxTokensNudge)), stopContinue, nil
	}

	st.continuations = 0
	return append(msgs, asst), stopEnd, nil
}

// hasContent reports whether blocks hold text or a tool call. A response of
// only thinking blocks counts as empty.
func hasContent(blocks []api.ContentBlock) bool {
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				return true
			}
		case "tool_use", "server_tool_use":
			return true
		}
	}
	return false
}

// onlyThinking reports whether blocks hold thinking and nothing else.
func onlyThinking(blocks []api.ContentBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type != "thinking" {
			return false
		}
	}
	return true
}

// appendNonEmpty appends m unless it has no content blocks.
func appendNonEmpty(msgs []api.Message, m api.Message) []api.Message {
	if len(m.Content) == 0 {
		return msgs
	}
	return append(msgs, m)
}

// nudgeMessage is an agent-initiated user message. It is not the user's turn,
// so callers leave the Copilot initiator at "agent".
func nudgeMessage(text string) api.Message {
	return api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: text}}}
}

// mergeNudge adds text to the trailing user message, or appends a user message
// when the history does not end in one. The message is copied: its content
// slice may be shared with the caller's history.
func mergeNudge(msgs []api.Message, text string) []api.Message {
	n := len(msgs)
	if n == 0 || msgs[n-1].Role != "user" {
		return append(msgs, nudgeMessage(text))
	}
	last := msgs[n-1]
	content := make([]api.ContentBlock, 0, len(last.Content)+1)
	content = append(content, last.Content...)
	content = append(content, api.ContentBlock{Type: "text", Text: text})
	last.Content = content
	out := append([]api.Message(nil), msgs...)
	out[n-1] = last
	return out
}
