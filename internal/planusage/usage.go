// Package planusage fetches Claude subscription usage windows from the
// Anthropic OAuth usage endpoint.
package planusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const endpoint = "https://api.anthropic.com/api/oauth/usage"

// ErrRateLimited is returned (wrapped) when the API responds with 429.
var ErrRateLimited = errors.New("rate limited")

// RateLimitError carries the parsed Retry-After duration.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("plan usage: rate limited (retry after %s)", e.RetryAfter.Round(time.Second))
	}
	return "plan usage: rate limited"
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Info is the subset of usage data rendered in the TUI footer.
type Info struct {
	FiveHour Window
	SevenDay Window
}

// Window describes one plan usage window.
type Window struct {
	Utilization float64
	ResetsAt    time.Time
}

type response struct {
	FiveHour rawWindow `json:"five_hour"`
	SevenDay rawWindow `json:"seven_day"`
}

type rawWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// Fetch retrieves current Claude plan usage using an OAuth access token.
func Fetch(ctx context.Context, accessToken string) (Info, error) {
	if accessToken == "" {
		return Info{}, fmt.Errorf("plan usage: OAuth access token unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Info{}, fmt.Errorf("plan usage: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("plan usage: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return Info{}, &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("plan usage: API returned %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var raw response
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Info{}, fmt.Errorf("plan usage: decode: %w", err)
	}
	return Info{
		FiveHour: parseWindow(raw.FiveHour),
		SevenDay: parseWindow(raw.SevenDay),
	}, nil
}

// parseRetryAfter converts a Retry-After header value (seconds integer or
// HTTP-date) into a duration. Returns 0 if the header is absent or unparseable.
func parseRetryAfter(s string) time.Duration {
	if s == "" {
		return 0
	}
	if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(s); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func parseWindow(w rawWindow) Window {
	reset, _ := time.Parse(time.RFC3339, w.ResetsAt)
	return Window{
		Utilization: w.Utilization,
		ResetsAt:    reset,
	}
}
