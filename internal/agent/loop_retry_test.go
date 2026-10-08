package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/sse"
	"github.com/icehunter/conduit/internal/tool"
)

// scripted is one canned HTTP response.
type scripted struct {
	status  int // 0 means 200 with an event stream
	headers map[string]string
	body    string
}

// scriptServer answers each request with the next scripted response and keeps
// answering with the last one once the script runs out.
type scriptServer struct {
	mu        sync.Mutex
	script    []scripted
	bodies    []string
	headerLog []http.Header
}

func (s *scriptServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, string(raw))
	s.headerLog = append(s.headerLog, r.Header.Clone())
	n := len(s.bodies)
	resp := s.script[min(n, len(s.script))-1]
	s.mu.Unlock()

	for k, v := range resp.headers {
		w.Header().Set(k, v)
	}
	if resp.status == 0 {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, resp.body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.status)
	_, _ = io.WriteString(w, resp.body)
}

func (s *scriptServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *scriptServer) request(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[i]
}

// sleepLog is an injected Sleep that records the delays and never waits.
type sleepLog struct {
	mu   sync.Mutex
	ds   []time.Duration
	hook func() // runs on each sleep, e.g. to cancel the context
}

func (s *sleepLog) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.ds = append(s.ds, d)
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return ctx.Err()
}

func (s *sleepLog) delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.ds...)
}

// eventLog collects loop events.
type eventLog struct {
	mu  sync.Mutex
	evs []LoopEvent
}

func (e *eventLog) handle(ev LoopEvent) {
	e.mu.Lock()
	e.evs = append(e.evs, ev)
	e.mu.Unlock()
}

func (e *eventLog) all() []LoopEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]LoopEvent(nil), e.evs...)
}

func (e *eventLog) ofType(t EventType) []LoopEvent {
	var out []LoopEvent
	for _, ev := range e.all() {
		if ev.Type == t {
			out = append(out, ev)
		}
	}
	return out
}

// newScriptLoop builds a Loop against a scriptServer with an injected sleep.
func newScriptLoop(t *testing.T, script []scripted, mut func(*LoopConfig)) (*Loop, *scriptServer, *sleepLog) {
	t.Helper()
	ss := &scriptServer{script: script}
	srv := httptest.NewServer(ss)
	t.Cleanup(srv.Close)
	sl := &sleepLog{}
	cfg := LoopConfig{
		Model:     "m",
		MaxTokens: 1024,
		System:    []api.SystemBlock{{Type: "text", Text: "base"}},
		MaxTurns:  10,
		Sleep:     sl.sleep,
	}
	if mut != nil {
		mut(&cfg)
	}
	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	return NewLoop(c, tool.NewRegistry(), cfg), ss, sl
}

// --- SSE builders -----------------------------------------------------------

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func sseMessageStart() string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"
}

func sseTextBlock(text string) string {
	const idx = 0
	return fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n", idx) +
		fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", idx, jsonStr(text)) +
		fmt.Sprintf("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", idx)
}

func sseThinkingBlock(idx int, text string) string {
	return fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n", idx) +
		fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":%s}}\n\n", idx, jsonStr(text)) +
		fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}\n\n", idx) +
		fmt.Sprintf("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", idx)
}

// sseToolBlock streams a tool_use whose input arrives as partialJSON. When
// closed is false the block never gets its content_block_stop.
func sseToolBlock(idx int, id, name, partialJSON string, closed bool) string {
	out := fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"tool_use\",\"id\":%s,\"name\":%s,\"input\":{}}}\n\n", idx, jsonStr(id), jsonStr(name)) +
		fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%s}}\n\n", idx, jsonStr(partialJSON))
	if closed {
		out += fmt.Sprintf("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", idx)
	}
	return out
}

func sseFinish(stop string) string {
	return fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%s},\"usage\":{\"output_tokens\":5}}\n\n", jsonStr(stop)) +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

// sseMessage is a complete response: the blocks followed by the finish.
func sseMessage(stop string, blocks ...string) string {
	return sseMessageStart() + strings.Join(blocks, "") + sseFinish(stop)
}

// sseTruncated is a response the server drops after some text: no message_stop.
func sseTruncated(text string) string {
	return sseMessageStart() + sseTextBlock(text)
}

func sseErrorEvent(typ, msg string) string {
	return fmt.Sprintf("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":%s,\"message\":%s}}\n\n", jsonStr(typ), jsonStr(msg))
}

func apiErrBody(typ, msg string) string {
	return fmt.Sprintf(`{"type":"error","error":{"type":%s,"message":%s}}`, jsonStr(typ), jsonStr(msg))
}

func okResp(text string) scripted { return scripted{body: textOnlySSE(text)} }

func truncResp(text string) scripted { return scripted{body: sseTruncated(text)} }

func errResp(status int, typ, msg string, headers map[string]string) scripted {
	return scripted{status: status, headers: headers, body: apiErrBody(typ, msg)}
}

func lastText(t *testing.T, msgs []api.Message) string {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatal("empty history")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || len(last.Content) == 0 {
		t.Fatalf("last message = %+v; want a non-empty assistant message", last)
	}
	return last.Content[0].Text
}

// --- loop-level stream retries ---------------------------------------------

func TestLoop_StreamRetry(t *testing.T) {
	tests := []struct {
		name       string
		script     []scripted
		maxTurns   int
		wantCalls  int
		wantSleeps int
		wantErr    error // nil means Run succeeds
		wantFinal  string
	}{
		{
			// Bug 1: a stream that connects and then dies used to retry until
			// MaxTurns, because the counter reset on every successful connect.
			name:       "truncated forever gives up after 3 retries",
			script:     []scripted{truncResp("half")},
			maxTurns:   10,
			wantCalls:  4,
			wantSleeps: 3,
			wantErr:    api.ErrStreamTruncated,
		},
		{
			name:       "truncated then ok does not use the single turn",
			script:     []scripted{truncResp("half"), okResp("whole")},
			maxTurns:   1,
			wantCalls:  2,
			wantSleeps: 1,
			wantFinal:  "whole",
		},
		{
			name:       "three truncations then ok",
			script:     []scripted{truncResp("a"), truncResp("b"), truncResp("c"), okResp("fine")},
			maxTurns:   10,
			wantCalls:  4,
			wantSleeps: 3,
			wantFinal:  "fine",
		},
		{
			name:       "mid-stream overload event is retried",
			script:     []scripted{{body: sseMessageStart() + sseErrorEvent("overloaded_error", "Overloaded")}, okResp("fine")},
			maxTurns:   1,
			wantCalls:  2,
			wantSleeps: 1,
			wantFinal:  "fine",
		},
		{
			name:      "mid-stream auth error is not retried",
			script:    []scripted{{body: sseMessageStart() + sseErrorEvent("authentication_error", "bad key")}, okResp("never")},
			maxTurns:  10,
			wantCalls: 1,
			wantErr:   &sse.Error{},
		},
		{
			name:      "empty 200 body is a truncated stream",
			script:    []scripted{{body: ""}, okResp("fine")},
			maxTurns:  1,
			wantCalls: 2, wantSleeps: 1,
			wantFinal: "fine",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp, ss, sl := newScriptLoop(t, tt.script, func(c *LoopConfig) { c.MaxTurns = tt.maxTurns })
			ev := &eventLog{}
			msgs, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle)

			if ss.calls() != tt.wantCalls {
				t.Errorf("requests = %d, want %d", ss.calls(), tt.wantCalls)
			}
			if got := len(sl.delays()); got != tt.wantSleeps {
				t.Errorf("sleeps = %d (%v), want %d", got, sl.delays(), tt.wantSleeps)
			}
			switch {
			case tt.wantErr == nil:
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if got := lastText(t, msgs); got != tt.wantFinal {
					t.Errorf("final text = %q, want %q", got, tt.wantFinal)
				}
			case errors.Is(tt.wantErr, api.ErrStreamTruncated):
				if !errors.Is(err, api.ErrStreamTruncated) {
					t.Errorf("err = %v, want it to wrap ErrStreamTruncated", err)
				}
			default:
				var se *sse.Error
				if !errors.As(err, &se) {
					t.Errorf("err = %v, want an *sse.Error", err)
				}
			}
			if parts := ev.ofType(EventPartial); len(parts) != 0 {
				t.Errorf("EventPartial emitted %d time(s); it must not fire on exhaustion", len(parts))
			}
		})
	}
}

// The backoff between stream retries doubles from about a second, and each
// retry is announced as a stream restart with its reason and position.
func TestLoop_StreamRetry_BackoffAndEvents(t *testing.T) {
	lp, _, sl := newScriptLoop(t, []scripted{truncResp("x"), truncResp("x"), truncResp("x"), okResp("ok")}, nil)
	ev := &eventLog{}
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ds := sl.delays()
	if len(ds) != 3 {
		t.Fatalf("sleeps = %v, want 3", ds)
	}
	for i, d := range ds {
		base := time.Second << i
		if d < base*3/4 || d > base*5/4 {
			t.Errorf("sleep %d = %v, want within 25%% of %v", i, d, base)
		}
	}

	retries := ev.ofType(EventAPIRetry)
	if len(retries) != 3 {
		t.Fatalf("retry events = %d, want 3", len(retries))
	}
	for i, r := range retries {
		if r.RetryReason != api.ReasonStreamTruncated || !r.RetryStream || r.RetryAttempt != i+1 || r.RetryMax != 3 || r.RetryDelay != ds[i] {
			t.Errorf("retry %d = %+v", i, r)
		}
	}
}

// When the retries run out, the text that did arrive is kept as a partial
// assistant message, once, and without any tool call.
func TestLoop_StreamRetry_ExhaustedKeepsPartialText(t *testing.T) {
	body := sseMessageStart() + sseTextBlock("so far") + sseToolBlock(1, "toolu_x", "Bash", `{"command":"l`, false)
	lp, _, _ := newScriptLoop(t, []scripted{{body: body}}, nil)
	ev := &eventLog{}
	msgs, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle)
	if !errors.Is(err, api.ErrStreamTruncated) {
		t.Fatalf("err = %v, want ErrStreamTruncated", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("history = %+v, want the prompt and one partial assistant message", msgs)
	}
	last := msgs[1]
	if last.Role != "assistant" || len(last.Content) != 1 || last.Content[0].Type != "text" || last.Content[0].Text != "so far" {
		t.Errorf("partial = %+v, want only the text", last)
	}
}

// A tool row opened by a stream that then fails must be closed with an error
// result, or it stays "running…" forever.
func TestLoop_StreamRetry_ClosesDanglingToolRow(t *testing.T) {
	broken := sseMessageStart() + sseTextBlock("plan") + sseToolBlock(1, "toolu_dangling", "Bash", `{"command":"ls`, false)
	lp, _, _ := newScriptLoop(t, []scripted{{body: broken}, okResp("fine")}, nil)
	ev := &eventLog{}
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle); err != nil {
		t.Fatalf("Run: %v", err)
	}

	start, result, retry := -1, -1, -1
	for i, e := range ev.all() {
		switch {
		case e.Type == EventToolStart && e.ToolID == "toolu_dangling":
			start = i
		case e.Type == EventToolResult && e.ToolID == "toolu_dangling":
			result = i
			if !e.IsError || e.ResultText == "" {
				t.Errorf("closing result = %+v, want an error with text", e)
			}
		case e.Type == EventAPIRetry && retry < 0:
			retry = i
		}
	}
	if start < 0 || result < 0 || retry < 0 {
		t.Fatalf("events: start=%d result=%d retry=%d, want all present", start, result, retry)
	}
	if !(start < result && result < retry) {
		t.Errorf("event order start=%d result=%d retry=%d, want the row closed before the retry notice", start, result, retry)
	}
}

func TestLoop_StreamRetry_DanglingToolRowClosedOnExhaustion(t *testing.T) {
	broken := sseMessageStart() + sseToolBlock(0, "toolu_dangling", "Bash", `{"command":"ls`, false)
	lp, _, _ := newScriptLoop(t, []scripted{{body: broken}}, nil)
	ev := &eventLog{}
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle); err == nil {
		t.Fatal("expected an error")
	}
	var starts, results int
	for _, e := range ev.all() {
		switch {
		case e.Type == EventToolStart:
			starts++
		case e.Type == EventToolResult && e.IsError:
			results++
		}
	}
	if starts != 4 || results != 4 {
		t.Errorf("tool rows opened=%d closed=%d, want every opened row closed (4 attempts)", starts, results)
	}
}

// --- connect-time errors ----------------------------------------------------

func TestLoop_ConnectErrors(t *testing.T) {
	tests := []struct {
		name       string
		script     []scripted
		wantCalls  int
		wantSleeps []time.Duration // nil means none; a zero entry only checks the count
		wantErr    string          // substring; empty means Run succeeds
		wantClass  api.ErrorClass
	}{
		{
			name:      "400 is sent once",
			script:    []scripted{errResp(400, "invalid_request_error", "messages: bad field", nil), okResp("never")},
			wantCalls: 1, wantErr: "invalid_request_error", wantClass: api.ClassInvalidRequest,
		},
		{
			name:      "401 is sent once",
			script:    []scripted{errResp(401, "authentication_error", "bad token", nil), okResp("never")},
			wantCalls: 1, wantErr: "authentication_error", wantClass: api.ClassAuth,
		},
		{
			name:      "403 is sent once",
			script:    []scripted{errResp(403, "permission_error", "no", nil), okResp("never")},
			wantCalls: 1, wantErr: "permission_error", wantClass: api.ClassAuth,
		},
		{
			name:       "500 500 200 succeeds after two backoffs",
			script:     []scripted{errResp(500, "api_error", "boom", nil), errResp(500, "api_error", "boom", nil), okResp("ok")},
			wantCalls:  3,
			wantSleeps: []time.Duration{0, 0},
		},
		{
			name:       "retry-after-ms sets the delay exactly",
			script:     []scripted{errResp(429, "rate_limit_error", "slow", map[string]string{"retry-after-ms": "1500", "retry-after": "30"}), okResp("ok")},
			wantCalls:  2,
			wantSleeps: []time.Duration{1500 * time.Millisecond},
		},
		{
			name:       "retry-after seconds sets the delay",
			script:     []scripted{errResp(429, "rate_limit_error", "slow", map[string]string{"retry-after": "7"}), okResp("ok")},
			wantCalls:  2,
			wantSleeps: []time.Duration{7 * time.Second},
		},
		{
			name:      "x-should-retry false stops the retries",
			script:    []scripted{errResp(503, "api_error", "unavailable", map[string]string{"x-should-retry": "false"}), okResp("never")},
			wantCalls: 1, wantErr: "unavailable", wantClass: api.ClassTransient,
		},
		{
			name:       "x-should-retry true retries a client error, as the SDK does",
			script:     []scripted{errResp(418, "api_error", "teapot", map[string]string{"x-should-retry": "true"}), okResp("ok")},
			wantCalls:  2,
			wantSleeps: []time.Duration{0},
		},
		{
			name:       "x-should-retry true is bounded by the retry budget",
			script:     []scripted{errResp(400, "invalid_request_error", "again", map[string]string{"x-should-retry": "true"})},
			wantCalls:  6,
			wantSleeps: []time.Duration{0, 0, 0, 0, 0},
			wantErr:    "again", wantClass: api.ClassInvalidRequest,
		},
		{
			name:      "x-should-retry true never retries a 401",
			script:    []scripted{errResp(401, "authentication_error", "bad token", map[string]string{"x-should-retry": "true"}), okResp("never")},
			wantCalls: 1, wantErr: "authentication_error", wantClass: api.ClassAuth,
		},
		{
			// A local server answers an n_ctx overflow with a 500; retrying the
			// identical request cannot help.
			name:      "500 carrying an overflow message is not retried",
			script:    []scripted{errResp(500, "api_error", "the request exceeds the available context size, try increasing it (n_ctx)", nil), okResp("never")},
			wantCalls: 1, wantErr: "context size", wantClass: api.ClassContextOverflow,
		},
		{
			name:       "529 forever stops after 5 retries",
			script:     []scripted{errResp(529, "overloaded_error", "Overloaded", nil)},
			wantCalls:  6,
			wantSleeps: []time.Duration{0, 0, 0, 0, 0},
			wantErr:    "Overloaded", wantClass: api.ClassTransient,
		},
		{
			name:      "a rate limit that lasts minutes is surfaced",
			script:    []scripted{errResp(429, "rate_limit_error", "limit", map[string]string{"retry-after": "300"}), okResp("never")},
			wantCalls: 1, wantErr: "rate_limit_error", wantClass: api.ClassRateLimited,
		},
	}
	// These cases are about retries; overflow recovery has its own tests.
	t.Setenv("DISABLE_AUTO_COMPACT", "1")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp, ss, sl := newScriptLoop(t, tt.script, nil)
			ev := &eventLog{}
			_, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle)

			if ss.calls() != tt.wantCalls {
				t.Errorf("requests = %d, want %d", ss.calls(), tt.wantCalls)
			}
			ds := sl.delays()
			if len(ds) != len(tt.wantSleeps) {
				t.Fatalf("sleeps = %v, want %d of them", ds, len(tt.wantSleeps))
			}
			for i, want := range tt.wantSleeps {
				if want != 0 && ds[i] != want {
					t.Errorf("sleep %d = %v, want %v", i, ds[i], want)
				}
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if got := api.Classify(err).Class; got != tt.wantClass {
				t.Errorf("class = %v, want %v", got, tt.wantClass)
			}
		})
	}
}

func TestLoop_ConnectRetryEventsCarryReason(t *testing.T) {
	lp, _, _ := newScriptLoop(t, []scripted{errResp(502, "api_error", "bad gateway", nil), okResp("ok")}, nil)
	ev := &eventLog{}
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle); err != nil {
		t.Fatalf("Run: %v", err)
	}
	retries := ev.ofType(EventAPIRetry)
	if len(retries) != 1 {
		t.Fatalf("retry events = %d, want 1", len(retries))
	}
	r := retries[0]
	if r.RetryReason != api.ReasonServerError || r.RetryStatus != 502 || r.RetryAttempt != 1 || r.RetryMax != 5 || r.RetryStream {
		t.Errorf("retry event = %+v", r)
	}
}

// Retries of a request that never got a response must not consume turns.
func TestLoop_ConnectRetriesDoNotUseTurns(t *testing.T) {
	lp, ss, _ := newScriptLoop(t, []scripted{errResp(500, "api_error", "x", nil), errResp(500, "api_error", "x", nil), okResp("ok")}, func(c *LoopConfig) { c.MaxTurns = 1 })
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ss.calls() != 3 {
		t.Errorf("requests = %d, want 3", ss.calls())
	}
}

// --- cancellation -----------------------------------------------------------

func TestLoop_CancelDuringBackoff(t *testing.T) {
	tests := []struct {
		name   string
		script []scripted
	}{
		{"api-layer backoff", []scripted{errResp(503, "api_error", "unavailable", nil), okResp("never")}},
		{"loop-level stream backoff", []scripted{truncResp("half"), okResp("never")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp, ss, sl := newScriptLoop(t, tt.script, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sl.hook = cancel

			_, err := lp.Run(ctx, []api.Message{user("go")}, func(LoopEvent) {})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if ss.calls() != 1 {
				t.Errorf("requests = %d, want 1: a cancelled backoff must not resend", ss.calls())
			}
			if got := len(sl.delays()); got != 1 {
				t.Errorf("sleeps = %d, want 1", got)
			}
		})
	}
}

// --- recoverDrain classification -------------------------------------------

func TestRecoverDrain(t *testing.T) {
	idle := fmt.Errorf("stream: %w: %w", api.ErrStreamIdleTimeout, context.Canceled)
	tests := []struct {
		name       string
		err        error
		cancelled  bool // the caller's context is done
		spent      int  // retries already used on this request
		wantOut    drainOutcome
		wantSleeps []time.Duration
	}{
		// Bug 2: the idle-timeout error wraps context.Canceled, but the user
		// did not cancel; the caller's context decides.
		{name: "idle timeout with a live context is retried", err: idle, wantOut: drainRetry, wantSleeps: []time.Duration{0}},
		{name: "idle timeout with a cancelled context is a cancel", err: idle, cancelled: true, wantOut: drainCancel},
		{name: "canceled error with a live context is not retried", err: context.Canceled, wantOut: drainFail},
		{name: "truncated stream is retried", err: api.ErrStreamTruncated, wantOut: drainRetry, wantSleeps: []time.Duration{0}},
		{name: "unrecognised error is retried", err: errors.New("weird break"), wantOut: drainRetry, wantSleeps: []time.Duration{0}},
		{name: "retries exhausted", err: api.ErrStreamTruncated, spent: maxStreamRetries, wantOut: drainFail},
		{name: "auth error event is not retried", err: &sse.Error{Type: "authentication_error", Message: "no"}, wantOut: drainFail},
		{name: "overflow with no recovery fails", err: &sse.Error{Type: "invalid_request_error", Message: "prompt is too long: 300000 tokens > 200000 maximum"}, wantOut: drainFail},
		{
			name:       "retry-after hint is capped at a minute",
			err:        &api.APIStatusError{Status: 503, RetryAfter: 10 * time.Minute},
			wantOut:    drainRetry,
			wantSleeps: []time.Duration{maxLoopBackoffHint},
		},
		{
			name:       "short retry-after is honoured",
			err:        &api.APIStatusError{Status: 503, RetryAfter: 4 * time.Second},
			wantOut:    drainRetry,
			wantSleeps: []time.Duration{4 * time.Second},
		},
		{name: "rate limit longer than two minutes is not waited out", err: &api.APIStatusError{Status: 429, RetryAfter: 5 * time.Minute}, wantOut: drainFail},
		{
			name:       "short rate limit is waited out",
			err:        &api.APIStatusError{Status: 429, RetryAfter: 30 * time.Second},
			wantOut:    drainRetry,
			wantSleeps: []time.Duration{30 * time.Second},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sl := &sleepLog{}
			lp := &Loop{cfg: LoopConfig{Sleep: sl.sleep}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			rec := &requestRecovery{stream: tt.spent}
			ev := &eventLog{}
			res := streamResult{
				Blocks:       []api.ContentBlock{{Type: "text", Text: "partial"}},
				StartedTools: []startedTool{{ID: "t1", Name: "Bash"}},
			}
			in := []api.Message{user("q")}

			out, outcome, err := lp.recoverDrain(ctx, tt.err, res, in, rec, &failoverState{}, ev.handle)

			if outcome != tt.wantOut {
				t.Fatalf("outcome = %v, want %v (err %v)", outcome, tt.wantOut, err)
			}
			if (outcome != drainRetry) != (err != nil) {
				t.Errorf("err = %v for outcome %v", err, outcome)
			}
			ds := sl.delays()
			if len(ds) != len(tt.wantSleeps) {
				t.Fatalf("sleeps = %v, want %d", ds, len(tt.wantSleeps))
			}
			for i, want := range tt.wantSleeps {
				if want != 0 && ds[i] != want {
					t.Errorf("sleep %d = %v, want %v", i, ds[i], want)
				}
			}
			// Every outcome closes the row of the tool that never ran.
			if rs := ev.ofType(EventToolResult); len(rs) != 1 || rs[0].ToolID != "t1" || !rs[0].IsError {
				t.Errorf("tool results = %+v, want one error closing t1", rs)
			}
			// Text is kept as a partial message when the turn ends, not when it
			// is retried (the retry regenerates it).
			wantPartial := outcome == drainFail || outcome == drainCancel
			if gotPartial := len(out) == 2; gotPartial != wantPartial {
				t.Errorf("history len = %d, want a partial message = %v", len(out), wantPartial)
			}
		})
	}
}

// --- provider rotation -------------------------------------------------------

func rotationLoop(t *testing.T, primary, fallback []scripted) (*Loop, *scriptServer, *scriptServer, *sleepLog) {
	t.Helper()
	pss := &scriptServer{script: primary}
	fss := &scriptServer{script: fallback}
	psrv := httptest.NewServer(pss)
	fsrv := httptest.NewServer(fss)
	t.Cleanup(psrv.Close)
	t.Cleanup(fsrv.Close)

	chain := []settings.ActiveProviderSettings{
		{Kind: settings.ProviderKindAnthropicAPI, Model: "primary-model"},
		{Kind: settings.ProviderKindAnthropicAPI, Model: "fallback-model"},
	}
	sl := &sleepLog{}
	pc := api.NewClient(api.Config{BaseURL: psrv.URL, AuthToken: "t"}, psrv.Client())
	lp := NewLoop(pc, tool.NewRegistry(), LoopConfig{
		Model: "primary-model", MaxTokens: 1024, MaxTurns: 10, Sleep: sl.sleep,
		System: []api.SystemBlock{{Type: "text", Text: "base"}},
	})
	lp.SetProviderChain(settings.RoleDefault,
		func(string) []settings.ActiveProviderSettings { return chain },
		func(p settings.ActiveProviderSettings) (*api.Client, error) {
			if p.Model == "fallback-model" {
				return api.NewClient(api.Config{BaseURL: fsrv.URL, AuthToken: "t"}, fsrv.Client()), nil
			}
			return pc, nil
		})
	return lp, pss, fss, sl
}

// A stream that keeps breaking is retried with backoff first, then the loop
// moves to the next provider, which gets a fresh set of retries.
func TestLoop_StreamRetriesThenRotates(t *testing.T) {
	lp, pss, fss, sl := rotationLoop(t, []scripted{truncResp("x")}, []scripted{okResp("from fallback")})
	ev := &eventLog{}
	msgs, err := lp.Run(context.Background(), []api.Message{user("go")}, ev.handle)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := lastText(t, msgs); got != "from fallback" {
		t.Errorf("final text = %q", got)
	}
	if pss.calls() != 4 || fss.calls() != 1 {
		t.Errorf("primary calls = %d, fallback calls = %d, want 4 and 1", pss.calls(), fss.calls())
	}
	if got := len(sl.delays()); got != 3 {
		t.Errorf("sleeps = %d, want 3 (rotation itself does not wait)", got)
	}
	if fo := ev.ofType(EventProviderFailover); len(fo) != 1 {
		t.Errorf("failover events = %d, want 1", len(fo))
	}
}

// With another provider to try, a rate limit is not waited out at all.
func TestLoop_RateLimitRotatesWithoutWaiting(t *testing.T) {
	lp, pss, fss, sl := rotationLoop(t,
		[]scripted{errResp(429, "rate_limit_error", "slow", map[string]string{"retry-after": "5"})},
		[]scripted{okResp("from fallback")})
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pss.calls() != 1 || fss.calls() != 1 {
		t.Errorf("primary calls = %d, fallback calls = %d, want 1 and 1", pss.calls(), fss.calls())
	}
	if ds := sl.delays(); len(ds) != 0 {
		t.Errorf("sleeps = %v, want none", ds)
	}
}

// --- session reminder on retries ------------------------------------------

func TestLoop_SessionReminderOnEveryAttemptOfFirstRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	lp, ss, _ := newScriptLoop(t, []scripted{truncResp("half"), okResp("ok")}, func(c *LoopConfig) {
		c.Hooks = &settings.HooksSettings{
			SessionStart: []settings.HookMatcher{{
				Matcher: "startup",
				Hooks:   []settings.Hook{{Type: "command", Command: `printf '{"additionalContext":"startup context"}'`}},
			}},
		}
	})
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ss.calls() != 2 {
		t.Fatalf("requests = %d, want 2", ss.calls())
	}
	for i := range 2 {
		if !strings.Contains(ss.request(i), "startup context") {
			t.Errorf("request %d lacks the session reminder", i+1)
		}
	}
}

// A short rate limit must be retried, not surfaced, when the only other
// provider is cooling down from its own earlier failure.
func TestLoop_ShortRateLimitWithCooledChain(t *testing.T) {
	short := errResp(429, "rate_limit_error", "slow", map[string]string{"retry-after": "1"})
	lp, pss, fss, sl := rotationLoop(t,
		[]scripted{errResp(429, "rate_limit_error", "slow", map[string]string{"retry-after": "30"})},
		[]scripted{short, okResp("ok")})
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run: %v (primary %d, fallback %d calls, sleeps %v)", err, pss.calls(), fss.calls(), sl.delays())
	}
	if fss.calls() != 2 {
		t.Errorf("fallback calls = %d, want 2", fss.calls())
	}
}

// An empty-response resend of request 1 keeps the session reminder.
func TestLoop_SessionReminderSurvivesEmptyRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	lp, ss, _ := newScriptLoop(t, []scripted{{body: sseMessage("end_turn")}, okResp("ok")}, func(c *LoopConfig) {
		c.Hooks = &settings.HooksSettings{SessionStart: []settings.HookMatcher{{
			Matcher: "startup",
			Hooks:   []settings.Hook{{Type: "command", Command: `printf '{"additionalContext":"startup context"}'`}},
		}}}
	})
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ss.calls() != 2 || !strings.Contains(ss.request(1), "startup context") {
		t.Errorf("calls = %d; second request lacks the reminder", ss.calls())
	}
}

// With a single provider, a persistent short 429 spends only the API layer's
// retry budget: the loop's bounded resend is for rotation aborts only.
func TestLoop_PersistentShort429NoChain(t *testing.T) {
	lp, ss, sl := newScriptLoop(t, []scripted{errResp(429, "rate_limit_error", "slow", map[string]string{"retry-after": "1"})}, nil)
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err == nil {
		t.Fatal("expected the rate limit to surface")
	}
	if ss.calls() != 6 || len(sl.delays()) != 5 {
		t.Errorf("calls = %d, sleeps = %d, want 6 and 5", ss.calls(), len(sl.delays()))
	}
}

// Cancelling during the resend's wait is a cancel, not the wrapped 429. The
// handler aborts the API retries for a rotation that then fails (the fallback
// client cannot be built), which is what leaves the resend.
func TestLoop_CancelDuringRotationResend(t *testing.T) {
	lp, ss, sl := newScriptLoop(t, []scripted{errResp(429, "rate_limit_error", "slow", map[string]string{"retry-after": "1"})}, nil)
	chain := []settings.ActiveProviderSettings{
		{Kind: settings.ProviderKindAnthropicAPI, Model: "a"},
		{Kind: settings.ProviderKindAnthropicAPI, Model: "b"},
	}
	lp.SetProviderChain(settings.RoleDefault,
		func(string) []settings.ActiveProviderSettings { return chain },
		func(settings.ActiveProviderSettings) (*api.Client, error) { return nil, errors.New("no client") })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sl.hook = cancel
	_, err := lp.Run(ctx, []api.Message{user("go")}, func(LoopEvent) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if ss.calls() != 1 || len(sl.delays()) != 1 {
		t.Errorf("calls = %d, sleeps = %d, want 1 and 1", ss.calls(), len(sl.delays()))
	}
}
