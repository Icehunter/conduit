package api

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// RetryEvent describes one API retry delay. It is exposed through context so
// higher layers can render a useful "waiting for rate limit" state without the
// HTTP client importing UI packages.
type RetryEvent struct {
	Attempt int
	// MaxAttempts is the number of retries withRetry makes before giving up.
	MaxAttempts int
	Delay       time.Duration
	RetryAfter  time.Duration
	ResetAt     time.Time
	Err         error
	// Reason is the Classify reason of Err (ReasonRateLimit, ReasonOverloaded,
	// ReasonServerError, ReasonNetwork, ...). Status is the HTTP status, or 0
	// for a network error.
	Reason string
	Status int
}

type retryHandlerKey struct{}

// RetryHandler is called before sleeping for a retry. Return false to abort
// the retry and surface the decoded API error immediately.
type RetryHandler func(RetryEvent) bool

// WithRetryHandler installs a handler used by withRetry.
func WithRetryHandler(ctx context.Context, handler RetryHandler) context.Context {
	if handler == nil {
		return ctx
	}
	return context.WithValue(ctx, retryHandlerKey{}, handler)
}

// Retry parameters mirroring withRetry.ts:
//
//	base=1s, multiplier=2, max=32s, jitter=25%, maxRetries=5
const (
	retryBase       = 1 * time.Second
	retryMultiplier = 2.0
	retryMax        = 32 * time.Second
	retryMaxCount   = 5
)

// SleepContext waits for d or until ctx is done, returning ctx.Err() in the
// latter case. It is the default sleep for retry backoff.
func SleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sleepFn is replaced in tests to avoid real sleeps.
var sleepFn = SleepContext

type sleepKey struct{}

// WithSleep installs fn as the sleep withRetry uses for backoff, so callers
// (and tests) can avoid real waits. A nil fn leaves ctx unchanged.
func WithSleep(ctx context.Context, fn func(context.Context, time.Duration) error) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, sleepKey{}, fn)
}

func sleepFor(ctx context.Context, d time.Duration) error {
	if fn, ok := ctx.Value(sleepKey{}).(func(context.Context, time.Duration) error); ok && fn != nil {
		return fn(ctx, d)
	}
	return sleepFn(ctx, d)
}

// BackoffDelay returns the wait before retry number attempt (0-based): hint
// when the server asked for a delay, otherwise 1s doubling to a 32s cap with
// ±25% jitter. Mirrors withRetry.ts.
func BackoffDelay(attempt int, hint time.Duration) time.Duration {
	if hint > 0 {
		return hint
	}
	base := float64(retryBase) * math.Pow(retryMultiplier, float64(max(attempt, 0)))
	if base > float64(retryMax) {
		base = float64(retryMax)
	}
	jitter := (rand.Float64()*0.5 - 0.25) * base
	return max(time.Duration(base+jitter), 0)
}

// maxRetryWait is the longest server-requested delay withRetry will sleep.
// Past it the error is surfaced, the way CC gives up on long subscription
// limits instead of leaving the user watching a spinner.
const maxRetryWait = 2 * time.Minute

// parseRetryAfter parses the retry-after header value.
// Returns seconds as float64, 0 if absent or unparseable.
// The header can be a decimal seconds value or an HTTP-date
// (e.g. "Thu, 08 May 2026 12:00:00 GMT" from CDN intermediaries).
func parseRetryAfter(h string) float64 {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	// Try numeric first (most common from Anthropic).
	if v, err := strconv.ParseFloat(h, 64); err == nil && v > 0 {
		return v
	}
	// Fall back to HTTP-date form (RFC 7231 / CDN intermediaries).
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d.Seconds()
		}
	}
	return 0
}

func parseRateLimitReset(h string) time.Time {
	h = strings.TrimSpace(h)
	if h == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, h); err == nil {
		return t
	}
	if t, err := http.ParseTime(h); err == nil {
		return t
	}
	return time.Time{}
}

// maxRetryAfter bounds server-sent retry delays the way the SDK does
// (2^31-1 ms, about 24.8 days). Longer values are ignored.
const maxRetryAfter = math.MaxInt32 * time.Millisecond

// retryAfterFromHeaders returns the delay the server asked for:
// retry-after-ms (float milliseconds) first, then retry-after as seconds or an
// HTTP date, the order CC's MTe() and the SDK use. A zero or unparsable
// retry-after-ms falls through to retry-after. Zero means no usable hint.
func retryAfterFromHeaders(h http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(strings.TrimSpace(h.Get("retry-after-ms")), 64); err == nil {
		if d := boundedRetryAfter(ms / 1000); d > 0 {
			return d
		}
	}
	return boundedRetryAfter(parseRetryAfter(h.Get("retry-after")))
}

// boundedRetryAfter converts seconds to a Duration. It returns 0 for values
// that are not positive or exceed maxRetryAfter, including NaN and ±Inf.
func boundedRetryAfter(secs float64) time.Duration {
	if secs > 0 && secs <= maxRetryAfter.Seconds() {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

// resetFromHeaders returns when the unified rate limit resets, from
// anthropic-ratelimit-unified-reset. The API sends Unix seconds (CC reads the
// header with Number()); RFC 3339 and HTTP dates are accepted too.
func resetFromHeaders(h http.Header) time.Time {
	v := strings.TrimSpace(h.Get("anthropic-ratelimit-unified-reset"))
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return time.Time{}
		}
		return time.Unix(secs, 0)
	}
	return parseRateLimitReset(v)
}

// shouldRetryStatus reports whether a response should be retried, in the
// SDK's order: x-should-retry first ("true" retries, "false" does not), then
// 408, 409, 429 and every 5xx. A "true" holds for any status, 401 aside, which
// withRetry leaves to the caller.
func shouldRetryStatus(code int, shouldRetryHeader string) bool {
	switch strings.ToLower(strings.TrimSpace(shouldRetryHeader)) {
	case "true":
		return true
	case "false":
		return false
	}
	return code == http.StatusRequestTimeout || code == http.StatusConflict ||
		code == http.StatusTooManyRequests || code >= 500
}

// withRetry wraps a function that returns (*http.Response, error) and retries
// before any response body is consumed: network errors and retryable statuses
// (see shouldRetryStatus), with exponential backoff or the server's
// retry-after-ms / retry-after. A retryable status whose decoded body is not
// transient, such as a 500 carrying a context-overflow message, is returned at
// once. 401 is not retried here; the caller handles it. Failures after a 200
// are the agent loop's to retry.
func withRetry(ctx context.Context, fn func() (*http.Response, error)) (*http.Response, error) {
	headerTimeouts := 0
	for attempt := 0; ; attempt++ {
		resp, err := fn()
		var (
			retryErr   error
			retryAfter time.Duration
			resetAt    time.Time
			reason     string
			status     int
		)
		if err != nil {
			c := Classify(err)
			if ctx.Err() != nil || c.Class != ClassTransient {
				return nil, err
			}
			if errors.Is(err, ErrResponseHeaderTimeout) {
				headerTimeouts++
				if headerTimeouts > maxHeaderTimeoutRetries {
					return nil, err
				}
			}
			retryErr, reason = err, c.Reason
		} else {
			if resp.StatusCode < 400 || resp.StatusCode == http.StatusUnauthorized ||
				!shouldRetryStatus(resp.StatusCode, resp.Header.Get("x-should-retry")) {
				return resp, nil
			}
			// Read the headers before the body is consumed and closed.
			retryAfter = retryAfterFromHeaders(resp.Header)
			resetAt = resetFromHeaders(resp.Header)
			explicit := strings.EqualFold(strings.TrimSpace(resp.Header.Get("x-should-retry")), "true")
			decodeErr := decodeErrorFromResp(resp)
			_ = resp.Body.Close()
			c := Classify(decodeErr)
			// The SDK retries whatever x-should-retry: true marks, whatever the
			// status; without it only a transient or rate-limit body is worth
			// another send.
			if !explicit && c.Class != ClassTransient && c.Class != ClassRateLimited {
				return nil, decodeErr
			}
			retryErr, reason, status = decodeErr, c.Reason, resp.StatusCode
		}

		if attempt >= retryMaxCount {
			return nil, retryErr
		}

		d := BackoffDelay(attempt, retryAfter)
		if handler, ok := ctx.Value(retryHandlerKey{}).(RetryHandler); ok && handler != nil {
			if !handler(RetryEvent{
				Attempt:     attempt + 1,
				MaxAttempts: retryMaxCount,
				Delay:       d,
				RetryAfter:  retryAfter,
				ResetAt:     resetAt,
				Err:         retryErr,
				Reason:      reason,
				Status:      status,
			}) {
				return nil, retryErr
			}
		}
		if d > maxRetryWait {
			return nil, retryErr
		}
		if err := sleepFor(ctx, d); err != nil {
			return nil, err
		}
	}
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
