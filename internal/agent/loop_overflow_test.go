package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/tool"
)

const (
	promptTooLong   = "prompt is too long: 210000 tokens > 200000 maximum"
	maxTokensCounts = "input length and `max_tokens` exceed context limit: 195000 + 21333 > 200000, decrease input length or `max_tokens` and try again"
)

func overflowResp(msg string) scripted { return errResp(400, "invalid_request_error", msg, nil) }

func summaryResp(summary string) scripted {
	return scripted{body: sseMessage("end_turn", sseTextBlock("<summary>"+summary+"</summary>"))}
}

// convo is a history long enough to have something before its verbatim tail.
func convo() []api.Message {
	return []api.Message{user("first question"), asst(text("first answer")), user("go")}
}

func isCompactionBody(body string) bool {
	return strings.Contains(body, "summarizing a coding conversation")
}

// requestMaxTokens reads max_tokens from a recorded request body.
func requestMaxTokens(t *testing.T, body string) int {
	t.Helper()
	var r struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return r.MaxTokens
}

// requestKinds labels each recorded request "main" or "compaction".
func requestKinds(ss *scriptServer) []string {
	var kinds []string
	for i := 0; i < ss.calls(); i++ {
		if isCompactionBody(ss.request(i)) {
			kinds = append(kinds, "compaction")
		} else {
			kinds = append(kinds, "main")
		}
	}
	return kinds
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLoop_ReactiveCompaction(t *testing.T) {
	t.Setenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS", "1")
	lp, ss, _ := newScriptLoop(t, []scripted{overflowResp(promptTooLong), summaryResp("S1"), okResp("recovered")}, nil)
	ev := &eventLog{}
	out, err := lp.Run(context.Background(), convo(), ev.handle)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := requestKinds(ss), []string{"main", "compaction", "main"}; !equalStrings(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	if got := ss.headerLog[1].Get("x-claude-code-compaction"); got != "auto" {
		t.Errorf("compaction request hint = %q, want auto (CC has no reactive value there)", got)
	}
	if got := ss.headerLog[0].Get("x-claude-code-context-compacted"); got != "" {
		t.Errorf("first request carries context-compacted %q", got)
	}
	if got := ss.headerLog[2].Get("x-claude-code-context-compacted"); got != "reactive" {
		t.Errorf("resent request context-compacted = %q, want reactive", got)
	}
	if !strings.Contains(ss.request(2), "S1") {
		t.Errorf("resent request does not carry the summary: %s", ss.request(2))
	}
	if lastText(t, out) != "recovered" {
		t.Errorf("final reply = %q", lastText(t, out))
	}
	if !strings.Contains(out[0].Content[0].Text, "S1") {
		t.Errorf("history starts with %q, want the summary message", out[0].Content[0].Text)
	}

	compacted := ev.ofType(EventCompacted)
	if len(compacted) != 1 {
		t.Fatalf("EventCompacted count = %d, want 1", len(compacted))
	}
	c := compacted[0]
	if c.CompactKind != compact.KindReactive || c.CompactSummary != "S1" || len(c.History) == 0 || c.CompactTailLen < 1 {
		t.Errorf("EventCompacted = kind %q summary %q history %d tail %d", c.CompactKind, c.CompactSummary, len(c.History), c.CompactTailLen)
	}
	var notice bool
	for _, n := range ev.ofType(EventNotice) {
		if strings.Contains(n.Notice, "Context window exceeded (210000 > 200000)") {
			notice = true
		}
	}
	if !notice {
		t.Error("no 'Context window exceeded (210000 > 200000)' notice")
	}
}

func TestLoop_ReactiveCompaction_SecondOverflowSurfaces(t *testing.T) {
	lp, ss, _ := newScriptLoop(t, []scripted{overflowResp(promptTooLong), summaryResp("S1"), overflowResp(promptTooLong), okResp("never")}, nil)
	_, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle)
	if err == nil || api.Classify(err).Class != api.ClassContextOverflow {
		t.Fatalf("err = %v, want the context overflow", err)
	}
	if got, want := requestKinds(ss), []string{"main", "compaction", "main"}; !equalStrings(got, want) {
		t.Errorf("requests = %v, want %v (reactive compaction runs once per Run)", got, want)
	}
}

func TestLoop_MaxTokensOverflow(t *testing.T) {
	const vllmAlone = "max_tokens=16000 cannot be greater than max_model_len=8192. Please request fewer output tokens."
	const vllmCounts = "This model's maximum context length is 8192 tokens. However, you requested 8192 output tokens and your prompt contains 5000 input tokens"
	const openAICap = "max_tokens is too large: 65536. This model supports at most 32768 completion tokens, whereas you provided 65536."

	tests := []struct {
		name      string
		maxTokens int
		script    []scripted
		wantKinds []string
		wantMax   []int // max_tokens of each main request, in order
		wantErr   bool
	}{
		{
			name:      "clamps to limit - input - 1000",
			maxTokens: 21333,
			script:    []scripted{overflowResp(maxTokensCounts), okResp("ok")},
			wantKinds: []string{"main", "main"},
			wantMax:   []int{21333, 4000},
		},
		{
			name:      "floor of 3000",
			maxTokens: 21333,
			script:    []scripted{overflowResp("input length and `max_tokens` exceed context limit: 199500 + 21333 > 200000"), okResp("ok")},
			wantKinds: []string{"main", "main"},
			wantMax:   []int{21333, 3000},
		},
		{
			name:      "prompt alone over the limit skips the clamp and compacts",
			maxTokens: 21333,
			script:    []scripted{overflowResp("input length and `max_tokens` exceed context limit: 210000 + 21333 > 200000"), summaryResp("S"), okResp("ok")},
			wantKinds: []string{"main", "compaction", "main"},
			wantMax:   []int{21333, 21333},
		},
		{
			name:      "output cap in the limit clamps to the cap",
			maxTokens: 65536,
			script:    []scripted{overflowResp(openAICap), okResp("ok")},
			wantKinds: []string{"main", "main"},
			wantMax:   []int{65536, 32768},
		},
		{
			name:      "clamp that would not lower max_tokens compacts",
			maxTokens: 1024,
			script:    []scripted{overflowResp(maxTokensCounts), summaryResp("S"), okResp("ok")},
			wantKinds: []string{"main", "compaction", "main"},
			wantMax:   []int{1024, 1024},
		},
		{
			name:      "second clamp when the follow-up reports counts",
			maxTokens: 16000,
			script:    []scripted{overflowResp(vllmAlone), overflowResp(vllmCounts), okResp("ok")},
			wantKinds: []string{"main", "main", "main"},
			wantMax:   []int{16000, 8192, 3000},
		},
		{
			name:      "at most two clamps per request",
			maxTokens: 16000,
			script:    []scripted{overflowResp(vllmAlone), overflowResp(vllmCounts), overflowResp(vllmCounts), summaryResp("S"), okResp("ok")},
			wantKinds: []string{"main", "main", "main", "compaction", "main"},
			wantMax:   []int{16000, 8192, 3000, 3000},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp, ss, _ := newScriptLoop(t, tt.script, func(c *LoopConfig) { c.MaxTokens = tt.maxTokens })
			_, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Run err = %v, wantErr %v", err, tt.wantErr)
			}
			if got := requestKinds(ss); !equalStrings(got, tt.wantKinds) {
				t.Fatalf("requests = %v, want %v", got, tt.wantKinds)
			}
			var gotMax []int
			for i, k := range requestKinds(ss) {
				if k == "main" {
					gotMax = append(gotMax, requestMaxTokens(t, ss.request(i)))
				}
			}
			if len(gotMax) != len(tt.wantMax) {
				t.Fatalf("main max_tokens = %v, want %v", gotMax, tt.wantMax)
			}
			for i := range gotMax {
				if gotMax[i] != tt.wantMax[i] {
					t.Errorf("main max_tokens = %v, want %v", gotMax, tt.wantMax)
					break
				}
			}
		})
	}
}

func TestLoop_MaxTokensOverflow_ClampsThinkingBudget(t *testing.T) {
	tests := []struct {
		name       string
		budget     int
		wantBudget int // 0 means thinking is sent off
	}{
		{"budget below the new max_tokens is kept", 2000, 2000},
		{"budget above the new max_tokens drops below it", 20000, 3999},
		{"budget that cannot stay valid turns thinking off", 1000, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := []scripted{overflowResp(maxTokensCounts), okResp("ok")}
			lp, ss, _ := newScriptLoop(t, script, func(c *LoopConfig) {
				c.MaxTokens = 21333
				c.ThinkingBudget = tt.budget
			})
			if _, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle); err != nil {
				t.Fatalf("Run: %v", err)
			}
			var req struct {
				MaxTokens int `json:"max_tokens"`
				Thinking  *struct {
					BudgetTokens int `json:"budget_tokens"`
				} `json:"thinking"`
			}
			if err := json.Unmarshal([]byte(ss.request(1)), &req); err != nil {
				t.Fatal(err)
			}
			if req.MaxTokens != 4000 {
				t.Fatalf("max_tokens = %d, want 4000", req.MaxTokens)
			}
			got := 0
			if req.Thinking != nil {
				got = req.Thinking.BudgetTokens
			}
			if got != tt.wantBudget {
				t.Errorf("thinking budget = %d, want %d", got, tt.wantBudget)
			}
		})
	}
}

func TestLoop_ReactiveCompaction_SummarizerFailure(t *testing.T) {
	tests := []struct {
		name   string
		script []scripted
	}{
		{"summarizer rejected with 400", []scripted{overflowResp(promptTooLong), errResp(400, "invalid_request_error", "bad model", nil), okResp("ok")}},
		{"summarizer returns nothing", []scripted{overflowResp(promptTooLong), {body: sseMessage("end_turn", sseTextBlock(""))}, okResp("ok")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp, ss, _ := newScriptLoop(t, tt.script, nil)
			ev := &eventLog{}
			out, err := lp.Run(context.Background(), convo(), ev.handle)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got, want := requestKinds(ss), []string{"main", "compaction", "main"}; !equalStrings(got, want) {
				t.Fatalf("requests = %v, want %v", got, want)
			}
			if len(out) < 2 || lastText(t, out) != "ok" {
				t.Fatalf("history after recovery = %d messages, last %v; the history must not be wiped", len(out), out[len(out)-1])
			}
			var notice bool
			for _, n := range ev.ofType(EventNotice) {
				notice = notice || n.Notice == compact.FallbackNotice
			}
			if !notice {
				t.Errorf("no %q notice", compact.FallbackNotice)
			}
			if c := ev.ofType(EventCompacted); len(c) != 1 || len(c[0].History) == 0 {
				t.Errorf("EventCompacted = %+v, want one with a non-empty history", c)
			}
		})
	}
}

func TestLoop_ReactiveCompaction_DisabledByEnv(t *testing.T) {
	t.Setenv("DISABLE_AUTO_COMPACT", "1")
	lp, ss, _ := newScriptLoop(t, []scripted{overflowResp(promptTooLong), summaryResp("S"), okResp("never")}, nil)
	_, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle)
	if err == nil || api.Classify(err).Class != api.ClassContextOverflow {
		t.Fatalf("err = %v, want the context overflow", err)
	}
	if ss.calls() != 1 {
		t.Errorf("requests = %d, want 1 (no compaction)", ss.calls())
	}
}

func TestLoop_ReactiveCompaction_NothingToCompact(t *testing.T) {
	// A history that is all verbatim tail has nothing to summarize, so the
	// overflow surfaces instead of looping.
	lp, ss, _ := newScriptLoop(t, []scripted{overflowResp(promptTooLong), summaryResp("S"), okResp("never")}, nil)
	_, err := lp.Run(context.Background(), []api.Message{asst(text("only")), user("go")}, (&eventLog{}).handle)
	if err == nil || api.Classify(err).Class != api.ClassContextOverflow {
		t.Fatalf("err = %v, want the context overflow", err)
	}
	if ss.calls() != 1 {
		t.Errorf("requests = %d, want 1", ss.calls())
	}
}

func TestLoop_OverflowMicrocompactsFirst(t *testing.T) {
	big := strings.Repeat("x", 6000)
	msgs := make([]api.Message, 0, 18)
	msgs = append(msgs, user("start"))
	for i := range 8 {
		id := "toolu_" + string(rune('a'+i))
		msgs = append(msgs,
			asst(api.ContentBlock{Type: "tool_use", ID: id, Name: "Read", Input: map[string]any{"file_path": "/f"}}),
			api.Message{Role: "user", Content: []api.ContentBlock{{Type: "tool_result", ToolUseID: id, ResultContent: big}}})
	}
	msgs = append(msgs, user("go"))

	// A small window keeps the recency budget of tool results small.
	lp, ss, _ := newScriptLoop(t, []scripted{overflowResp(promptTooLong), okResp("ok")}, func(c *LoopConfig) { c.ContextWindow = 20_000 })
	if _, err := lp.Run(context.Background(), msgs, (&eventLog{}).handle); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := requestKinds(ss), []string{"main", "main"}; !equalStrings(got, want) {
		t.Fatalf("requests = %v, want %v (microcompact recovers before a summary)", got, want)
	}
	if len(ss.request(1)) >= len(ss.request(0)) {
		t.Errorf("resent request is %d bytes, not smaller than %d", len(ss.request(1)), len(ss.request(0)))
	}
}

func TestLoop_ContextWindowStopReasonCompacts(t *testing.T) {
	exceeded := scripted{body: sseMessage("model_context_window_exceeded", sseTextBlock("partial"))}
	lp, ss, _ := newScriptLoop(t, []scripted{exceeded, summaryResp("S"), okResp("done")}, nil)
	out, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := requestKinds(ss), []string{"main", "compaction", "main"}; !equalStrings(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	if lastText(t, out) != "done" {
		t.Errorf("final reply = %q", lastText(t, out))
	}
}

func TestLoop_ContextWindowStopReasonTwiceFails(t *testing.T) {
	exceeded := scripted{body: sseMessage("model_context_window_exceeded", sseTextBlock("partial"))}
	lp, _, _ := newScriptLoop(t, []scripted{exceeded, summaryResp("S"), exceeded}, nil)
	_, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle)
	if !errors.Is(err, ErrContextWindowExceeded) {
		t.Fatalf("err = %v, want ErrContextWindowExceeded", err)
	}
}

func TestLoop_ReactiveCompaction_OncePerRun(t *testing.T) {
	// After the first compaction the model calls a tool whose result is large,
	// and the next request overflows again. The history now has something to
	// summarize, so only the once-per-Run rule stops a second compaction.
	toolTurn := scripted{body: sseMessage("tool_use", sseToolBlock(0, "toolu_1", "Echo", `{}`, true))}
	ss := &scriptServer{script: []scripted{
		overflowResp(promptTooLong), summaryResp("S1"), toolTurn,
		overflowResp(promptTooLong), summaryResp("S2"), okResp("never"),
	}}
	srv := httptest.NewServer(ss)
	defer srv.Close()
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Echo", result: strings.Repeat("r", 3000)})
	lp := NewLoop(api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client()), reg, LoopConfig{
		Model: "m", MaxTokens: 1024, MaxTurns: 10,
		ContextWindow: 2000, // a verbatim tail of 400 tokens, so older turns can be summarized
		Sleep:         func(context.Context, time.Duration) error { return nil },
	})
	msgs := []api.Message{user("first question"), asst(text(strings.Repeat("a", 1500))), user("go")}
	_, err := lp.Run(context.Background(), msgs, (&eventLog{}).handle)
	if err == nil || api.Classify(err).Class != api.ClassContextOverflow {
		t.Fatalf("err = %v, want the second overflow to surface", err)
	}
	if got, want := requestKinds(ss), []string{"main", "compaction", "main", "main"}; !equalStrings(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

func TestLoop_MaxTokensOverflow_ClampLimit(t *testing.T) {
	counts := func(limit, input int) string {
		return fmt.Sprintf("This model's maximum context length is %d tokens. However, you requested 20000 output tokens and your prompt contains %d input tokens", limit, input)
	}
	// Three overflows, each allowing a smaller max_tokens: only two clamps are
	// made per request, the third goes to compaction.
	script := []scripted{
		overflowResp(counts(12000, 5000)), overflowResp(counts(10000, 5000)), overflowResp(counts(9000, 5000)),
		summaryResp("S"), okResp("ok"),
	}
	lp, ss, _ := newScriptLoop(t, script, func(c *LoopConfig) { c.MaxTokens = 20000 })
	if _, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := requestKinds(ss), []string{"main", "main", "main", "compaction", "main"}; !equalStrings(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	var gotMax []int
	for i, k := range requestKinds(ss) {
		if k == "main" {
			gotMax = append(gotMax, requestMaxTokens(t, ss.request(i)))
		}
	}
	if want := []int{20000, 6000, 4000, 4000}; !equalInts(gotMax, want) {
		t.Errorf("main max_tokens = %v, want %v", gotMax, want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A lone oversized prompt has no assistant message, so the whole history is
// the part to summarize and the request that must be resent would lose its
// prompt. The overflow surfaces (its error text points at /compact and /clear).
func TestLoop_ReactiveCompaction_LonePromptIsNothingToCompact(t *testing.T) {
	lp, ss, _ := newScriptLoop(t, []scripted{overflowResp(promptTooLong), summaryResp("S"), okResp("never")}, nil)
	_, err := lp.Run(context.Background(), []api.Message{user("one enormous prompt")}, (&eventLog{}).handle)
	if err == nil || api.Classify(err).Class != api.ClassContextOverflow {
		t.Fatalf("err = %v, want the context overflow", err)
	}
	if ss.calls() != 1 {
		t.Errorf("requests = %d, want 1 (no summarizer call, no resend)", ss.calls())
	}
}

// A cancel that lands while the summary is being produced must not persist or
// announce a compaction of a history the user has left.
func TestLoop_ReactiveCompaction_CancelledAfterSummary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lp, ss, _ := newScriptLoop(t, []scripted{overflowResp(promptTooLong), summaryResp("S"), okResp("never")}, func(c *LoopConfig) {
		// Runs after the summary came back and before the loop acts on it.
		c.OnSubAgentUsage = func(string, api.Usage) { cancel() }
	})
	ev := &eventLog{}
	_, err := lp.Run(ctx, convo(), ev.handle)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a cancel", err)
	}
	if got := ev.ofType(EventCompacted); len(got) != 0 {
		t.Errorf("EventCompacted after the cancel: %+v", got)
	}
	if ss.calls() != 2 {
		t.Errorf("requests = %d, want 2 (no resend)", ss.calls())
	}
}
