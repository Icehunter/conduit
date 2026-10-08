package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

func TestShouldFailover(t *testing.T) {
	status := func(code int, typ, msg string) error {
		return &api.APIStatusError{Status: code, Type: typ, Message: msg}
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"429 status", status(429, "rate_limit_error", "slow down"), true},
		{"503 status", status(503, "", "unavailable"), true},
		{"529 overloaded", status(529, "overloaded_error", "Overloaded"), true},
		{"502 gateway", status(502, "", "bad gateway"), true},
		{"model not found", status(404, "not_found_error", "model: x"), true},
		{"429 in text", errors.New("HTTP 429: too many requests"), true},
		{"503 in text", errors.New("upstream returned 503"), true},
		{"rate limit phrase", errors.New("rate limit exceeded"), true},
		{"overloaded phrase", errors.New("API is overloaded"), true},
		{"quota phrase", errors.New("quota exhausted for this plan"), true},
		{"network error", errors.New("read tcp: connection reset by peer"), true},
		{"truncated stream", api.ErrStreamTruncated, true},
		{"nil error", nil, false},
		{"context canceled", context.Canceled, false},
		{"401 auth", status(401, "authentication_error", "bad token"), false},
		{"400 invalid request", status(400, "invalid_request_error", "bad field"), false},
		{"400 in text", errors.New("400 bad request"), false},
		{"overflow wins over 500", status(500, "", "request exceeds the available context size (9000 tokens)"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldFailover(api.Classify(tt.err))
			if got != tt.want {
				t.Errorf("shouldFailover(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRotateCooldown(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		c    api.Classified
		want time.Time
	}{
		{"retry-after wins", api.Classified{RetryAfter: 30 * time.Second, ResetAt: now.Add(time.Hour)}, now.Add(30 * time.Second)},
		{"reset when no retry-after", api.Classified{ResetAt: now.Add(time.Hour)}, now.Add(time.Hour)},
		{"past reset ignored", api.Classified{ResetAt: now.Add(-time.Hour)}, now.Add(5 * time.Minute)},
		{"default", api.Classified{}, now.Add(5 * time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rotateCooldown(tt.c, now); !got.Equal(tt.want) {
				t.Errorf("rotateCooldown() = %v, want %v", got, tt.want)
			}
		})
	}
}

// newFailThenSucceedLoop creates a loop where the first provider always returns
// a 429 error and the second provider succeeds with textOnlySSE("ok").
func newFailThenSucceedLoop(t *testing.T) (*Loop, func()) {
	t.Helper()

	// First server always returns 429 with a large Retry-After so the loop's
	// retry handler aborts immediately (Retry-After > 2 min threshold), making
	// the test fast without any real sleep.
	firstCalled := atomic.Int32{}
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalled.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Retry-After", "9999") // > 2 min → loop aborts internal retry
		http.Error(w, "429 rate limit exceeded", http.StatusTooManyRequests)
	}))

	// Second server always succeeds.
	secondCalled := atomic.Int32{}
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("ok")))
	}))

	primaryProvider := settings.ActiveProviderSettings{
		Kind:  settings.ProviderKindAnthropicAPI,
		Model: "primary-model",
	}
	fallbackProvider := settings.ActiveProviderSettings{
		Kind:  settings.ProviderKindAnthropicAPI,
		Model: "fallback-model",
	}
	chain := []settings.ActiveProviderSettings{primaryProvider, fallbackProvider}

	// Build the primary client (points at the fail server).
	primaryClient := api.NewClient(api.Config{
		BaseURL:   failSrv.URL,
		AuthToken: "test",
	}, failSrv.Client())

	reg := tool.NewRegistry()
	lp := NewLoop(primaryClient, reg, LoopConfig{
		Model:     primaryProvider.Model,
		MaxTokens: 1024,
		System:    []api.SystemBlock{{Type: "text", Text: "test"}},
	})

	lp.SetProviderChain(
		settings.RoleDefault,
		func(_ string) []settings.ActiveProviderSettings { return chain },
		func(p settings.ActiveProviderSettings) (*api.Client, error) {
			// Return the ok server client when asked for the fallback provider.
			if p.Model == fallbackProvider.Model {
				return api.NewClient(api.Config{
					BaseURL:   okSrv.URL,
					AuthToken: "test",
				}, okSrv.Client()), nil
			}
			return api.NewClient(api.Config{
				BaseURL:   failSrv.URL,
				AuthToken: "test",
			}, failSrv.Client()), nil
		},
	)

	cleanup := func() {
		failSrv.Close()
		okSrv.Close()
		t.Logf("primary server called %d time(s)", firstCalled.Load())
		t.Logf("fallback server called %d time(s)", secondCalled.Load())
	}
	return lp, cleanup
}

func TestLoop_ProviderFailover_SwitchesOnRateLimit(t *testing.T) {
	lp, cleanup := newFailThenSucceedLoop(t)
	defer cleanup()

	var (
		mu               sync.Mutex
		events           []EventType
		failoverProvider string
	)

	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
	}, func(ev LoopEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev.Type)
		if ev.Type == EventProviderFailover {
			failoverProvider = ev.FailoverProvider
		}
	})
	if err != nil {
		t.Fatalf("Run() returned error after failover: %v", err)
	}

	mu.Lock()
	evSnapshot := append([]EventType(nil), events...)
	fpSnapshot := failoverProvider
	mu.Unlock()

	// Verify that a failover event was emitted.
	found := false
	for _, et := range evSnapshot {
		if et == EventProviderFailover {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected EventProviderFailover to be emitted, but it was not")
	}
	t.Logf("failover provider key: %q", fpSnapshot)

	// Verify the turn completed (EventText received from the fallback provider).
	hasText := false
	for _, et := range evSnapshot {
		if et == EventText {
			hasText = true
			break
		}
	}
	if !hasText {
		t.Error("expected EventText from fallback provider, but none received")
	}
}
