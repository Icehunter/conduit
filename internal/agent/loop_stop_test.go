package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/provider/copilot"
	"github.com/icehunter/conduit/internal/tool"
)

// The wording is pinned as literals: CC's max_tokens nudge is a wire-visible
// string, and the other two are what the model reads.
const (
	wantMaxTokensNudge = "Output token limit hit. Resume directly — no apology, no recap of what you were doing. Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces."
	wantEmptyNudge     = "<system-reminder>\nYour previous response was empty. Reply with text or a tool call.\n</system-reminder>"
)

func blocksText(s string) []api.ContentBlock { return []api.ContentBlock{{Type: "text", Text: s}} }

func TestHandleStop(t *testing.T) {
	prompt := user("do it")
	toolBlocks := []api.ContentBlock{{Type: "tool_use", ID: "t1", Name: "Bash", Input: map[string]any{}}}
	thinking := []api.ContentBlock{{Type: "thinking", Thinking: "hmm", Signature: "s"}}

	tests := []struct {
		name       string
		res        streamResult
		st         stopState
		wantAction stopAction
		wantErr    error
		wantLen    int    // history length after
		wantLast   string // text of the last message's last block, when non-empty
		wantRole   string // role of the last message, when non-empty
		wantNotice string // substring of the notice, empty for none
		wantState  stopState
	}{
		{
			name:       "end_turn text ends the turn",
			res:        streamResult{Blocks: blocksText("done"), StopReason: "end_turn"},
			wantAction: stopEnd, wantLen: 2, wantLast: "done", wantRole: "assistant",
		},
		{
			name:       "tool_use runs the tools",
			res:        streamResult{Blocks: toolBlocks, StopReason: "tool_use"},
			wantAction: stopTools, wantLen: 2, wantRole: "assistant",
		},
		{
			name:       "tools run whatever the stop_reason says",
			res:        streamResult{Blocks: toolBlocks, StopReason: "end_turn"},
			wantAction: stopTools, wantLen: 2,
		},
		{
			name:       "tools resets the counters",
			res:        streamResult{Blocks: toolBlocks, StopReason: "max_tokens"},
			st:         stopState{continuations: 2, emptyRetries: 1},
			wantAction: stopTools, wantLen: 2,
		},
		{
			name:       "refusal fails and keeps the prompt last",
			res:        streamResult{Blocks: blocksText("I can't help with that"), StopReason: "refusal"},
			wantAction: stopFail, wantErr: ErrModelRefusal, wantLen: 1, wantRole: "user",
			wantNotice: "declined",
		},
		{
			name:       "refusal with no content still fails",
			res:        streamResult{StopReason: "refusal"},
			wantAction: stopFail, wantErr: ErrModelRefusal, wantLen: 1, wantNotice: "declined",
		},
		{
			name:       "context window exceeded with no recovery fails",
			res:        streamResult{Blocks: blocksText("partial"), StopReason: "model_context_window_exceeded"},
			wantAction: stopFail, wantErr: ErrContextWindowExceeded, wantLen: 2, wantLast: "partial",
		},
		{
			name:       "pause_turn ends the turn with a notice",
			res:        streamResult{Blocks: blocksText("searching"), StopReason: "pause_turn"},
			wantAction: stopEnd, wantLen: 2, wantLast: "searching", wantNotice: "paused",
		},
		{
			name:       "max_tokens with text continues with CC's nudge",
			res:        streamResult{Blocks: blocksText("long answer"), StopReason: "max_tokens"},
			wantAction: stopContinue, wantLen: 3, wantLast: wantMaxTokensNudge, wantRole: "user",
			wantNotice: "continuing (1/3)", wantState: stopState{continuations: 1},
		},
		{
			name:       "third continuation still continues",
			res:        streamResult{Blocks: blocksText("more"), StopReason: "max_tokens"},
			st:         stopState{continuations: 2},
			wantAction: stopContinue, wantLen: 3, wantLast: wantMaxTokensNudge,
			wantNotice: "continuing (3/3)", wantState: stopState{continuations: 3},
		},
		{
			name:       "fourth max_tokens ends the turn",
			res:        streamResult{Blocks: blocksText("still more"), StopReason: "max_tokens"},
			st:         stopState{continuations: 3},
			wantAction: stopEnd, wantLen: 2, wantLast: "still more", wantRole: "assistant",
			wantNotice: "repeatedly", wantState: stopState{},
		},
		{
			name:       "a normal end_turn resets the continuation count",
			res:        streamResult{Blocks: blocksText("done"), StopReason: "end_turn"},
			st:         stopState{continuations: 2},
			wantAction: stopEnd, wantLen: 2, wantState: stopState{},
		},
		{
			name:       "first empty response retries with a merged nudge",
			res:        streamResult{StopReason: "end_turn"},
			wantAction: stopRetry, wantLen: 1, wantRole: "user", wantLast: wantEmptyNudge,
			wantState: stopState{emptyRetries: 1},
		},
		{
			name:       "thinking-only counts as empty",
			res:        streamResult{Blocks: thinking, StopReason: "end_turn"},
			wantAction: stopRetry, wantLen: 1, wantLast: wantEmptyNudge, wantState: stopState{emptyRetries: 1},
		},
		{
			name:       "second empty response ends with (no content)",
			res:        streamResult{StopReason: "end_turn"},
			st:         stopState{emptyRetries: 1},
			wantAction: stopEnd, wantLen: 2, wantLast: "(no content)", wantRole: "assistant",
			wantNotice: "/model", wantState: stopState{},
		},
		{
			name:       "second thinking-only response also hints at /effort",
			res:        streamResult{Blocks: thinking, StopReason: "end_turn"},
			st:         stopState{emptyRetries: 1},
			wantAction: stopEnd, wantLen: 2, wantLast: "(no content)", wantNotice: "/effort",
		},
		{
			name:       "a non-empty response resets the empty counter",
			res:        streamResult{Blocks: blocksText("ok"), StopReason: "end_turn"},
			st:         stopState{emptyRetries: 1},
			wantAction: stopEnd, wantLen: 2, wantState: stopState{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp := &Loop{}
			ev := &eventLog{}
			in := []api.Message{prompt}
			st := tt.st

			out, action, err := lp.handleStop(context.Background(), in, tt.res, &st, nil, ev.handle)

			if action != tt.wantAction {
				t.Fatalf("action = %v, want %v", action, tt.wantAction)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if len(out) != tt.wantLen {
				t.Fatalf("history = %d messages, want %d: %+v", len(out), tt.wantLen, out)
			}
			if len(out) > 0 {
				last := out[len(out)-1]
				if tt.wantRole != "" && last.Role != tt.wantRole {
					t.Errorf("last role = %q, want %q", last.Role, tt.wantRole)
				}
				if tt.wantLast != "" {
					if len(last.Content) == 0 || last.Content[len(last.Content)-1].Text != tt.wantLast {
						t.Errorf("last message = %+v, want its last block to read %q", last, tt.wantLast)
					}
				}
			}
			notices := ev.ofType(EventNotice)
			switch {
			case tt.wantNotice == "" && len(notices) != 0:
				t.Errorf("unexpected notice %q", notices[0].Notice)
			case tt.wantNotice != "" && (len(notices) != 1 || !strings.Contains(notices[0].Notice, tt.wantNotice)):
				t.Errorf("notices = %+v, want one containing %q", notices, tt.wantNotice)
			}
			if st != tt.wantState {
				t.Errorf("state = %+v, want %+v", st, tt.wantState)
			}
			// The caller's slice must not be modified by a merged nudge.
			if len(in[0].Content) != 1 {
				t.Errorf("input prompt mutated: %+v", in[0])
			}
		})
	}
}

// The overflow hook is the seam for reactive compaction: when it recovers, the
// returned history gets the continuation nudge and the request goes again.
func TestHandleStop_ContextWindowExceededWithRecovery(t *testing.T) {
	// handleStop calls recoverOverflow, which cannot recover yet; this pins
	// the contract the hook's caller relies on.
	lp := &Loop{}
	_, action, err := lp.handleStop(context.Background(), []api.Message{user("q")},
		streamResult{StopReason: "model_context_window_exceeded"}, &stopState{}, nil, func(LoopEvent) {})
	if action != stopFail || !errors.Is(err, ErrContextWindowExceeded) {
		t.Fatalf("action = %v, err = %v; want stopFail with ErrContextWindowExceeded", action, err)
	}
	if got := api.Classify(err); got.Class != api.ClassContextOverflow {
		t.Errorf("Classify(%v) = %v; the TUI hint keys on it being an overflow", err, got.Class)
	}
}

func TestMergeNudge(t *testing.T) {
	tests := []struct {
		name     string
		msgs     []api.Message
		wantLen  int
		wantBlks int // blocks in the last message
	}{
		{"merges into a trailing user message", []api.Message{user("q")}, 1, 2},
		{"appends when the history ends with the assistant", []api.Message{user("q"), asst(text("a"))}, 3, 1},
		{"appends to an empty history", nil, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := mergeNudge(tt.msgs, "nudge")
			if len(out) != tt.wantLen {
				t.Fatalf("len = %d, want %d", len(out), tt.wantLen)
			}
			last := out[len(out)-1]
			if last.Role != "user" || len(last.Content) != tt.wantBlks || last.Content[len(last.Content)-1].Text != "nudge" {
				t.Errorf("last = %+v", last)
			}
		})
	}
}

// --- drainStream --------------------------------------------------------------

func drainOf(t *testing.T, sse string) (streamResult, *eventLog, error) {
	t.Helper()
	ev := &eventLog{}
	lp := &Loop{}
	res, err := lp.drainStream(context.Background(), api.NewStreamFromReader(io.NopCloser(strings.NewReader(sse))), ev.handle, nil)
	return res, ev, err
}

func TestDrainStream(t *testing.T) {
	tests := []struct {
		name        string
		sse         string
		wantErr     error
		wantStop    string
		wantBlocks  int
		wantInvalid []string
		wantStarted []string
		wantMsg     string // substring of the invalid tool message
	}{
		{
			name:       "complete text response",
			sse:        sseMessage("end_turn", sseTextBlock("hi")),
			wantStop:   "end_turn",
			wantBlocks: 1,
		},
		{
			name:        "complete tool call",
			sse:         sseMessage("tool_use", sseToolBlock(0, "t1", "Bash", `{"command":"ls"}`, true)),
			wantStop:    "tool_use",
			wantBlocks:  1,
			wantStarted: []string{"t1"},
		},
		{
			name:       "no message_stop is truncation",
			sse:        sseMessageStart() + sseTextBlock("half"),
			wantErr:    api.ErrStreamTruncated,
			wantBlocks: 1,
		},
		{
			name:    "empty body is truncation",
			sse:     "",
			wantErr: api.ErrStreamTruncated,
		},
		{
			name:        "cut-off tool call at max_tokens is invalid and says why",
			sse:         sseMessage("max_tokens", sseToolBlock(0, "t1", "Write", `{"file_path":"/a","content":"abc`, true)),
			wantStop:    "max_tokens",
			wantBlocks:  1,
			wantInvalid: []string{"t1"},
			wantStarted: []string{"t1"},
			wantMsg:     "output token limit",
		},
		{
			name:        "malformed tool arguments are invalid",
			sse:         sseMessage("tool_use", sseToolBlock(0, "t1", "Bash", `not json at all`, true)),
			wantStop:    "tool_use",
			wantBlocks:  1,
			wantInvalid: []string{"t1"},
			wantStarted: []string{"t1"},
			wantMsg:     "not valid JSON",
		},
		{
			name:        "one good and one bad call",
			sse:         sseMessage("tool_use", sseToolBlock(0, "good", "Bash", `{"command":"ls"}`, true), sseToolBlock(1, "bad", "Bash", `{"command":`, true)),
			wantStop:    "tool_use",
			wantBlocks:  2,
			wantInvalid: []string{"bad"},
			wantStarted: []string{"good", "bad"},
			wantMsg:     "not valid JSON",
		},
		{
			name:        "tool call never closed before the stream died",
			sse:         sseMessageStart() + sseToolBlock(0, "t1", "Bash", `{"command":"l`, false),
			wantErr:     api.ErrStreamTruncated,
			wantBlocks:  1,
			wantInvalid: []string{"t1"},
			wantStarted: []string{"t1"},
		},
		{
			name:     "stop_reason is read from the last message_delta",
			sse:      sseMessage("refusal"),
			wantStop: "refusal",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, ev, err := drainOf(t, tt.sse)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && res.StopReason != tt.wantStop {
				t.Errorf("StopReason = %q, want %q", res.StopReason, tt.wantStop)
			}
			if len(res.Blocks) != tt.wantBlocks {
				t.Errorf("blocks = %d, want %d: %+v", len(res.Blocks), tt.wantBlocks, res.Blocks)
			}
			if len(res.InvalidTools) != len(tt.wantInvalid) {
				t.Errorf("InvalidTools = %v, want %v", res.InvalidTools, tt.wantInvalid)
			}
			for _, id := range tt.wantInvalid {
				msg, ok := res.InvalidTools[id]
				if !ok {
					t.Errorf("InvalidTools lacks %q", id)
				}
				if tt.wantMsg != "" && !strings.Contains(msg, tt.wantMsg) {
					t.Errorf("InvalidTools[%q] = %q, want it to contain %q", id, msg, tt.wantMsg)
				}
			}
			var started []string
			for _, s := range res.StartedTools {
				started = append(started, s.ID)
			}
			if strings.Join(started, ",") != strings.Join(tt.wantStarted, ",") {
				t.Errorf("StartedTools = %v, want %v", started, tt.wantStarted)
			}
			// An invalid call must not reach the UI as unparseable JSON.
			for _, u := range ev.ofType(EventToolUse) {
				if len(u.ToolInput) > 0 && !jsonValid(u.ToolInput) {
					t.Errorf("EventToolUse for %s carries invalid JSON %q", u.ToolID, u.ToolInput)
				}
			}
		})
	}
}

func jsonValid(b []byte) bool { return json.Valid(b) }

// countingTool counts Execute calls.
type countingTool struct {
	fakeTool
	n atomic.Int32
}

func (c *countingTool) Execute(ctx context.Context, in json.RawMessage) (tool.Result, error) {
	c.n.Add(1)
	return c.fakeTool.Execute(ctx, in)
}

func (c *countingTool) calls() int { return int(c.n.Load()) }

// --- end to end ------------------------------------------------------------------

// Bug 4: a tool call cut off by the output limit used to be dropped silently,
// leaving its row running forever and the model unaware. Now it is answered
// with an error result and the model is told to re-issue it.
func TestLoop_TruncatedToolCallGetsErrorResult(t *testing.T) {
	reg := tool.NewRegistry()
	fake := &countingTool{fakeTool: fakeTool{name: "Write", result: "written"}}
	reg.Register(fake)
	truncated := sseMessage("max_tokens", sseTextBlock("writing"), sseToolBlock(1, "toolu_w", "Write", `{"file_path":"/a","content":"abc`, true))
	lp, ss, _ := newScriptLoop(t, []scripted{{body: truncated}, okResp("retrying smaller")}, nil)
	lp.reg = reg
	ev := &eventLog{}

	msgs, err := lp.Run(context.Background(), []api.Message{user("write a file")}, ev.handle)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ss.calls() != 2 {
		t.Fatalf("requests = %d, want 2", ss.calls())
	}
	req2 := ss.request(1)
	if !strings.Contains(req2, `"tool_use_id":"toolu_w"`) || !strings.Contains(req2, `"is_error":true`) {
		t.Errorf("second request lacks an error tool_result for the cut-off call:\n%s", req2)
	}
	if !strings.Contains(req2, "output token limit") {
		t.Errorf("second request lacks the guidance to re-issue with smaller arguments:\n%s", req2)
	}
	if fake.calls() != 0 {
		t.Errorf("the tool ran %d time(s); an invalid call must not execute", fake.calls())
	}
	var closed bool
	for _, e := range ev.ofType(EventToolResult) {
		if e.ToolID == "toolu_w" && e.IsError {
			closed = true
		}
	}
	if !closed {
		t.Error("the UI row for the call was never closed with an error result")
	}
	if got := lastText(t, msgs); got != "retrying smaller" {
		t.Errorf("final text = %q", got)
	}
}

func TestLoop_MaxTokensContinuation(t *testing.T) {
	cut := func(s string) scripted { return scripted{body: sseMessage("max_tokens", sseTextBlock(s))} }

	t.Run("continues with the nudge and uses a turn", func(t *testing.T) {
		lp, ss, _ := newScriptLoop(t, []scripted{cut("part one"), okResp("part two")}, nil)
		ev := &eventLog{}
		msgs, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if ss.calls() != 2 || !strings.Contains(ss.request(1), "Output token limit hit. Resume directly") {
			t.Fatalf("requests = %d; second request body:\n%s", ss.calls(), ss.request(1))
		}
		if len(msgs) != 4 || msgs[2].Role != "user" || msgs[2].Content[0].Text != wantMaxTokensNudge {
			t.Errorf("history = %+v, want user, assistant, nudge, assistant", msgs)
		}
		if n := ev.ofType(EventNotice); len(n) != 1 || !strings.Contains(n[0].Notice, "(1/3)") {
			t.Errorf("notices = %+v", n)
		}
	})

	t.Run("a continuation consumes a turn", func(t *testing.T) {
		lp, ss, _ := newScriptLoop(t, []scripted{cut("part one"), okResp("part two")}, func(c *LoopConfig) { c.MaxTurns = 1 })
		_, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {})
		if !errors.Is(err, ErrMaxTurnsExceeded) {
			t.Fatalf("err = %v, want ErrMaxTurnsExceeded", err)
		}
		if ss.calls() != 1 {
			t.Errorf("requests = %d, want 1", ss.calls())
		}
	})

	t.Run("gives up after three", func(t *testing.T) {
		lp, ss, _ := newScriptLoop(t, []scripted{cut("again")}, func(c *LoopConfig) { c.MaxTurns = 20 })
		ev := &eventLog{}
		if _, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if ss.calls() != 4 {
			t.Errorf("requests = %d, want 4 (the original and three continuations)", ss.calls())
		}
		if n := ev.ofType(EventNotice); len(n) != 4 || !strings.Contains(n[3].Notice, "repeatedly") {
			t.Errorf("notices = %+v", n)
		}
	})
}

func TestLoop_RefusalReturnsErrModelRefusal(t *testing.T) {
	lp, ss, _ := newScriptLoop(t, []scripted{{body: sseMessage("refusal", sseTextBlock("no"))}, okResp("never")}, nil)
	ev := &eventLog{}
	msgs, err := lp.Run(context.Background(), []api.Message{user("bad request")}, ev.handle)
	if !errors.Is(err, ErrModelRefusal) {
		t.Fatalf("err = %v, want ErrModelRefusal", err)
	}
	if ss.calls() != 1 {
		t.Errorf("requests = %d, want 1", ss.calls())
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Errorf("history = %+v, want it to end at the prompt", msgs)
	}
	if n := ev.ofType(EventNotice); len(n) != 1 {
		t.Errorf("notices = %+v, want one", n)
	}
}

func TestLoop_EmptyResponse(t *testing.T) {
	empty := scripted{body: sseMessage("end_turn")}

	t.Run("retried once with a nudge and no turn used", func(t *testing.T) {
		lp, ss, _ := newScriptLoop(t, []scripted{empty, okResp("answer")}, func(c *LoopConfig) { c.MaxTurns = 1 })
		msgs, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if ss.calls() != 2 || !strings.Contains(ss.request(1), "Your previous response was empty") {
			t.Fatalf("requests = %d; second request:\n%s", ss.calls(), ss.request(1))
		}
		if len(msgs) != 2 || lastText(t, msgs) != "answer" {
			t.Errorf("history = %+v, want the prompt and the answer only", msgs)
		}
		// The nudge rides in the prompt's own message, not as an extra message.
		if got := len(msgs[0].Content); got != 2 {
			t.Errorf("prompt has %d blocks, want the text and the nudge", got)
		}
	})

	t.Run("second empty response ends with (no content)", func(t *testing.T) {
		lp, ss, _ := newScriptLoop(t, []scripted{empty}, nil)
		ev := &eventLog{}
		msgs, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if ss.calls() != 2 {
			t.Errorf("requests = %d, want 2", ss.calls())
		}
		if got := lastText(t, msgs); got != "(no content)" {
			t.Errorf("final text = %q", got)
		}
		if n := ev.ofType(EventNotice); len(n) != 1 || !strings.Contains(n[0].Notice, "/model") {
			t.Errorf("notices = %+v", n)
		}
	})

	t.Run("thinking-only is empty", func(t *testing.T) {
		thinkingOnly := scripted{body: sseMessage("end_turn", sseThinkingBlock(0, "pondering"))}
		lp, ss, _ := newScriptLoop(t, []scripted{thinkingOnly, okResp("answer")}, nil)
		if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if ss.calls() != 2 {
			t.Errorf("requests = %d, want 2", ss.calls())
		}
	})
}

// Nudges are conduit's, not the user's: on Copilot only the prompt itself is
// billed as user-initiated.
func TestLoop_NudgesAreNotUserTurns(t *testing.T) {
	const chatLength = `data: {"choices":[{"delta":{"content":"cut"},"finish_reason":"length"}]}` + "\n\ndata: [DONE]\n\n"
	const chatEmpty = `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"

	tests := []struct {
		name   string
		bodies []string
	}{
		{"max_tokens continuation", []string{chatLength, chatTextSSE("rest")}},
		{"empty-response retry", []string{chatEmpty, chatTextSSE("answer")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &openAIChatRecorder{bodies: tt.bodies}
			srv := httptest.NewServer(rec)
			defer srv.Close()
			c := api.NewClient(api.Config{
				ProviderKind:   api.ProviderKindOpenAICompatible,
				BaseURL:        srv.URL,
				APIKey:         "k",
				RequestHeaders: copilot.RequestHeaders,
			}, srv.Client())
			lp := NewLoop(c, tool.NewRegistry(), LoopConfig{Model: "gpt-4.1", MaxTokens: 1024, SessionID: "s"})
			if _, err := lp.Run(context.Background(), []api.Message{user("hi")}, func(LoopEvent) {}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			hs := rec.got()
			if len(hs) != 2 {
				t.Fatalf("requests = %d, want 2", len(hs))
			}
			for i, want := range []string{"user", "agent"} {
				if got := hs[i].Get("X-Initiator"); got != want {
					t.Errorf("request %d x-initiator = %q, want %q", i+1, got, want)
				}
			}
		})
	}
}
