package api

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// AnthropicVersion is the value sent for the `anthropic-version` header on
// every request. Hardcoded to match decoded/0168.js:370.
const AnthropicVersion = "2023-06-01"

// SDKPackageVersion is the @anthropic-ai/sdk version we identify as. The
// real CLI bundles SDK 0.81.0 (decoded/0133.js:7); since Anthropic's API
// rate-limits clients whose Stainless headers don't look like the official
// CLI's, we report the same string.
const SDKPackageVersion = "0.81.0"

// Config configures a Client.
type Config struct {
	// ProviderKind selects the request/response wire dialect. Empty means
	// Anthropic Messages API. "openai-compatible" uses /chat/completions.
	ProviderKind string
	// BaseURL is the API origin, e.g. https://api.anthropic.com. No trailing slash.
	BaseURL string
	// AuthToken is the OAuth bearer token. Required when APIKey is empty.
	// Mutually exclusive with APIKey at runtime — set exactly one.
	AuthToken string
	// APIKey is the legacy x-api-key. Set this xor AuthToken.
	APIKey string
	// BetaHeaders are joined into the `anthropic-beta` header.
	// For OAuth-Max users include "oauth-2025-04-20".
	BetaHeaders []string
	// SessionID becomes the X-Claude-Code-Session-Id header.
	SessionID string
	// UserAgent overrides the default User-Agent. Leave empty for default.
	UserAgent string
	// ClaudeCodeID is the value of the `x-app` header. Defaults to "cli".
	ClaudeCodeID string
	// OnAuth401 is called when the API returns 401 to refresh credentials.
	// It must update the same Client's AuthToken and return nil to retry,
	// or return an error to surface to the caller. The retry is done at
	// most once per outbound request. Reference: decoded/4500.js:32-44.
	OnAuth401 func(ctx context.Context) error
	// ExtraHeaders are merged into every request after the standard set.
	// Use for headers like `anthropic-dangerous-direct-browser-access` that
	// the real CLI sends but aren't part of the canonical core set.
	ExtraHeaders map[string]string
}

// Client posts to /v1/messages.
type Client struct {
	mu   sync.Mutex // guards cfg.AuthToken across refresh
	cfg  Config
	http *http.Client
}

// NewClient returns a Client. If httpClient is nil, http.DefaultClient is used.
func NewClient(cfg Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.ClaudeCodeID == "" {
		cfg.ClaudeCodeID = "cli"
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = fmt.Sprintf("claude-cli/0.0.0 (%s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	return &Client{cfg: cfg, http: httpClient}
}

// SetAuthToken updates the bearer token used for subsequent requests.
// Safe to call from any goroutine, including from inside an OnAuth401 callback.
func (c *Client) SetAuthToken(tok string) {
	c.mu.Lock()
	c.cfg.AuthToken = tok
	c.mu.Unlock()
}

// CreateMessage performs a non-streaming POST /v1/messages?beta=true.
//
// On 401 the Client invokes OnAuth401 to refresh credentials, then retries
// the same request once. Reference: decoded/4500.js:32-44 (single retry on
// 401 after refresh handler runs).
//
// Errors from the API are returned as a fmt.Errorf-wrapped error containing
// the error type and message, so callers can `errors.As` against APIError
// for finer control once we add typed errors in M2.
func (c *Client) CreateMessage(ctx context.Context, req *MessageRequest) (*MessageResponse, error) {
	if c.cfg.ProviderKind == "openai-compatible" {
		return c.createOpenAICompatible(ctx, req)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("api: marshal request: %w", err)
	}

	// withRetry handles 429 with exponential backoff, mirroring StreamMessage.
	resp, err := withRetry(ctx, func() (*http.Response, error) {
		return c.do(ctx, body, req.Model)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	// Single 401 retry after refresh.
	if resp.StatusCode == http.StatusUnauthorized && c.cfg.OnAuth401 != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err := c.cfg.OnAuth401(ctx); err != nil {
			return nil, fmt.Errorf("api: refresh on 401: %w", err)
		}
		resp, err = withRetry(ctx, func() (*http.Response, error) {
			return c.do(ctx, body, req.Model)
		})
		if err != nil {
			return nil, err
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.decodeError(resp)
	}

	var out MessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("api: decode response: %w", err)
	}
	return &out, nil
}

// do builds and sends the request. The body is buffered, so retries simply
// rebuild a fresh reader from the same bytes.
func (c *Client) do(ctx context.Context, body []byte, model string) (*http.Response, error) {
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/messages?beta=true"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("api: build request: %w", err)
	}
	c.applyHeaders(httpReq.Header, model)

	if os.Getenv("CLAUDE_GO_DEBUG_HTTP") == "1" {
		fmt.Fprintf(os.Stderr, "\n[CLAUDE_GO_DEBUG_HTTP] >>> POST %s\n", url)
		// Print headers in stable alphabetical order. Redact Authorization.
		keys := make([]string, 0, len(httpReq.Header))
		for k := range httpReq.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := httpReq.Header.Get(k)
			if isCredentialHeader(k) {
				v = "(redacted)"
			}
			fmt.Fprintf(os.Stderr, "  %s: %s\n", k, v)
		}
		fmt.Fprintf(os.Stderr, "  body bytes: %d\n", len(body))
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("api: send: %w", err)
	}
	return resp, nil
}

func (c *Client) applyHeaders(h http.Header, model string) {
	c.mu.Lock()
	tok := c.cfg.AuthToken
	apiKey := c.cfg.APIKey
	c.mu.Unlock()

	h.Set("Accept", "application/json")
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", c.cfg.UserAgent)
	h.Set("anthropic-version", AnthropicVersion)
	h.Set("x-app", c.cfg.ClaudeCodeID)
	if c.cfg.SessionID != "" {
		h.Set("X-Claude-Code-Session-Id", c.cfg.SessionID)
	}
	if len(c.cfg.BetaHeaders) > 0 {
		betas := filterBetasForModel(c.cfg.BetaHeaders, model)
		if len(betas) > 0 {
			// Decoded reference joins with comma-no-space via Array.toString
			// (decoded/0158.js:55,67,84). Match exactly for byte equivalence.
			h.Set("anthropic-beta", strings.Join(betas, ","))
		}
	}

	// Stainless SDK fingerprint headers. The official CLI ships these on
	// every request via @anthropic-ai/sdk; the API rate-limits clients
	// missing them, so we replicate them exactly. Reference:
	// decoded/0168.js:362-370 (call site) and decoded/0133.js:47-95
	// (AQK() builder), with version literal D4H="0.81.0".
	h.Set("X-Stainless-Lang", "js")
	h.Set("X-Stainless-Package-Version", SDKPackageVersion)
	h.Set("X-Stainless-OS", stainlessOS())
	h.Set("X-Stainless-Arch", stainlessArch())
	h.Set("X-Stainless-Runtime", "node")
	h.Set("X-Stainless-Runtime-Version", "v24.3.0") // matches Bun's reported node compatibility (v133)

	// Per-request correlation ID — new in v133 (CLIENT_REQUEST_ID_HEADER).
	var reqID [16]byte
	_, _ = crand.Read(reqID[:])
	reqID[6] = (reqID[6] & 0x0f) | 0x40
	reqID[8] = (reqID[8] & 0x3f) | 0x80
	h.Set("x-client-request-id",
		hex.EncodeToString(reqID[:4])+"-"+
			hex.EncodeToString(reqID[4:6])+"-"+
			hex.EncodeToString(reqID[6:8])+"-"+
			hex.EncodeToString(reqID[8:10])+"-"+
			hex.EncodeToString(reqID[10:]))

	if tok != "" {
		h.Set("Authorization", "Bearer "+tok)
	} else if apiKey != "" {
		h.Set("x-api-key", apiKey)
	}

	// Merge caller-supplied extras. Identity/authentication headers are locked
	// and cannot be overridden by ExtraHeaders to prevent spoofing.
	for k, v := range c.cfg.ExtraHeaders {
		if !lockedHeaders[strings.ToLower(k)] {
			h.Set(k, v)
		}
	}
}

// lockedHeaders is the set of header names (lower-cased) that ExtraHeaders
// must not override. These carry identity, auth, and wire-fingerprint
// information that must match what the server expects.
var lockedHeaders = map[string]bool{
	"user-agent":                  true,
	"x-app":                       true,
	"authorization":               true,
	"anthropic-beta":              true,
	"x-api-key":                   true,
	"x-stainless-lang":            true,
	"x-stainless-package-version": true,
	"x-stainless-runtime":         true,
	"x-stainless-runtime-version": true,
	"x-stainless-timeout":         true,
}

// stainlessOS maps GOOS to the value the Stainless SDK reports
// (decoded/0133.js:104-114, function Jg8).
func stainlessOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	case "freebsd":
		return "FreeBSD"
	case "openbsd":
		return "OpenBSD"
	default:
		return "Other:" + runtime.GOOS
	}
}

// stainlessArch maps GOARCH to the value the Stainless SDK reports
// (decoded/0133.js:96-103, function jg8).
func stainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "x32"
	case "arm":
		return "arm"
	case "arm64":
		return "arm64"
	default:
		return "other:" + runtime.GOARCH
	}
}

// decodeErrorFromResp is the package-level error decoder used by retry logic
// before the Client is available (e.g. inside withRetry).
func decodeErrorFromResp(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var rl string
	if resp.StatusCode == http.StatusTooManyRequests {
		var bits []string
		if v := resp.Header.Get("retry-after"); v != "" {
			bits = append(bits, "retry-after="+v+"s")
		}
		for _, h := range []string{
			"anthropic-ratelimit-unified-status",
			"anthropic-ratelimit-requests-remaining",
			"anthropic-ratelimit-input-tokens-remaining",
		} {
			if v := resp.Header.Get(h); v != "" {
				bits = append(bits, strings.TrimPrefix(h, "anthropic-ratelimit-")+"="+v)
			}
		}
		if len(bits) > 0 {
			rl = " [" + strings.Join(bits, " ") + "]"
		}
	}
	var env APIErrorEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Error.Type != "" {
		return fmt.Errorf("api: %d %s: %s: %s%s",
			resp.StatusCode, http.StatusText(resp.StatusCode),
			env.Error.Type, env.Error.Message, rl)
	}
	return fmt.Errorf("api: %d %s: %s%s",
		resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(raw)), rl)
}

// isCredentialHeader returns true for any header whose value should be
// redacted in debug output. Covers all common credential header names plus
// any header whose name contains "token", "secret", "api-key", or "auth".
func isCredentialHeader(name string) bool {
	lower := strings.ToLower(name)
	explicit := map[string]bool{
		"authorization":       true,
		"x-api-key":           true,
		"cookie":              true,
		"set-cookie":          true,
		"anthropic-api-key":   true,
		"x-anthropic-api-key": true,
	}
	if explicit[lower] {
		return true
	}
	for _, kw := range []string{"token", "secret", "api-key", "-auth"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func (c *Client) decodeError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// Debug hatch: when CLAUDE_GO_DEBUG_HTTP=1 dump the raw response body
	// + status line + selected headers to stderr so we can see what the API
	// is actually rejecting. Off by default; never includes Authorization.
	if os.Getenv("CLAUDE_GO_DEBUG_HTTP") == "1" {
		fmt.Fprintf(os.Stderr, "\n[CLAUDE_GO_DEBUG_HTTP] %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))
		for _, h := range []string{
			"request-id",
			"x-should-retry",
			"retry-after",
			"anthropic-ratelimit-unified-status",
			"anthropic-ratelimit-unified-reset",
			"anthropic-ratelimit-unified-fallback-percentage",
			"anthropic-ratelimit-unified-overage-status",
			"anthropic-ratelimit-unified-overage-disabled-reason",
			"anthropic-organization-id",
			"cf-ray",
		} {
			if v := resp.Header.Get(h); v != "" {
				fmt.Fprintf(os.Stderr, "  %s: %s\n", h, v)
			}
		}
		fmt.Fprintf(os.Stderr, "  body: %s\n", strings.TrimSpace(string(raw)))
	}

	// Surface rate-limit context when the API tells us we're throttled.
	// Headers are documented at https://docs.anthropic.com/en/api/rate-limits.
	var rl string
	if resp.StatusCode == http.StatusTooManyRequests {
		var bits []string
		if v := resp.Header.Get("retry-after"); v != "" {
			bits = append(bits, "retry-after="+v+"s")
		}
		for _, h := range []string{
			"anthropic-ratelimit-unified-status",
			"anthropic-ratelimit-unified-reset",
			"anthropic-ratelimit-unified-fallback-percentage",
			"anthropic-ratelimit-requests-remaining",
			"anthropic-ratelimit-requests-reset",
			"anthropic-ratelimit-input-tokens-remaining",
			"anthropic-ratelimit-input-tokens-reset",
			"anthropic-ratelimit-output-tokens-remaining",
			"anthropic-ratelimit-output-tokens-reset",
		} {
			if v := resp.Header.Get(h); v != "" {
				bits = append(bits, strings.TrimPrefix(h, "anthropic-ratelimit-")+"="+v)
			}
		}
		if len(bits) > 0 {
			rl = " [" + strings.Join(bits, " ") + "]"
		}
	}

	var env APIErrorEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Error.Type != "" {
		return fmt.Errorf("api: %d %s: %s: %s%s",
			resp.StatusCode, http.StatusText(resp.StatusCode),
			env.Error.Type, env.Error.Message, rl)
	}
	return fmt.Errorf("api: %d %s: %s%s",
		resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(raw)), rl)
}
