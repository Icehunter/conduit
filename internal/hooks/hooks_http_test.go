package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/icehunter/conduit/internal/settings"
)

func TestRunHTTPHook_PostsJSON(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hook := settings.Hook{Type: "http", URL: srv.URL}
	input := HookInput{SessionID: "test-sess", ToolName: "Bash"}
	r := runHTTPHook(context.Background(), hook, input)

	if r.Blocked {
		t.Errorf("200 response should not block; reason: %s", r.Reason)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("server did not receive valid JSON: %v", err)
	}
	if body["session_id"] != "test-sess" {
		t.Errorf("body missing session_id: %v", body)
	}
}

func TestRunHTTPHook_BlockDecision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"decision":"block","reason":"not allowed"}`)
	}))
	defer srv.Close()

	hook := settings.Hook{Type: "http", URL: srv.URL}
	r := runHTTPHook(context.Background(), hook, HookInput{})

	if !r.Blocked {
		t.Error("block decision should block")
	}
	if r.Reason != "not allowed" {
		t.Errorf("reason = %q", r.Reason)
	}
}

func TestRunHTTPHook_ApproveDecision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"decision":"approve"}`)
	}))
	defer srv.Close()

	hook := settings.Hook{Type: "http", URL: srv.URL}
	r := runHTTPHook(context.Background(), hook, HookInput{})

	if r.Blocked {
		t.Error("approve should not block")
	}
	if !r.Approved {
		t.Error("approve decision should set Approved")
	}
}

func TestRunHTTPHook_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	hook := settings.Hook{Type: "http", URL: srv.URL}
	r := runHTTPHook(context.Background(), hook, HookInput{})

	if !r.Blocked {
		t.Error("5xx response should block")
	}
}

func TestRunHTTPHook_CancelledContext(t *testing.T) {
	// Verify that a pre-cancelled context causes the hook to block with error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This handler won't be reached when context is pre-cancelled.
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hook := settings.Hook{Type: "http", URL: srv.URL}

	// Pre-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := runHTTPHook(ctx, hook, HookInput{})

	if !r.Blocked {
		t.Error("cancelled context should block")
	}
}

func TestRunHTTPHook_CustomHeaders(t *testing.T) {
	var gotAuthHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("X-Custom-Header")
	}))
	defer srv.Close()

	hook := settings.Hook{
		Type:    "http",
		URL:     srv.URL,
		Headers: map[string]string{"X-Custom-Header": "myvalue"},
	}
	runHTTPHook(context.Background(), hook, HookInput{})

	if gotAuthHeader != "myvalue" {
		t.Errorf("custom header not sent; got %q", gotAuthHeader)
	}
}

func TestRunMatching_AsyncHookDoesNotBlock(t *testing.T) {
	// An async hook should fire but not block the caller even if it would
	// normally block (e.g. non-zero exit).
	matchers := []settings.HookMatcher{{
		Matcher: "",
		Hooks: []settings.Hook{{
			Type:    "command",
			Command: "false",
			Async:   true,
		}},
	}}
	r := runMatching(context.Background(), matchers, "", HookInput{SessionID: "s"})
	if r.Blocked {
		t.Error("async hook should not block caller even on failure")
	}
}

func TestRunHTTPHook_InvalidURL(t *testing.T) {
	hook := settings.Hook{Type: "http", URL: "not-a-url"}
	r := runHTTPHook(context.Background(), hook, HookInput{})
	if !r.Blocked {
		t.Error("invalid URL should block with error")
	}
}
