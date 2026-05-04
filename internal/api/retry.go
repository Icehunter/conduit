package api

import (
	"context"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Retry parameters mirroring withRetry.ts:
//
//	base=1s, multiplier=2, max=32s, jitter=25%, maxRetries=5
const (
	retryBase       = 1 * time.Second
	retryMultiplier = 2.0
	retryMax        = 32 * time.Second
	retryMaxCount   = 5
)

// sleepFn is replaced in tests to avoid real sleeps.
var sleepFn = func(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryDelay returns the delay for attempt n (0-based), clamped to retryMax,
// with ±25% jitter. Mirrors withRetry.ts exponential curve.
func retryDelay(n int, retryAfterSecs float64) time.Duration {
	if retryAfterSecs > 0 {
		// Server told us exactly when to retry.
		d := time.Duration(retryAfterSecs * float64(time.Second))
		// Still add a small jitter (±10%) so many clients don't thunderherd.
		jitter := time.Duration(rand.Float64()*0.1*float64(d)) - time.Duration(0.05*float64(d))
		d += jitter
		if d < 0 {
			d = 0
		}
		return d
	}
	base := float64(retryBase) * math.Pow(retryMultiplier, float64(n))
	if base > float64(retryMax) {
		base = float64(retryMax)
	}
	// ±25% jitter
	jitter := (rand.Float64()*0.5 - 0.25) * base
	d := time.Duration(base + jitter)
	if d < 0 {
		d = 0
	}
	return d
}

// parseRetryAfter parses the retry-after header value.
// Returns seconds as float64, 0 if absent or unparseable.
// The header can be a decimal seconds value or an HTTP-date.
func parseRetryAfter(h string) float64 {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	// Try numeric first (most common from Anthropic).
	if v, err := strconv.ParseFloat(h, 64); err == nil {
		return v
	}
	return 0
}

// withRetry wraps a function that returns (*http.Response, error), retrying
// on 429 with exponential backoff. On 401 it is not retried here; the caller
// handles 401 separately.
func withRetry(ctx context.Context, fn func() (*http.Response, error)) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= retryMaxCount; attempt++ {
		resp, err = fn()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}

		// 429 — read retry-after before closing body.
		retryAfter := parseRetryAfter(resp.Header.Get("retry-after"))
		decodeErr := decodeErrorFromResp(resp)
		// Must consume and close before retrying.
		_ = resp.Body.Close()

		if attempt == retryMaxCount {
			return nil, decodeErr
		}

		d := retryDelay(attempt, retryAfter)
		if err := sleepFn(ctx, d); err != nil {
			return nil, err
		}
	}
	return resp, err
}

// NewClientWithProxy builds a Client whose transport honours HTTPS_PROXY /
// HTTP_PROXY environment variables (via http.ProxyFromEnvironment). This is
// the recommended constructor for production use; NewClient is kept for tests
// that supply their own httptest.Server transport.
func NewClientWithProxy(cfg Config) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	// If explicit proxy env vars are set, override with parsed URL.
	if proxyURL := explicitProxyURL(); proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return NewClient(cfg, &http.Client{Transport: transport})
}

// explicitProxyURL checks HTTPS_PROXY then HTTP_PROXY for an explicit URL.
func explicitProxyURL() *url.URL {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := os.Getenv(key); v != "" {
			u, err := url.Parse(v)
			if err == nil {
				return u
			}
		}
	}
	return nil
}
