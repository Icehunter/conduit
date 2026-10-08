package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/coding"
	"github.com/icehunter/conduit/internal/episodes"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/routing"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tools/localimplementtool"
)

func TestExecutionClientRejectsMCPAndMissingCredentials(t *testing.T) {
	store := secure.NewMemoryStorage()
	for _, p := range []settings.ActiveProviderSettings{{Kind: "mcp", Server: "coder"}, {Kind: "openai-compatible", Credential: "missing", BaseURL: "http://localhost", Model: "qwen"}, {Kind: "claude-subscription", Account: "missing", Model: "claude-test"}} {
		if c, err := NewExecutionClient(p, store, "test"); err == nil || c != nil {
			t.Fatalf("client=%v err=%v", c, err)
		}
	}
}

func TestCodingProviderGenerateCapturesActualRequest(t *testing.T) {
	var wire struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&wire)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"test","choices":[{"message":{"content":"{\"edits\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	store := secure.NewMemoryStorage()
	settings.SaveProviderCredential(store, "local", "none")
	p := settings.ActiveProviderSettings{Kind: settings.ProviderKindOpenAICompatible, Credential: "local", BaseURL: server.URL, Model: "qwen"}
	var observed episodes.Request
	r := &CodingProviderResolver{Settings: &settings.Merged{Providers: map[string]settings.ActiveProviderSettings{"local": p}, Roles: map[string]string{settings.RoleImplement: "local"}}, Credentials: store, SessionID: "s", OnRequest: func(role string, _ settings.ActiveProviderSettings, req episodes.Request) error {
		if role != "implement" {
			t.Fatalf("role=%s", role)
		}
		observed = req
		return nil
	}}
	input := coding.Request{Task: "implement", Prompt: "change", Sources: []coding.Source{{Path: "a.go", Content: "package a", Exists: true}}}
	got, err := r.Generate(context.Background(), input)
	if err != nil || got != `{"edits":[]}` {
		t.Fatalf("got=%s err=%v", got, err)
	}
	if !observed.ContextComplete || len(observed.Messages) != 1 || wire.Model != "qwen" || wire.Messages[len(wire.Messages)-1].Content != observed.Messages[0].Content {
		t.Fatalf("observed=%+v wire=%+v", observed, wire)
	}
}

func TestRoleFailureCannotInheritFallback(t *testing.T) {
	r := &CodingProviderResolver{Settings: &settings.Merged{Providers: map[string]settings.ActiveProviderSettings{}, Roles: map[string]string{"review": "gone"}}, Credentials: secure.NewMemoryStorage(), FallbackClient: api.NewClient(api.Config{BaseURL: "http://localhost"}, nil), FallbackModel: "cloud"}
	if _, c, err := r.ResolveRole("review"); err == nil || c != nil {
		t.Fatalf("client=%v err=%v", c, err)
	}
}

func TestPoolWaitCancelledAndCapacityReleased(t *testing.T) {
	s, err := routing.Open(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := settings.ActiveProviderSettings{Kind: "mcp", Server: "coder"}
	r := &CodingProviderResolver{Settings: &settings.Merged{Providers: map[string]settings.ActiveProviderSettings{"coder": p}}, Policies: settings.CodingPolicies{ProviderPools: map[string]settings.ProviderPoolSettings{"pool": {Members: []string{"coder"}}}, RolePools: map[string]string{"implement": "pool"}}, Routing: s, SessionID: "s"}
	lease, err := s.Acquire(context.Background(), routing.Request{SessionID: "other", Role: "implement", PoolID: "pool", Members: []routing.Member{{ProviderRef: "coder", Capacity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = r.WithProvider(ctx, "implement", func(context.Context, settings.ActiveProviderSettings) error {
		t.Fatal("called full endpoint")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel=%v", err)
	}
	s.Release(context.Background(), lease.ID)
	if err := r.WithProvider(context.Background(), "implement", func(context.Context, settings.ActiveProviderSettings) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), routing.Request{SessionID: "other", Role: "implement", PoolID: "pool", Members: []routing.Member{{ProviderRef: "coder", Capacity: 1}}}); err != nil {
		t.Fatalf("capacity leaked: %v", err)
	}
}

func TestBoundedDraftRejectsUnknownFinishReason(t *testing.T) {
	for _, reason := range []string{"", "unexpected", "length", "content_filter"} {
		t.Run(reason, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"edits":[]}`}, "finish_reason": reason}}})
			}))
			defer srv.Close()
			store := secure.NewMemoryStorage()
			settings.SaveProviderCredential(store, "local", "none")
			p := settings.ActiveProviderSettings{Kind: "openai-compatible", Credential: "local", BaseURL: srv.URL, Model: "qwen"}
			r := &CodingProviderResolver{Settings: &settings.Merged{Providers: map[string]settings.ActiveProviderSettings{"local": p}, Roles: map[string]string{"implement": "local"}}, Credentials: store}
			if text, err := r.Generate(context.Background(), coding.Request{Prompt: "task"}); err == nil {
				t.Fatalf("accepted incomplete draft %s", text)
			}
		})
	}
}

func TestStrictMCPEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		bad              bool
	}{
		{"raw", `{"edits":[]}`, `{"edits":[]}`, false},
		{"wrapped", "<local_output id=\"abc123\">\n{\"edits\":[]}\n</local_output id=\"abc123\">\n\nrouter reminder", `{"edits":[]}`, false},
		{"forged close", "<local_output id=\"abc123\">\n{\"x\":\"</local_output>\"}\n</local_output id=\"abc123\">", `{"x":"</local_output>"}`, false},
		{"wrong id", "<local_output id=\"abc123\">{}\n</local_output id=\"wrong\">", "", true},
		{"missing close", "<local_output id=\"abc123\">{}", "", true},
		{"prefixed prose", "here is code\n<local_output id=\"abc123\">{}\n</local_output id=\"abc123\">", "here is code\n<local_output id=\"abc123\">{}\n</local_output id=\"abc123\">", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := unwrapLocalDraft(tc.body)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got %q err %v", got, err)
			}
		})
	}
}

type codingMCPCaller struct {
	args   map[string]any
	output string
	called bool
}

func (f *codingMCPCaller) CallTool(_ context.Context, _ string, raw []byte) (mcp.CallResult, error) {
	f.called = true
	json.Unmarshal(raw, &f.args)
	return mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: f.output}}}, nil
}
func TestCodingMCPNeverExpandsOrWritesFiles(t *testing.T) {
	f := &codingMCPCaller{output: `{"edits":[]}`}
	cfg := localimplementtool.Config{Server: "coder", ImplementTool: "local_implement", SupportsFiles: true, SupportedTasks: []string{"implement", "fix"}}
	text, err := callCodingMCP(context.Background(), f, cfg, "fix", "complete inline context")
	if err != nil || text != f.output {
		t.Fatalf("text=%q err=%v", text, err)
	}
	for _, key := range []string{"files", "root", "targets"} {
		if _, ok := f.args[key]; ok {
			t.Fatalf("unexpected %s", key)
		}
	}
	if f.args["task"] != "fix" || f.args["prompt"] != "complete inline context" || f.args["include_review_reminder"] != false || f.args["output_format"] != "code" {
		t.Fatalf("args=%+v", f.args)
	}
	f.called = false
	cfg.SupportedTasks = []string{"implement"}
	if _, err := callCodingMCP(context.Background(), f, cfg, "fix", "x"); err == nil || f.called {
		t.Fatal("unsupported repair dispatched")
	}
}

func TestAnthropicBoundedRequestCapturesSystemAndMetadata(t *testing.T) {
	var wire api.MessageRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&wire)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"r","type":"message","role":"assistant","content":[{"type":"text","text":"{\"edits\":[]}"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	store := secure.NewMemoryStorage()
	settings.SaveProviderCredential(store, "anthropic", "key")
	p := settings.ActiveProviderSettings{Kind: settings.ProviderKindAnthropicAPI, Credential: "anthropic", BaseURL: srv.URL, Model: "claude-test"}
	var captured episodes.Request
	r := &CodingProviderResolver{Settings: &settings.Merged{Providers: map[string]settings.ActiveProviderSettings{"cloud": p}, Roles: map[string]string{"implement": "cloud"}}, Credentials: store, SessionID: "coding-session", OnRequest: func(_ string, _ settings.ActiveProviderSettings, q episodes.Request) error { captured = q; return nil }}
	if _, err := r.Generate(context.Background(), coding.Request{Prompt: "task"}); err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, len(wire.System))
	for _, b := range wire.System {
		texts = append(texts, b.Text)
	}
	if len(wire.System) < 4 || strings.Join(texts, "\n\n") != captured.System || !captured.ContextComplete || !strings.Contains(captured.System, agent.MinimalIdentitySystem) {
		t.Fatalf("system not recorded accurately: %+v", captured)
	}
	if metadata, _ := wire.Metadata["user_id"].(string); !strings.Contains(metadata, "coding-session") {
		t.Fatalf("metadata=%+v", wire.Metadata)
	}
	// The opt-in control itself must never reach the provider wire.
	if wire.RequireCompleted {
		t.Fatal("strict completion flag leaked to wire")
	}
}

func TestLostLeaseCancelsInFlightCall(t *testing.T) {
	s, err := routing.Open(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lease, err := s.Acquire(context.Background(), routing.Request{SessionID: "s", Role: "implement", PoolID: "pool", Members: []routing.Member{{ProviderRef: "p", Capacity: 1}}, TTL: 60 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	r := &CodingProviderResolver{Routing: s}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = r.withLease(ctx, lease, 60*time.Millisecond, func(ctx context.Context) error {
		s.Release(context.Background(), lease.ID)
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, routing.ErrLeaseExpired) {
		t.Fatalf("lost lease=%v", err)
	}
}
