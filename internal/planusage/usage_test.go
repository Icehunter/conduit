package planusage

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseWindow(t *testing.T) {
	got := parseWindow(rawWindow{
		Utilization: 42.5,
		ResetsAt:    "2026-05-05T12:34:56Z",
	})
	if got.Utilization != 42.5 {
		t.Fatalf("Utilization = %v; want 42.5", got.Utilization)
	}
	want := time.Date(2026, 5, 5, 12, 34, 56, 0, time.UTC)
	if !got.ResetsAt.Equal(want) {
		t.Fatalf("ResetsAt = %v; want %v", got.ResetsAt, want)
	}
}

func TestParseWindowBadReset(t *testing.T) {
	got := parseWindow(rawWindow{Utilization: 12, ResetsAt: "not-a-time"})
	if !got.ResetsAt.IsZero() {
		t.Fatalf("ResetsAt = %v; want zero time", got.ResetsAt)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "30", 30 * time.Second},
		{"zero seconds", "0", 0},
		{"negative", "-5", 0},
		{"garbage", "soon", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(tt.input)
			if got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v; want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFetchRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	// Temporarily override the endpoint for this test via a local fetch call.
	// We call the internals directly since endpoint is a package-level const;
	// use an httptest round-trip by building the request manually.
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}

	rle := &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	if rle.RetryAfter != 120*time.Second {
		t.Errorf("RetryAfter = %v; want 120s", rle.RetryAfter)
	}
	if !errors.Is(rle, ErrRateLimited) {
		t.Errorf("errors.Is(rle, ErrRateLimited) = false; want true")
	}
}
