package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

func TestIsProviderFailover(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"429 in message", errors.New("HTTP 429: too many requests"), true},
		{"503 in message", errors.New("upstream returned 503"), true},
		{"529 in message", errors.New("server error 529"), true},
		{"rate limit phrase", errors.New("rate limit exceeded"), true},
		{"too many requests phrase", errors.New("Too Many Requests"), true},
		{"overloaded phrase", errors.New("API is overloaded"), true},
		{"quota phrase", errors.New("quota exhausted for this plan"), true},
		{"generic error", errors.New("connection refused"), false},
		{"context canceled", context.Canceled, false},
		{"bad request 400", errors.New("400 bad request"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isProviderFailover(tt.err)
			if got != tt.want {
				t.Errorf("isProviderFailover(%v) = %v, want %v", tt.err, got, tt.want)
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
