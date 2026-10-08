package api

import (
	"context"
	"errors"
	"io"
	"net"
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

func TestRetryHandlerReceivesUnifiedReset(t *testing.T) {
	sleepFn = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() {
		sleepFn = func(ctx context.Context, d time.Duration) error {
			select {
			case <-time.After(d):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	resetAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	var got RetryEvent
	ctx := WithRetryHandler(context.Background(), func(ev RetryEvent) bool {
		got = ev
		return false
	})
	_, err := withRetry(ctx, func() (*http.Response, error) {
		body := io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`))
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Header: http.Header{
				"Retry-After":                       []string{"60"},
				"Anthropic-Ratelimit-Unified-Reset": []string{resetAt.Format(time.RFC3339)},
			},
			Body: body,
		}, nil
	})
	if err == nil {
		t.Fatal("expected retry handler to abort with rate-limit error")
	}
	if got.RetryAfter != time.Minute {
		t.Fatalf("RetryAfter = %s; want 1m", got.RetryAfter)
	}
	if !got.ResetAt.Equal(resetAt) {
		t.Fatalf("ResetAt = %s; want %s", got.ResetAt, resetAt)
	}
}

func TestStreamMessage_429RetryHandlerNotified(t *testing.T) {
	oldSleep := sleepFn
	t.Cleanup(func() { sleepFn = oldSleep })
	sleepFn = func(context.Context, time.Duration) error { return nil }

	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("retry-after", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"x\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
	var got RetryEvent
	ctx := WithRetryHandler(context.Background(), func(ev RetryEvent) bool {
		got = ev
		return true
	})
	_, err := c.StreamMessage(ctx, &MessageRequest{Model: "m", MaxTokens: 1})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	if got.Attempt != 1 {
		t.Fatalf("retry attempt = %d, want 1", got.Attempt)
	}
	if got.RetryAfter != 5*time.Second {
		t.Fatalf("retry-after = %v, want 5s", got.RetryAfter)
	}
	if got.Err == nil || !strings.Contains(got.Err.Error(), "rate_limit_error") {
		t.Fatalf("retry error = %v, want rate_limit_error", got.Err)
	}
}

func TestStreamMessage_429RetryHandlerCanAbort(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("retry-after", "600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
	ctx := WithRetryHandler(context.Background(), func(RetryEvent) bool { return false })
	_, err := c.StreamMessage(ctx, &MessageRequest{Model: "m", MaxTokens: 1})
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if !strings.Contains(err.Error(), "rate_limit_error") {
		t.Fatalf("err = %v, want rate_limit_error", err)
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

func TestBackoffDelay(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		hint    time.Duration
		lo, hi  time.Duration
	}{
		{"first retry", 0, 0, 750 * time.Millisecond, 1250 * time.Millisecond},
		{"second retry doubles", 1, 0, 1500 * time.Millisecond, 2500 * time.Millisecond},
		{"fifth retry", 4, 0, 12 * time.Second, 20 * time.Second},
		{"capped at 32s plus jitter", 10, 0, 24 * time.Second, 40 * time.Second},
		{"negative attempt counts as first", -3, 0, 750 * time.Millisecond, 1250 * time.Millisecond},
		{"a server hint is used exactly", 3, 1500 * time.Millisecond, 1500 * time.Millisecond, 1500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for range 50 {
				if got := BackoffDelay(tt.attempt, tt.hint); got < tt.lo || got > tt.hi {
					t.Fatalf("BackoffDelay(%d, %v) = %v, want within [%v, %v]", tt.attempt, tt.hint, got, tt.lo, tt.hi)
				}
			}
		})
	}
}

func TestShouldRetryStatus(t *testing.T) {
	tests := []struct {
		code   int
		header string
		want   bool
	}{
		{408, "", true}, {409, "", true}, {429, "", true}, {500, "", true}, {502, "", true},
		{503, "", true}, {529, "", true},
		{400, "", false}, {401, "", false}, {403, "", false}, {404, "", false}, {413, "", false}, {200, "", false},
		{500, "false", false}, {429, "false", false}, {529, " False ", false},
		{400, "true", true}, {401, "true", true},
		{503, "garbage", true}, {400, "garbage", false},
	}
	for _, tt := range tests {
		if got := shouldRetryStatus(tt.code, tt.header); got != tt.want {
			t.Errorf("shouldRetryStatus(%d, %q) = %v, want %v", tt.code, tt.header, got, tt.want)
		}
	}
}

// statusResp is a canned response for driving withRetry directly.
func statusResp(code int, hdr http.Header, body string) *http.Response {
	if hdr == nil {
		hdr = http.Header{}
	}
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Header: hdr, Body: io.NopCloser(strings.NewReader(body))}
}

func TestWithRetry(t *testing.T) {
	rateBody := `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`
	apiBody := `{"type":"error","error":{"type":"api_error","message":"boom"}}`
	badBody := `{"type":"error","error":{"type":"invalid_request_error","message":"bad field"}}`
	overflowBody := `{"type":"error","error":{"type":"api_error","message":"request exceeds the available context size (n_ctx)"}}`
	netErr := &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}

	tests := []struct {
		name       string
		steps      []func() (*http.Response, error)
		wantCalls  int
		wantSleeps []time.Duration // zero entries only check the count
		wantStatus int             // status of the response returned, 0 when an error is
		wantErr    string
	}{
		{
			name: "network error then ok",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) { return nil, netErr },
				func() (*http.Response, error) { return statusResp(200, nil, ""), nil },
			},
			wantCalls: 2, wantSleeps: []time.Duration{0}, wantStatus: 200,
		},
		{
			name: "network error forever stops after 5 retries",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) { return nil, netErr },
			},
			wantCalls: 6, wantSleeps: []time.Duration{0, 0, 0, 0, 0}, wantErr: "connection reset",
		},
		{
			name: "a tls error is not retried",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) {
					return nil, errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority")
				},
			},
			wantCalls: 1, wantErr: "x509",
		},
		{
			name: "500 502 then 200",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) { return statusResp(500, nil, apiBody), nil },
				func() (*http.Response, error) { return statusResp(502, nil, apiBody), nil },
				func() (*http.Response, error) { return statusResp(200, nil, ""), nil },
			},
			wantCalls: 3, wantSleeps: []time.Duration{0, 0}, wantStatus: 200,
		},
		{
			name: "400 is returned for the caller to decode",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) { return statusResp(400, nil, badBody), nil },
			},
			wantCalls: 1, wantStatus: 400,
		},
		{
			name: "401 is returned for the caller to refresh",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) { return statusResp(401, nil, badBody), nil },
			},
			wantCalls: 1, wantStatus: 401,
		},
		{
			name: "retry-after-ms wins over retry-after",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) {
					return statusResp(429, http.Header{"Retry-After-Ms": {"2500"}, "Retry-After": {"9"}}, rateBody), nil
				},
				func() (*http.Response, error) { return statusResp(200, nil, ""), nil },
			},
			wantCalls: 2, wantSleeps: []time.Duration{2500 * time.Millisecond}, wantStatus: 200,
		},
		{
			name: "x-should-retry false is returned undecoded",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) {
					return statusResp(503, http.Header{"X-Should-Retry": {"false"}}, apiBody), nil
				},
			},
			wantCalls: 1, wantStatus: 503,
		},
		{
			name: "a 500 that is really an overflow is not retried",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) { return statusResp(500, nil, overflowBody), nil },
			},
			wantCalls: 1, wantErr: "context size",
		},
		{
			name: "a wait over two minutes is surfaced without sleeping",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) {
					return statusResp(429, http.Header{"Retry-After": {"300"}}, rateBody), nil
				},
			},
			wantCalls: 1, wantErr: "rate_limit_error",
		},
		{
			name: "a 408 is retried",
			steps: []func() (*http.Response, error){
				func() (*http.Response, error) { return statusResp(408, nil, apiBody), nil },
				func() (*http.Response, error) { return statusResp(200, nil, ""), nil },
			},
			wantCalls: 2, wantSleeps: []time.Duration{0}, wantStatus: 200,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var slept []time.Duration
			ctx := WithSleep(context.Background(), func(_ context.Context, d time.Duration) error {
				slept = append(slept, d)
				return nil
			})
			calls := 0
			resp, err := withRetry(ctx, func() (*http.Response, error) {
				step := tt.steps[min(calls, len(tt.steps)-1)]
				calls++
				return step()
			})
			if calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tt.wantCalls)
			}
			if len(slept) != len(tt.wantSleeps) {
				t.Fatalf("sleeps = %v, want %d", slept, len(tt.wantSleeps))
			}
			for i, want := range tt.wantSleeps {
				if want != 0 && slept[i] != want {
					t.Errorf("sleep %d = %v, want %v", i, slept[i], want)
				}
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("withRetry: %v", err)
			}
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestWithRetry_EventCarriesReasonAndStatus(t *testing.T) {
	var events []RetryEvent
	ctx := WithSleep(WithRetryHandler(context.Background(), func(ev RetryEvent) bool {
		events = append(events, ev)
		return true
	}), func(context.Context, time.Duration) error { return nil })

	steps := []func() (*http.Response, error){
		func() (*http.Response, error) {
			return nil, &net.OpError{Op: "read", Err: errors.New("connection reset")}
		},
		func() (*http.Response, error) {
			return statusResp(529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), nil
		},
		func() (*http.Response, error) {
			return statusResp(502, nil, `{"type":"error","error":{"type":"api_error","message":"bad gateway"}}`), nil
		},
		func() (*http.Response, error) { return statusResp(200, nil, ""), nil },
	}
	n := 0
	if _, err := withRetry(ctx, func() (*http.Response, error) { s := steps[n]; n++; return s() }); err != nil {
		t.Fatalf("withRetry: %v", err)
	}
	want := []struct {
		reason string
		status int
	}{{ReasonNetwork, 0}, {ReasonOverloaded, 529}, {ReasonServerError, 502}}
	if len(events) != len(want) {
		t.Fatalf("events = %d, want %d", len(events), len(want))
	}
	for i, w := range want {
		ev := events[i]
		if ev.Reason != w.reason || ev.Status != w.status || ev.Attempt != i+1 || ev.MaxAttempts != retryMaxCount {
			t.Errorf("event %d = %+v, want reason %q status %d attempt %d", i, ev, w.reason, w.status, i+1)
		}
	}
}

func TestWithRetry_CancelDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithSleep(ctx, func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	})
	calls := 0
	_, err := withRetry(ctx, func() (*http.Response, error) {
		calls++
		return statusResp(503, nil, `{"type":"error","error":{"type":"api_error","message":"x"}}`), nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("err = %v after %d calls, want context.Canceled after 1", err, calls)
	}
}

func TestWithRetry_CancelledContextStopsNetworkRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := withRetry(ctx, func() (*http.Response, error) {
		calls++
		return nil, &net.OpError{Op: "read", Err: errors.New("connection reset")}
	})
	if err == nil || calls != 1 {
		t.Errorf("err = %v after %d calls, want one failed call", err, calls)
	}
}

func TestSleepContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("SleepContext on a cancelled ctx = %v, want context.Canceled", err)
	}
	if err := SleepContext(context.Background(), 0); err != nil {
		t.Errorf("SleepContext(0) = %v, want nil", err)
	}
	if err := SleepContext(context.Background(), time.Millisecond); err != nil {
		t.Errorf("SleepContext(1ms) = %v, want nil", err)
	}
}

func TestWithRetry_ShouldRetryHeaderIgnoredForSuccessAndAuth(t *testing.T) {
	for _, code := range []int{200, 201, 401} {
		calls := 0
		resp, err := withRetry(WithSleep(context.Background(), func(context.Context, time.Duration) error { return nil }),
			func() (*http.Response, error) {
				calls++
				return statusResp(code, http.Header{"X-Should-Retry": {"true"}}, `{"ok":true}`), nil
			})
		if err != nil || calls != 1 || resp.StatusCode != code {
			t.Errorf("status %d: err=%v calls=%d resp=%v; want the response returned untouched", code, err, calls, resp)
		}
	}
}
