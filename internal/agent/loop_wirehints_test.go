package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/provider/copilot"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/ttsr"
)

// hintRecorder is a test server that records the hint headers of every
// request and answers with the next scripted SSE body.
type hintRecorder struct {
	mu       sync.Mutex
	headers  []http.Header
	bodies   []string
	statuses []int // optional per-request HTTP status; 0 means 200
}

func (h *hintRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	h.mu.Lock()
	h.headers = append(h.headers, r.Header.Clone())
	n := len(h.headers)
	body := textOnlySSE("done")
	if n <= len(h.bodies) {
		body = h.bodies[n-1]
	}
	status := 0
	if n <= len(h.statuses) {
		status = h.statuses[n-1]
	}
	h.mu.Unlock()
	if status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(body))
}

func (h *hintRecorder) got() []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.headers
}

// TestLoop_WireHints_Root covers the main loop: class main and the prompt id
// on every request, prev-tool-durations only after a tool round, and the
// context-compacted flag only on the first request after a compaction.
func TestLoop_WireHints_Root(t *testing.T) {
	t.Setenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS", "1")
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Echo", result: "ok"})
	rec := &hintRecorder{bodies: []string{toolCallOnlySSE("Echo", "toolu_1"), textOnlySSE("done")}}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{Model: "m", MaxTokens: 1024})
	lp.MarkContextCompacted(compact.KindManual)

	if _, err := lp.Run(context.Background(), []api.Message{user("hi")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	hs := rec.got()
	if len(hs) != 2 {
		t.Fatalf("requests = %d, want 2", len(hs))
	}
	for i, h := range hs {
		if got := h.Get("x-claude-code-request-class"); got != "main" {
			t.Errorf("request %d class = %q, want main", i+1, got)
		}
		if h.Get("x-claude-code-agent-id") != "" || h.Get("x-claude-code-agent-type") != "" {
			t.Errorf("request %d: main loop must not send agent identity: %v", i+1, h)
		}
	}
	pid := hs[0].Get("x-claude-code-prompt-id")
	if !isCanonicalUUID(pid) || hs[1].Get("x-claude-code-prompt-id") != pid {
		t.Errorf("prompt ids = %q, %q; want the same UUID on both", pid, hs[1].Get("x-claude-code-prompt-id"))
	}
	if got := hs[0].Get("x-claude-code-context-compacted"); got != "manual" {
		t.Errorf("request 1 context-compacted = %q, want manual", got)
	}
	// The legacy x-cc-* pair is gated on a first-party base URL only (CC's
	// yl()); the env override doesn't reach it.
	if got := hs[0].Get("x-cc-context-compacted"); got != "" {
		t.Errorf("request 1 x-cc-context-compacted = %q, want none off api.anthropic.com", got)
	}
	if got := hs[1].Get("x-claude-code-context-compacted"); got != "" {
		t.Errorf("request 2 context-compacted = %q, want it consumed", got)
	}
	if got := hs[0].Get("x-claude-code-prev-tool-durations"); got != "" {
		t.Errorf("request 1 prev-tool-durations = %q, want none", got)
	}
	if got := hs[1].Get("x-claude-code-prev-tool-durations"); !strings.HasPrefix(got, "Echo=") || strings.Contains(got, ";") {
		t.Errorf("request 2 prev-tool-durations = %q, want one Echo entry", got)
	}
}

// TestLoop_WireHints_Child covers a Task sub-agent: class subagent, its agent
// type and id, and no parent id when spawned by the root loop.
func TestLoop_WireHints_Child(t *testing.T) {
	t.Setenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS", "1")
	rec := &hintRecorder{}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	parent := NewLoop(c, tool.NewRegistry(), LoopConfig{Model: "m", MaxTokens: 1024})
	parent.MarkContextCompacted(compact.KindAuto) // must not leak into the child

	if _, err := parent.RunSubAgentTyped(context.Background(), "go", SubAgentSpec{AgentType: "custom"}); err != nil {
		t.Fatalf("RunSubAgentTyped: %v", err)
	}
	hs := rec.got()
	if len(hs) != 1 {
		t.Fatalf("requests = %d, want 1", len(hs))
	}
	h := hs[0]
	want := map[string]string{
		"x-claude-code-request-class":     "subagent",
		"x-claude-code-agent-type":        "custom",
		"x-claude-code-parent-agent-id":   "",
		"x-claude-code-context-compacted": "",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if id := h.Get("x-claude-code-agent-id"); !strings.HasPrefix(id, "sub-") {
		t.Errorf("x-claude-code-agent-id = %q, want a sub- id", id)
	}
}

func TestSubAgentSpec_RequestClass(t *testing.T) {
	tests := []struct {
		name string
		spec SubAgentSpec
		want string
	}{
		{"task", SubAgentSpec{}, "subagent"},
		{"workflow node", SubAgentSpec{Workflow: true}, "workflow"},
		{"background review", SubAgentSpec{Background: true}, "auxiliary"},
		{"background wins over workflow", SubAgentSpec{Background: true, Workflow: true}, "auxiliary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.requestClass(); got != tt.want {
				t.Errorf("requestClass() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoop_MarkContextCompacted_IgnoredOnChild(t *testing.T) {
	lp := &Loop{}
	lp.setWireIdentity("sub-1", "", "", requestClassSubagent)
	lp.MarkContextCompacted(compact.KindAuto)
	if h := lp.wireHints("", "", lp.takeContextCompacted(), false); h.ContextCompacted != "" || h.RequestClass != "subagent" {
		t.Errorf("hints = %+v, want subagent class and no context-compacted", h)
	}
}

// TestLoop_WireHints_CompactedFlagSurvivesRetry: when the first request after
// a compaction fails and the loop retries, the retry still carries the flag
// (CC re-adds it on every attempt); it clears only after a response drains.
func TestLoop_WireHints_CompactedFlagSurvivesRetry(t *testing.T) {
	t.Setenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS", "1")
	rec := &hintRecorder{statuses: []int{http.StatusInternalServerError}}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	noWait := func(context.Context, time.Duration) error { return nil }
	lp := NewLoop(c, tool.NewRegistry(), LoopConfig{Model: "m", MaxTokens: 1024, Sleep: noWait})
	lp.MarkContextCompacted(compact.KindAuto)
	if _, err := lp.Run(context.Background(), []api.Message{user("hi")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := lp.Run(context.Background(), []api.Message{user("again")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	hs := rec.got()
	if len(hs) != 3 {
		t.Fatalf("requests = %d, want 3 (failed, retried, next prompt)", len(hs))
	}
	for i, want := range []string{"auto", "auto", ""} {
		if got := hs[i].Get("x-claude-code-context-compacted"); got != want {
			t.Errorf("request %d context-compacted = %q, want %q", i+1, got, want)
		}
	}
}

func TestLoop_ForgetContextCompacted(t *testing.T) {
	tests := []struct {
		name   string
		forget func(*Loop)
		want   string
	}{
		{"kept", func(*Loop) {}, "manual"},
		{"forget", (*Loop).ForgetContextCompacted, ""},
		{"session switch", func(l *Loop) { l.SetSessionID("other") }, ""},
		{"same session", func(l *Loop) { l.SetSessionID("s1") }, "manual"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp := &Loop{cfg: LoopConfig{SessionID: "s1"}}
			lp.MarkContextCompacted(compact.KindManual)
			tt.forget(lp)
			if got := lp.takeContextCompacted(); got != tt.want {
				t.Errorf("pending = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoop_WireHints_SessionID(t *testing.T) {
	lp := &Loop{cfg: LoopConfig{SessionID: "s1"}}
	if h := lp.wireHints("", "", "", false); h.SessionID != "s1" {
		t.Errorf("root SessionID = %q, want s1", h.SessionID)
	}
	lp.SetSessionID("s2")
	if h := lp.wireHints("", "", "", false); h.SessionID != "s2" {
		t.Errorf("after SetSessionID SessionID = %q, want s2", h.SessionID)
	}
	child := &Loop{cfg: lp.cfg}
	child.setWireIdentity("sub-1", "", "custom", requestClassSubagent)
	if h := child.wireHints("", "", "", false); h.SessionID != "s2" || h.RequestClass != "subagent" {
		t.Errorf("child hints = %+v, want the parent's session and class subagent", h)
	}
}

// openAIChatRecorder answers /chat/completions with scripted SSE bodies and
// records every request's headers.
type openAIChatRecorder struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []string
}

func (h *openAIChatRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	h.mu.Lock()
	h.headers = append(h.headers, r.Header.Clone())
	body := chatTextSSE("done")
	if n := len(h.headers); n <= len(h.bodies) {
		body = h.bodies[n-1]
	}
	h.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, body)
}

func (h *openAIChatRecorder) got() []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.headers
}

func chatTextSSE(text string) string {
	return `data: {"choices":[{"delta":{"content":"` + text + `"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
}

func chatToolCallSSE(name, id string) string {
	return `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"` + id + `","type":"function","function":{"name":"` + name + `","arguments":"{}"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
}

// TestLoop_CopilotRequestHeaders runs the loop against a Copilot-configured
// client: only the prompt itself is user-initiated; the tool follow-up, a
// sub-agent and compaction are agent-initiated, and the loop's requests share
// the session's X-Interaction-Id.
func TestLoop_CopilotRequestHeaders(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Echo", result: "ok"})
	rec := &openAIChatRecorder{bodies: []string{
		chatToolCallSSE("Echo", "call_1"),
		chatTextSSE("done"),
		chatTextSSE("sub done"),
		chatTextSSE("<summary>s</summary>"),
	}}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c := api.NewClient(api.Config{
		ProviderKind:   api.ProviderKindOpenAICompatible,
		BaseURL:        srv.URL,
		APIKey:         "k",
		RequestHeaders: copilot.RequestHeaders,
	}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{Model: "gpt-4.1", MaxTokens: 1024, SessionID: "sess-1"})
	msgs, err := lp.Run(context.Background(), []api.Message{user("hi")}, func(LoopEvent) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := lp.RunSubAgentTyped(context.Background(), "go", SubAgentSpec{}); err != nil {
		t.Fatalf("RunSubAgentTyped: %v", err)
	}
	if _, err := compact.CompactWithModel(context.Background(), c, "gpt-4.1", msgs, "", compact.KindAuto); err != nil {
		t.Fatalf("CompactWithModel: %v", err)
	}

	hs := rec.got()
	want := []struct{ initiator, session string }{
		{"user", "sess-1"},  // the prompt
		{"agent", "sess-1"}, // tool follow-up
		{"agent", "sess-1"}, // sub-agent
		{"agent", ""},       // compaction (its hints carry no session id)
	}
	if len(hs) != len(want) {
		t.Fatalf("requests = %d, want %d", len(hs), len(want))
	}
	for i, w := range want {
		if got := hs[i].Get("X-Initiator"); got != w.initiator {
			t.Errorf("request %d x-initiator = %q, want %q", i+1, got, w.initiator)
		}
		if got := hs[i].Get("X-Interaction-Id"); got != w.session {
			t.Errorf("request %d X-Interaction-Id = %q, want %q", i+1, got, w.session)
		}
	}
}

// hookTool runs fn when executed, so a test can queue messages mid-turn.
type hookTool struct {
	fakeTool
	fn func()
}

func (h *hookTool) Execute(ctx context.Context, in json.RawMessage) (tool.Result, error) {
	h.fn()
	return h.fakeTool.Execute(ctx, in)
}

// TestLoop_CopilotInitiatorForInjectedAndSteeringMessages: text conduit
// injects as a user turn (team mailbox delivery) is agent-initiated, while a
// steering message the user typed is user-initiated.
func TestLoop_CopilotInitiatorForInjectedAndSteeringMessages(t *testing.T) {
	tests := []struct {
		name  string
		queue func(*Loop)
		want  []string
	}{
		{"team delivery", func(l *Loop) { l.InjectMessage(`<team-completion from="w">done</team-completion>`) }, []string{"user", "agent"}},
		{"steering", func(l *Loop) { l.InjectSteerMessage("use grep instead") }, []string{"user", "user"}},
		{"both", func(l *Loop) {
			l.InjectMessage(`<team-idle from="w" />`)
			l.InjectSteerMessage("use grep instead")
		}, []string{"user", "user"}},
		{"neither", func(*Loop) {}, []string{"user", "agent"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &openAIChatRecorder{bodies: []string{chatToolCallSSE("Echo", "call_1"), chatTextSSE("done")}}
			srv := httptest.NewServer(rec)
			defer srv.Close()
			c := api.NewClient(api.Config{
				ProviderKind:   api.ProviderKindOpenAICompatible,
				BaseURL:        srv.URL,
				APIKey:         "k",
				RequestHeaders: copilot.RequestHeaders,
			}, srv.Client())
			reg := tool.NewRegistry()
			var lp *Loop
			reg.Register(&hookTool{fakeTool: fakeTool{name: "Echo", result: "ok"}, fn: func() { tt.queue(lp) }})
			lp = NewLoop(c, reg, LoopConfig{Model: "gpt-4.1", MaxTokens: 1024, SessionID: "sess-1"})
			if _, err := lp.Run(context.Background(), []api.Message{user("hi")}, func(LoopEvent) {}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			hs := rec.got()
			if len(hs) != len(tt.want) {
				t.Fatalf("requests = %d, want %d", len(hs), len(tt.want))
			}
			for i, w := range tt.want {
				if got := hs[i].Get("X-Initiator"); got != w {
					t.Errorf("request %d x-initiator = %q, want %q", i+1, got, w)
				}
			}
		})
	}
}

// A TTSR correction is injected by conduit, so the retry it triggers is not a
// user turn.
func TestLoop_TTSRRetryIsNotUserTurn(t *testing.T) {
	rec := &openAIChatRecorder{bodies: []string{chatTextSSE("I will REWRITE_TRIGGER everything"), chatTextSSE("ok")}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	var mu sync.Mutex
	var userTurns []bool
	c := api.NewClient(api.Config{
		ProviderKind: api.ProviderKindOpenAICompatible,
		BaseURL:      srv.URL,
		APIKey:       "k",
		RequestHeaders: func(req *api.MessageRequest) map[string]string {
			mu.Lock()
			defer mu.Unlock()
			userTurns = append(userTurns, req.Hints.UserTurn)
			return copilot.RequestHeaders(req)
		},
	}, srv.Client())
	lp := NewLoop(c, tool.NewRegistry(), LoopConfig{Model: "gpt-4.1", MaxTokens: 1024, SessionID: "sess-1"})
	lp.SetTTSRRules([]ttsr.Rule{{Name: "r", Pattern: regexp.MustCompile(`REWRITE_TRIGGER`), Correction: "targeted only", MaxFires: 1}})
	if _, err := lp.Run(context.Background(), []api.Message{user("hi")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(userTurns) != 2 || !userTurns[0] || userTurns[1] {
		t.Errorf("UserTurn per request = %v, want [true false]", userTurns)
	}
	hs := rec.got()
	if len(hs) != 2 || hs[0].Get("X-Initiator") != "user" || hs[1].Get("X-Initiator") != "agent" {
		t.Errorf("initiators = %v", hs)
	}
}
