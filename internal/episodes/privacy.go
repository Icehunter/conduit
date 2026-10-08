package episodes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/icehunter/conduit/internal/rtk"
)

var recorderAssignment = regexp.MustCompile(`(?i)((?:password|passwd|api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|secret)\s*[=:]\s*["']?)[^\s"',;}]+`)
var privateReasoning = regexp.MustCompile(`(?is)<(?:think|thinking)>.*?(?:</(?:think|thinking)>|$)`)

// SourceAllowed checks lexical boundaries and every existing ancestor for
// symlink escapes. Missing paths are allowed only inside an allowed ancestor.
func (r *Recorder) SourceAllowed(path string) bool {
	if r == nil || !r.cfg.Enabled || !r.cfg.IncludeContent {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.cfg.RepositoryRoot, path)
	}
	rel, err := filepath.Rel(r.cfg.RepositoryRoot, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	for current := path; ; current = filepath.Dir(current) {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			rr, err := filepath.Rel(r.cfg.RepositoryRoot, resolved)
			if err != nil || rr == ".." || strings.HasPrefix(rr, ".."+string(filepath.Separator)) {
				return false
			}
			break
		}
		if !os.IsNotExist(err) || current == filepath.Dir(current) {
			return false
		}
	}
	rel = filepath.ToSlash(rel)
	for _, part := range strings.Split(rel, "/") {
		low := strings.ToLower(part)
		if low == ".git" || low == ".ssh" || low == ".aws" || low == ".env" || strings.HasPrefix(low, ".env.") || low == "credentials" || strings.HasSuffix(low, ".pem") || strings.HasSuffix(low, ".key") {
			return false
		}
	}
	for _, pattern := range r.cfg.ExcludedPaths {
		match, err := doublestar.Match(pattern, rel)
		if err != nil || match {
			return false
		}
	}
	for _, pattern := range r.cfg.AllowedPaths {
		match, err := doublestar.Match(pattern, rel)
		if err == nil && match {
			return true
		}
	}
	return false
}

func (r *Recorder) sanitize(e *Event) error {
	// Do not persist invalid IDs or credentials disguised as metadata.
	for _, p := range []*string{&e.Kind, &e.EpisodeID, &e.RunID, &e.ParentRunID, &e.AttemptID, &e.RequestID, &e.ToolCallID, &e.Origin} {
		*p = r.clean(*p, &e.Redacted)
	}
	sourceDenied := false
	normalized := make([]string, 0, len(e.Sources))
	for _, p := range e.Sources {
		if !r.SourceAllowed(p) {
			sourceDenied = true
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(r.cfg.RepositoryRoot, p)
		}
		rel, err := filepath.Rel(r.cfg.RepositoryRoot, p)
		if err != nil {
			return fmt.Errorf("episodes: source path: %w", err)
		}
		normalized = append(normalized, r.clean(filepath.ToSlash(rel), &e.Redacted))
	}
	e.Sources = normalized
	if sourceDenied || !r.cfg.IncludeContent {
		e.Incomplete = true
		e.Redacted = true
		e.Payload = map[string]any{"omitted": "content policy"}
		return nil
	}
	b, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("episodes: copy payload: %w", err)
	}
	var data any
	if err := json.Unmarshal(b, &data); err != nil {
		return fmt.Errorf("episodes: decode payload: %w", err)
	}
	e.Payload = r.scrub(data, &e.Redacted)
	return nil
}

func (r *Recorder) clean(s string, redacted *bool) string {
	out, n := rtk.RedactSecrets(s)
	if n > 0 {
		*redacted = true
	}
	if recorderAssignment.MatchString(out) {
		out = recorderAssignment.ReplaceAllString(out, "${1}[OMITTED:private]")
		*redacted = true
	}
	if privateReasoning.MatchString(out) {
		out = privateReasoning.ReplaceAllString(out, "[OMITTED:reasoning]")
		*redacted = true
	}
	// Absolute identity paths are not necessary to reproduce a coding example.
	beforePaths := out
	out = strings.ReplaceAll(out, r.cfg.RepositoryRoot, "<repo>")
	if r.inputRoot != "" {
		out = strings.ReplaceAll(out, r.inputRoot, "<repo>")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = strings.ReplaceAll(out, home, "<home>")
	}
	if out != beforePaths {
		*redacted = true
	}
	return out
}

func (r *Recorder) scrub(v any, redacted *bool) any {
	switch x := v.(type) {
	case string:
		return r.clean(x, redacted)
	case []any:
		for i, value := range x {
			x[i] = r.scrub(value, redacted)
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, value := range x {
			key := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "-", "_"), " ", "_"))
			if sensitiveKey(key) {
				out[r.clean(k, redacted)] = "[OMITTED:private]"
				*redacted = true
				continue
			}
			out[r.clean(k, redacted)] = r.scrub(value, redacted)
		}
		return out
	default:
		return v
	}
}

func sensitiveKey(k string) bool {
	switch k {
	case "authorization", "proxy_authorization", "api_key", "apikey", "password", "passwd", "secret", "credentials", "access_token", "accesstoken", "refresh_token", "refreshtoken", "id_token", "token", "cookie", "set_cookie", "env", "environment", "thinking", "reasoning", "reasoning_content", "signature", "thought_signature", "image", "image_url", "data", "base64":
		return true
	}
	return strings.HasSuffix(k, "_password") || strings.HasSuffix(k, "_secret") || strings.HasSuffix(k, "_api_key")
}
