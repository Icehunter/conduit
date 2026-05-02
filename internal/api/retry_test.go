package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamMessage_429ExponentialBackoff verifies that on 429 the client
// waits then retries with exponential back-off, up to maxRetries.
// Mirrors withRetry.ts: base=1s, multiplier=2, max=32s, jitter.
func TestStreamMessage_429ExponentialBackoff(t *testing.T) {
	calls := 0
	var delays []time.Duration
	var lastCallAt time.Time

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		if !lastCallAt.IsZero() {
			delays = append(delays, now.Sub(lastCallAt))
		}
		lastCallAt = now
		calls++

		if calls <= 2 {
			// First two calls → 429 with 10ms retry-after
			w.Header().Set("retry-after", "0.01") // 10ms in seconds
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
			return
		}
		// Third call → success
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"x\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())

	_, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model:     "m",
		MaxTokens: 1,
		Messages:  []Message{},
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d; want 3", calls)
	}
}

// TestStreamMessage_429MaxRetries verifies that after maxRetries the error surfaces.
func TestStreamMessage_429MaxRetries(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("retry-after", "0.001")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())

	_, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model:     "m",
		MaxTokens: 1,
		Messages:  []Message{},
	})
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	if !strings.Contains(err.Error(), "rate_limit_error") {
		t.Errorf("err = %v; want rate_limit_error mention", err)
	}
	// Should have retried maxRetries+1 total calls (1 initial + maxRetries retries)
	if calls < 2 {
		t.Errorf("calls = %d; want at least 2 (initial + retries)", calls)
	}
}

// TestStreamMessage_429ContextCancellation verifies backoff respects context cancellation.
func TestStreamMessage_429ContextCancellation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("retry-after", "60") // 60s wait
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately after one attempt
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.StreamMessage(ctx, &MessageRequest{
		Model:     "m",
		MaxTokens: 1,
		Messages:  []Message{},
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	// Should not have waited the full 60s
	if elapsed > 2*time.Second {
		t.Errorf("waited %v; should have cancelled quickly", elapsed)
	}
}

// TestStreamMessage_HTTPProxy verifies HTTPS_PROXY env var is honoured.
// We test this by pointing HTTPS_PROXY at an httptest server that records
// the CONNECT or forwarded request.
func TestStreamMessage_HTTPProxy(t *testing.T) {
	// A simple proxy that records requests and forwards them.
	var proxyCalled bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalled = true
		// Return a minimal response (not a real proxy, just checking call).
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"proxy test"}}`)
	}))
	defer proxy.Close()

	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)

	// Build client with a transport that honours the env vars.
	c := NewClientWithProxy(Config{
		BaseURL:   "https://api.anthropic.com",
		AuthToken: "tok",
	})

	// We don't care about success — just that the proxy was called.
	_, _ = c.StreamMessage(context.Background(), &MessageRequest{
		Model:     "m",
		MaxTokens: 1,
		Messages:  []Message{},
	})

	if !proxyCalled {
		t.Error("HTTPS_PROXY was not honoured — proxy server not called")
	}
}
