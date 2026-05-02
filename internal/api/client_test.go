// Package api: tests for the non-streaming /v1/messages call.
//
// We don't replicate every Stainless-SDK convenience header (X-Stainless-*),
// only the headers Anthropic's API actually validates — anthropic-version,
// anthropic-beta, Authorization, Content-Type — plus the Claude Code
// identification headers (x-app, User-Agent, X-Claude-Code-Session-Id).
// References: decoded/0168.js (SDK base), decoded/0158.js (Messages create),
// src/services/api/client.ts in the leaked TS for the cli-specific headers.
package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateMessage_RequestShape(t *testing.T) {
	var capturedHeader http.Header
	var capturedBody map[string]any
	var capturedPath string
	var capturedQuery string

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Clone()
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "msg_01",
			"type": "message",
			"role": "assistant",
			"model": "claude-sonnet-4-6",
			"content": [{"type":"text","text":"hi"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:      srv.URL,
		AuthToken:    "tok-abc",
		SessionID:    "00000000-0000-0000-0000-000000000001",
		BetaHeaders:  []string{"oauth-2025-04-20"},
		UserAgent:    "claude-cli/0.0.1 (test)",
		ClaudeCodeID: "cli",
	}, srv.Client())

	resp, err := c.CreateMessage(context.Background(), &MessageRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 16,
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: "hi"}},
		}},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	// --- response shape ---
	if resp.ID != "msg_01" {
		t.Errorf("resp.ID = %q", resp.ID)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "hi" {
		t.Errorf("resp.Content = %#v", resp.Content)
	}

	// --- path + query ---
	if capturedPath != "/v1/messages" {
		t.Errorf("path = %q; want /v1/messages", capturedPath)
	}
	if capturedQuery != "beta=true" {
		t.Errorf("query = %q; want beta=true", capturedQuery)
	}

	// --- headers ---
	mustHeader := func(name, want string) {
		t.Helper()
		got := capturedHeader.Get(name)
		if got != want {
			t.Errorf("header %s = %q; want %q", name, got, want)
		}
	}
	mustHeader("Authorization", "Bearer tok-abc")
	mustHeader("anthropic-version", "2023-06-01")
	mustHeader("anthropic-beta", "oauth-2025-04-20")
	mustHeader("Content-Type", "application/json")
	mustHeader("Accept", "application/json")
	mustHeader("x-app", "cli")
	mustHeader("X-Claude-Code-Session-Id", "00000000-0000-0000-0000-000000000001")
	if !strings.HasPrefix(capturedHeader.Get("User-Agent"), "claude-cli/") {
		t.Errorf("User-Agent = %q; want claude-cli/* prefix", capturedHeader.Get("User-Agent"))
	}

	// --- body ---
	if capturedBody["model"] != "claude-sonnet-4-6" {
		t.Errorf("body.model = %v", capturedBody["model"])
	}
	if mt, ok := capturedBody["max_tokens"].(float64); !ok || mt != 16 {
		t.Errorf("body.max_tokens = %v", capturedBody["max_tokens"])
	}
}

func TestCreateMessage_MultipleBetaHeaders(t *testing.T) {
	var capturedBeta string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		capturedBeta = r.Header.Get("anthropic-beta")
		_, _ = io.WriteString(w, `{"id":"x","type":"message","role":"assistant","model":"m","content":[],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:     srv.URL,
		AuthToken:   "tok",
		SessionID:   "sess",
		BetaHeaders: []string{"oauth-2025-04-20", "structured-outputs-2025-12-15"},
	}, srv.Client())

	_, _ = c.CreateMessage(context.Background(), &MessageRequest{Model: "m", MaxTokens: 1, Messages: []Message{}})

	// Decoded reference uses Array.prototype.toString() which yields
	// comma-without-space joining (decoded/0158.js:55,67,84). The plan
	// requires byte-equivalent headers, so we must match exactly.
	want := "oauth-2025-04-20,structured-outputs-2025-12-15"
	if capturedBeta != want {
		t.Errorf("anthropic-beta = %q; want %q", capturedBeta, want)
	}
}

func TestCreateMessage_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad thing"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())

	_, err := c.CreateMessage(context.Background(), &MessageRequest{Model: "m", MaxTokens: 1, Messages: []Message{}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid_request_error") {
		t.Errorf("err = %v; should mention invalid_request_error", err)
	}
	if !strings.Contains(err.Error(), "bad thing") {
		t.Errorf("err = %v; should mention message", err)
	}
}

// TestCreateMessage_401RetryAfterRefresh verifies decoded/4500.js:32-44 semantics:
// on 401, the client calls the refresh hook, then retries the same request
// exactly once with the updated token.
func TestCreateMessage_401RetryAfterRefresh(t *testing.T) {
	var calls int
	var seenTokens []string

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		calls++
		seenTokens = append(seenTokens, r.Header.Get("Authorization"))
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"expired"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var refreshCalls int
	cfg := Config{
		BaseURL:   srv.URL,
		AuthToken: "stale",
	}
	var c *Client
	cfg.OnAuth401 = func(ctx context.Context) error {
		refreshCalls++
		c.SetAuthToken("fresh")
		return nil
	}
	c = NewClient(cfg, srv.Client())

	resp, err := c.CreateMessage(context.Background(), &MessageRequest{Model: "m", MaxTokens: 1, Messages: []Message{}})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if resp.Content[0].Text != "ok" {
		t.Errorf("text = %q", resp.Content[0].Text)
	}
	if calls != 2 {
		t.Errorf("calls = %d; want 2", calls)
	}
	if refreshCalls != 1 {
		t.Errorf("refreshCalls = %d; want 1", refreshCalls)
	}
	if seenTokens[0] != "Bearer stale" {
		t.Errorf("first call token = %q", seenTokens[0])
	}
	if seenTokens[1] != "Bearer fresh" {
		t.Errorf("second call token = %q", seenTokens[1])
	}
}

// TestCreateMessage_401NoRetryWhenNoHook: without OnAuth401, a 401 surfaces
// as an error rather than looping.
func TestCreateMessage_401NoRetryWhenNoHook(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	_, err := c.CreateMessage(context.Background(), &MessageRequest{Model: "m", MaxTokens: 1, Messages: []Message{}})
	if err == nil {
		t.Fatal("expected 401 error")
	}
}

func TestCreateMessage_ContextCancel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.CreateMessage(ctx, &MessageRequest{Model: "m", MaxTokens: 1, Messages: []Message{}})
	if err == nil {
		t.Fatal("expected error from cancelled ctx")
	}
}
