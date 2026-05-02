package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

const defaultHTTPTimeout = 10 * time.Minute

// runHTTPHook POSTs hook input as JSON to the configured URL and interprets
// the JSON response as a HookOutput directive. Mirrors execHttpHook.ts.
func runHTTPHook(ctx context.Context, hook settings.Hook, input HookInput) Result {
	if hook.URL == "" {
		return Result{Blocked: true, Reason: "http hook has no url"}
	}

	payload, err := json.Marshal(input)
	if err != nil {
		return Result{Blocked: true, Reason: fmt.Sprintf("http hook: marshal: %v", err)}
	}

	timeout := defaultHTTPTimeout
	if hook.TimeoutSecs > 0 {
		timeout = time.Duration(hook.TimeoutSecs) * time.Second
	}

	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(hctx, http.MethodPost, hook.URL, bytes.NewReader(payload))
	if err != nil {
		return Result{Blocked: true, Reason: fmt.Sprintf("http hook: build request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	// Apply custom headers with optional env var interpolation.
	for k, v := range hook.Headers {
		req.Header.Set(k, interpolateEnv(v, hook.AllowedEnvVars))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{Blocked: true, Reason: fmt.Sprintf("http hook: request failed: %v", err)}
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{Blocked: true, Reason: fmt.Sprintf("http hook: server returned %d", resp.StatusCode)}
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := strings.TrimSpace(string(body))
	if out == "" {
		return Result{}
	}
	var directive HookOutput
	if err := json.Unmarshal([]byte(out), &directive); err != nil {
		return Result{} // non-JSON response is advisory
	}
	switch strings.ToLower(directive.Decision) {
	case "block":
		reason := directive.Reason
		if reason == "" {
			reason = "blocked by http hook"
		}
		return Result{Blocked: true, Reason: reason}
	case "approve":
		return Result{Approved: true}
	}
	return Result{}
}

// interpolateEnv replaces $VAR and ${VAR} references in s with env values,
// but only for vars explicitly listed in allowed. Unallowed refs become "".
func interpolateEnv(s string, allowed []string) string {
	if len(allowed) == 0 || !strings.Contains(s, "$") {
		return s
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, v := range allowed {
		allowedSet[v] = true
	}

	// Replace ${VAR} and $VAR patterns.
	result := os.Expand(s, func(key string) string {
		if allowedSet[key] {
			return os.Getenv(key)
		}
		return ""
	})
	return result
}
