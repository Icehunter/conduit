package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/provider/copilot"
	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

// TestNewProviderAPIClient_OpenAICompatibleThreadsExtraBody proves a local
// openai-compatible provider's ExtraBody (e.g. LM Studio/llama.cpp's
// chat_template_kwargs) reaches the outgoing request, not just the client
// config struct — the end-to-end path a local role would take.
func TestNewProviderAPIClient_OpenAICompatibleThreadsExtraBody(t *testing.T) {
	var capturedBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","model":"qwen3.8-27b","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := secure.NewMemoryStorage()
	if err := settings.SaveProviderCredential(store, "local-lmstudio", "not-needed"); err != nil {
		t.Fatalf("SaveProviderCredential: %v", err)
	}

	provider := settings.ActiveProviderSettings{
		Kind:       settings.ProviderKindOpenAICompatible,
		Model:      "qwen3.8-27b",
		Credential: "local-lmstudio",
		BaseURL:    srv.URL,
		ExtraBody: map[string]any{
			"chat_template_kwargs": map[string]any{"enable_thinking": false},
		},
	}
	client, err := NewProviderAPIClient(provider, store, "0.0.0")
	if err != nil {
		t.Fatalf("NewProviderAPIClient: %v", err)
	}

	resp, err := client.CreateMessage(context.Background(), &api.MessageRequest{
		Model:     "qwen3.8-27b",
		MaxTokens: 8,
		Messages:  []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text != "ok" {
		t.Fatalf("response content = %#v, want ok text", resp.Content)
	}
	kwargs, ok := capturedBody["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != false {
		t.Fatalf("chat_template_kwargs = %#v, want enable_thinking=false", capturedBody["chat_template_kwargs"])
	}
}

func TestCopilotConfig_Routes(t *testing.T) {
	tests := []struct {
		model     string
		kind      string
		baseURL   string
		stripTags bool
	}{
		{"gpt-5", api.ProviderKindOpenAIResponses, "https://example.test", false},
		{"claude-sonnet-4.5", "", copilot.ChatBaseURL, true},
		{"gpt-4.1", api.ProviderKindOpenAICompatible, "https://example.test", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			cfg := copilotConfig(tt.model, "https://example.test", "ua", nil)
			if cfg.ProviderKind != tt.kind || cfg.BaseURL != tt.baseURL || cfg.StripCacheControlScope != tt.stripTags {
				t.Errorf("cfg = {%q %q %v}, want {%q %q %v}", cfg.ProviderKind, cfg.BaseURL, cfg.StripCacheControlScope, tt.kind, tt.baseURL, tt.stripTags)
			}
			if cfg.RequestHeaders == nil {
				t.Fatal("RequestHeaders unset: x-initiator would never be sent")
			}
			for k := range cfg.ExtraHeaders {
				if strings.EqualFold(k, "x-initiator") {
					t.Errorf("static ExtraHeaders still carry %s", k)
				}
			}
			h := cfg.RequestHeaders(&api.MessageRequest{
				Hints:    &api.WireHints{RequestClass: "main", SessionID: "s1", UserTurn: true},
				Messages: []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}}},
			})
			if h["x-initiator"] != "user" || h["X-Interaction-Id"] != "s1" {
				t.Errorf("RequestHeaders = %v, want user initiator and the session id", h)
			}
		})
	}
}

// TestNewProviderAPIClient_CopilotRequestHeaders drives the Copilot chat and
// Responses routes end to end: a main-loop prompt is user-initiated, a tool
// follow-up is agent-initiated, and both carry the session's X-Interaction-Id.
// A plain openai-compatible provider sends neither header.
func TestNewProviderAPIClient_CopilotRequestHeaders(t *testing.T) {
	var mu sync.Mutex
	var got []http.Header
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request, body string) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"id":"c","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	})
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"id":"r","model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := secure.NewMemoryStorage()
	if err := settings.SaveStructuredProviderCredential(store, copilot.ProviderID, &providerauth.ProviderCredential{
		AccessToken: "copilot-token",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := settings.SaveProviderCredential(store, "plain", "key"); err != nil {
		t.Fatal(err)
	}

	prompt := []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "fix it"}}}}
	followUp := append(append([]api.Message{}, prompt...),
		api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "tool_use", ID: "call_1", Name: "Read", Input: map[string]any{}}}},
		api.Message{Role: "user", Content: []api.ContentBlock{{Type: "tool_result", ToolUseID: "call_1", ResultContent: "file"}}},
	)
	tests := []struct {
		name          string
		provider      settings.ActiveProviderSettings
		wantInitiator []string // per request: prompt, follow-up
		wantSession   string
	}{
		{"copilot chat", settings.ActiveProviderSettings{Kind: settings.ProviderKindOpenAICompatible, Model: "gpt-4.1", Credential: copilot.ProviderID, BaseURL: srv.URL},
			[]string{"user", "agent"}, "sess-9"},
		{"copilot responses", settings.ActiveProviderSettings{Kind: settings.ProviderKindOpenAICompatible, Model: "gpt-5", Credential: copilot.ProviderID, BaseURL: srv.URL},
			[]string{"user", "agent"}, "sess-9"},
		{"plain provider", settings.ActiveProviderSettings{Kind: settings.ProviderKindOpenAICompatible, Model: "gpt-4.1", Credential: "plain", BaseURL: srv.URL},
			[]string{"", ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu.Lock()
			got = nil
			mu.Unlock()
			client, err := NewProviderAPIClient(tt.provider, store, "0.0.0")
			if err != nil {
				t.Fatalf("NewProviderAPIClient: %v", err)
			}
			for i, msgs := range [][]api.Message{prompt, followUp} {
				if _, err := client.CreateMessage(context.Background(), &api.MessageRequest{
					Model:    tt.provider.Model,
					Messages: msgs,
					Hints:    &api.WireHints{RequestClass: "main", SessionID: "sess-9", UserTurn: i == 0},
				}); err != nil {
					t.Fatalf("CreateMessage: %v", err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) != 2 {
				t.Fatalf("requests = %d, want 2", len(got))
			}
			for i, h := range got {
				if v := h.Get("X-Initiator"); v != tt.wantInitiator[i] {
					t.Errorf("request %d x-initiator = %q, want %q", i+1, v, tt.wantInitiator[i])
				}
				if v := h.Get("X-Interaction-Id"); v != tt.wantSession {
					t.Errorf("request %d X-Interaction-Id = %q, want %q", i+1, v, tt.wantSession)
				}
			}
		})
	}
}

// TestNewProviderAPIClient_ThreadsReasoningReplay: the provider's
// reasoningReplay setting decides whether a local server gets the assistant
// turn's reasoning back.
func TestNewProviderAPIClient_ThreadsReasoningReplay(t *testing.T) {
	for _, tt := range []struct {
		mode string
		want bool
	}{{"", true}, {"off", false}} {
		t.Run("mode="+tt.mode, func(t *testing.T) {
			var body struct {
				Messages []map[string]any `json:"messages"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"c","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer srv.Close()
			store := secure.NewMemoryStorage()
			if err := settings.SaveProviderCredential(store, "local", "none"); err != nil {
				t.Fatal(err)
			}
			client, err := NewProviderAPIClient(settings.ActiveProviderSettings{
				Kind: settings.ProviderKindOpenAICompatible, Model: "qwen3", Credential: "local", BaseURL: srv.URL, ReasoningReplay: tt.mode,
			}, store, "0.0.0")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.CreateMessage(context.Background(), &api.MessageRequest{Model: "qwen3", Messages: []api.Message{
				{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}},
				{Role: "assistant", Content: []api.ContentBlock{{Type: "thinking", Thinking: "say hi"}, {Type: "text", Text: "hi"}}},
				{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "again"}}},
			}}); err != nil {
				t.Fatal(err)
			}
			if len(body.Messages) != 3 {
				t.Fatalf("messages = %v", body.Messages)
			}
			_, has := body.Messages[1]["reasoning_content"]
			if has != tt.want {
				t.Errorf("assistant reasoning_content present = %v, want %v (%v)", has, tt.want, body.Messages[1])
			}
		})
	}
}

// TestNewProviderAPIClient_ThreadsToolCallFormat: the provider's
// toolCallFormat setting moves the tool schemas from the request's tools
// field into the system prompt.
func TestNewProviderAPIClient_ThreadsToolCallFormat(t *testing.T) {
	for _, tt := range []struct {
		format   string
		wantTool bool
	}{{"", true}, {"hermes", false}} {
		t.Run("format="+tt.format, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"c","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer srv.Close()
			store := secure.NewMemoryStorage()
			if err := settings.SaveProviderCredential(store, "local", "none"); err != nil {
				t.Fatal(err)
			}
			client, err := NewProviderAPIClient(settings.ActiveProviderSettings{
				Kind: settings.ProviderKindOpenAICompatible, Model: "qwen3", Credential: "local", BaseURL: srv.URL, ToolCallFormat: tt.format,
			}, store, "0.0.0")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.CreateMessage(context.Background(), &api.MessageRequest{
				Model:    "qwen3",
				Tools:    []api.ToolDef{{Name: "Bash", InputSchema: map[string]any{"type": "object"}}},
				Messages: []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}}},
			}); err != nil {
				t.Fatal(err)
			}
			if _, has := body["tools"]; has != tt.wantTool {
				t.Errorf("tools field present = %v, want %v", has, tt.wantTool)
			}
		})
	}
}

// TestCodexConfig_SessionCacheAndReasoning drives the Codex config against a
// local server: session_id and prompt_cache_key follow the request's conduit
// session (the client's stable id without one), store=false asks for
// encrypted reasoning, and a replayed reasoning item goes back with it.
func TestCodexConfig_SessionCacheAndReasoning(t *testing.T) {
	type seen struct {
		header http.Header
		body   map[string]any
	}
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, seen{r.Header.Clone(), body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"r","model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer srv.Close()

	cfg := codexConfig(codexConfigParams{accessToken: "tok", accountID: "acct", wireVersion: "0.0.0", userAgent: "ua"})
	cfg.BaseURL = srv.URL
	client := api.NewClient(cfg, srv.Client())

	history := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: []api.ContentBlock{{Type: "thinking", Signature: "oai-resp:rs_1:ENC"}, {Type: "text", Text: "hello"}}},
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "again"}}},
	}
	tests := []struct {
		name  string
		hints *api.WireHints
		want  func(sid string) bool
	}{
		{"conduit session", &api.WireHints{SessionID: "sess-7"}, func(sid string) bool { return sid == "sess-7" }},
		{"no hints: stable client id", nil, func(sid string) bool { return sid != "" && sid != "sess-7" }},
	}
	var clientIDs []string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu.Lock()
			got = nil
			mu.Unlock()
			if _, err := client.CreateMessage(context.Background(), &api.MessageRequest{Model: "gpt-5.4", Messages: history, Hints: tt.hints}); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) != 1 {
				t.Fatalf("requests = %d", len(got))
			}
			sid, key := got[0].header.Get("session_id"), got[0].body["prompt_cache_key"]
			if !tt.want(sid) || key != sid {
				t.Errorf("session_id = %q, prompt_cache_key = %v", sid, key)
			}
			clientIDs = append(clientIDs, sid)
			if inc, _ := got[0].body["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
				t.Errorf("include = %v", got[0].body["include"])
			}
			if got[0].body["store"] != false {
				t.Errorf("store = %v", got[0].body["store"])
			}
			var types []string
			for _, it := range got[0].body["input"].([]any) {
				m := it.(map[string]any)
				if m["type"] != nil {
					types = append(types, m["type"].(string))
				} else {
					types = append(types, m["role"].(string))
				}
			}
			if strings.Join(types, ",") != "user,reasoning,assistant,user" {
				t.Errorf("input items = %v, want the reasoning item replayed before its assistant text", types)
			}
			if got[0].header.Get("ChatGPT-Account-Id") != "acct" {
				t.Errorf("account header = %q", got[0].header.Get("ChatGPT-Account-Id"))
			}
		})
	}
	// The fallback id is stable across requests of one client.
	for i := 0; i < 2; i++ {
		if _, err := client.CreateMessage(context.Background(), &api.MessageRequest{Model: "gpt-5.4", Messages: history}); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[1].header.Get("session_id") != got[2].header.Get("session_id") || got[1].header.Get("session_id") == "" {
		t.Errorf("fallback session id not stable: %v %v", got[1].header.Get("session_id"), got[2].header.Get("session_id"))
	}
}

// TestNewProviderAPIClient_CopilotBusinessAndEndpointRouting: a seat's
// endpoints.api replaces the default host, and the recorded supported_endpoints
// (not the model name) choose the wire.
func TestNewProviderAPIClient_CopilotBusinessAndEndpointRouting(t *testing.T) {
	tests := []struct {
		name     string
		meta     map[string]string
		model    string
		baseURL  string
		wantHost string
		wantKind string
	}{
		{"business seat, grok on /responses", map[string]string{"api_endpoint": "https://api.business.githubcopilot.com", "model_endpoints": `{"grok-4.5":["/chat/completions","/responses"]}`},
			"grok-4.5", copilot.ChatBaseURL, "api.business.githubcopilot.com", api.ProviderKindOpenAIResponses},
		{"individual: no metadata falls back to the name", nil, "gpt-4.1", copilot.ChatBaseURL, "api.githubcopilot.com", api.ProviderKindOpenAICompatible},
		{"business seat, claude on messages", map[string]string{"api_endpoint": "https://api.business.githubcopilot.com", "model_endpoints": `{"claude-sonnet-4.5":["/v1/messages"]}`},
			"claude-sonnet-4.5", copilot.ChatBaseURL, "api.business.githubcopilot.com", ""},
		{"user-chosen base URL is kept", map[string]string{"api_endpoint": "https://api.business.githubcopilot.com"},
			"gpt-4.1", "https://proxy.example.com", "proxy.example.com", api.ProviderKindOpenAICompatible},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := secure.NewMemoryStorage()
			if err := settings.SaveStructuredProviderCredential(store, copilot.ProviderID, &providerauth.ProviderCredential{
				AccessToken: "t", Expiry: time.Now().Add(time.Hour), Metadata: tt.meta,
			}); err != nil {
				t.Fatal(err)
			}
			client, err := NewProviderAPIClient(settings.ActiveProviderSettings{
				Kind: settings.ProviderKindOpenAICompatible, Model: tt.model, Credential: copilot.ProviderID, BaseURL: tt.baseURL,
			}, store, "0.0.0")
			if err != nil {
				t.Fatal(err)
			}
			if client.BaseHost() != tt.wantHost || client.ProviderKind() != tt.wantKind {
				t.Errorf("host %q kind %q, want %q %q", client.BaseHost(), client.ProviderKind(), tt.wantHost, tt.wantKind)
			}
		})
	}
}

func TestProviderHeaderTimeout(t *testing.T) {
	for in, want := range map[int]time.Duration{0: -1, -5: -1, 30: 30 * time.Second} {
		if got := providerHeaderTimeout(in); got != want {
			t.Errorf("providerHeaderTimeout(%d) = %v, want %v", in, got, want)
		}
	}
}
