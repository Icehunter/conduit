package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// blockingServer never sends headers until released (or the test ends).
func blockingServer(t *testing.T, bodyAfterHeaders string) (*httptest.Server, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bodyAfterHeaders != "" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = io.WriteString(w, bodyAfterHeaders)
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv, release
}

func TestResponseHeaderTimeout(t *testing.T) {
	sends := []struct {
		name string
		kind string
		do   func(c *Client) (*http.Response, error)
	}{
		{"anthropic", "", func(c *Client) (*http.Response, error) {
			return c.doStream(context.Background(), []byte(`{}`), "m", nil, nil)
		}},
		{"chat", ProviderKindOpenAICompatible, func(c *Client) (*http.Response, error) {
			return c.doOpenAI(context.Background(), []byte(`{}`), nil)
		}},
		{"responses", ProviderKindOpenAIResponses, func(c *Client) (*http.Response, error) {
			return c.doOpenAIResponses(context.Background(), []byte(`{}`), nil)
		}},
	}
	for _, s := range sends {
		t.Run(s.name+"/no headers times out", func(t *testing.T) {
			srv, _ := blockingServer(t, "")
			c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", ProviderKind: s.kind, ResponseHeaderTimeout: 30 * time.Millisecond}, srv.Client())
			_, err := s.do(c)
			if !errors.Is(err, ErrResponseHeaderTimeout) {
				t.Fatalf("err = %v, want ErrResponseHeaderTimeout", err)
			}
			if got := Classify(err); got.Class != ClassTransient || got.Reason != ReasonNetwork {
				t.Errorf("Classify = %+v, want transient/network", got)
			}
		})
		t.Run(s.name+"/headers then slow body is fine", func(t *testing.T) {
			srv, release := blockingServer(t, "ok")
			c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", ProviderKind: s.kind, ResponseHeaderTimeout: 30 * time.Millisecond}, srv.Client())
			resp, err := s.do(c)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			// The timer is stopped once headers arrive, so a body that
			// outlasts the timeout is not cancelled.
			timer := time.NewTimer(100 * time.Millisecond)
			defer timer.Stop()
			<-timer.C
			release <- struct{}{}
			b, err := io.ReadAll(resp.Body)
			if err != nil || string(b) != "ok" {
				t.Fatalf("body = %q, %v", b, err)
			}
		})
	}
}

func TestResponseHeaderTimeout_CallerCancelIsNotTimeout(t *testing.T) {
	srv, _ := blockingServer(t, "")
	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", ProviderKind: ProviderKindOpenAIResponses, ResponseHeaderTimeout: time.Hour}, srv.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.doOpenAIResponses(ctx, []byte(`{}`), nil)
	if errors.Is(err, ErrResponseHeaderTimeout) || Classify(err).Class != ClassCancelled {
		t.Fatalf("err = %v, class %v", err, Classify(err).Class)
	}
}

func TestResponseHeaderTimeout_Config(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		in      time.Duration
		want    time.Duration
	}{
		{"hosted default", "https://api.openai.com/v1", 0, defaultResponseHeaderTimeout},
		{"anthropic default", "", 0, defaultResponseHeaderTimeout},
		{"localhost unset is unlimited", "http://localhost:11434/v1", 0, 0},
		{"LAN unset is unlimited", "http://192.168.0.71:8080/v1", 0, 0},
		{"local with explicit value", "http://localhost:11434/v1", 90 * time.Second, 90 * time.Second},
		{"negative disables", "https://api.openai.com/v1", -1, 0},
		{"explicit", "https://api.openai.com/v1", 7 * time.Second, 7 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (&Client{cfg: Config{BaseURL: tc.baseURL, ResponseHeaderTimeout: tc.in}}).responseHeaderTimeout(); got != tc.want {
				t.Errorf("responseHeaderTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}

// A header timeout is retried once, not retryMaxCount times: each resend costs
// a full timeout and restarts a local server's prefill.
func TestWithRetry_HeaderTimeoutRetriedOnce(t *testing.T) {
	calls := 0
	ctx := WithSleep(context.Background(), func(context.Context, time.Duration) error { return nil })
	_, err := withRetry(ctx, func() (*http.Response, error) {
		calls++
		return nil, ErrResponseHeaderTimeout
	})
	if !errors.Is(err, ErrResponseHeaderTimeout) || calls != 1+maxHeaderTimeoutRetries {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
}
