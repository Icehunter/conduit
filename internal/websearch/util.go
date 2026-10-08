package websearch

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// UserAgent identifies conduit to keyless search hosts. It is deliberately
// plain: no browser impersonation and no credentials.
const UserAgent = "conduit-websearch/1.0 (+https://github.com/icehunter/conduit)"

// DefaultTimeout is the per-search budget for a keyless provider. Providers
// apply it with context.WithTimeout so it covers the whole exchange, body
// included, and still yields to an earlier caller deadline.
const DefaultTimeout = 20 * time.Second

// MaxBodyBytes caps how much of a response a provider reads.
const MaxBodyBytes = 4 << 20

// NewHTTPClient returns a client for provider requests. It sets no
// http.Client.Timeout (that would cut a streaming body short); callers bound
// each search with context.WithTimeout. It never forwards credentials across
// redirects to a different host.
func NewHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after %d redirects", len(via))
			}
			if req.URL.Host != via[0].URL.Host {
				// Drop anything sensitive before following a cross-host hop.
				req.Header.Del("Authorization")
				req.Header.Del("Cookie")
			}
			return nil
		},
	}
}

// SanitizeSnippet makes a short, single-line, printable excerpt of a response
// body for error messages.
func SanitizeSnippet(b []byte, max int) string {
	s := strings.ToValidUTF8(string(b), "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "") + "..."
	}
	return s
}

// ReadBody reads a (possibly gzip-encoded) response body up to MaxBodyBytes.
func ReadBody(resp *http.Response) ([]byte, error) {
	var r io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	return io.ReadAll(io.LimitReader(r, MaxBodyBytes))
}

// ScanSSE calls fn with the joined data payload of each SSE event in r.
// Iteration stops when fn returns false, on ctx cancellation, or at EOF.
func ScanSSE(ctx context.Context, r io.Reader, fn func(data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), MaxBodyBytes)
	var data []string
	flush := func() bool {
		if len(data) == 0 {
			return true
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		return fn(payload)
	}
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Text()
		switch {
		case line == "":
			if !flush() {
				return nil
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	flush()
	return nil
}

var (
	tagRe   = regexp.MustCompile(`<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// HTMLText strips tags from an HTML fragment, unescapes entities and
// collapses whitespace.
func HTMLText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(strings.ReplaceAll(s, " ", " "), " "))
}

// FilterDomains applies allowed/blocked domain rules to results by hostname
// (exact or subdomain match). Entries may be bare hosts or contain a path;
// only the host part is used. Results with unparsable URLs are dropped.
func FilterDomains(results []Result, allowed, blocked []string) []Result {
	if len(allowed) == 0 && len(blocked) == 0 {
		return results
	}
	out := make([]Result, 0, len(results))
	for _, r := range results {
		host := hostOf(r.URL)
		if host == "" {
			continue
		}
		if len(allowed) > 0 && !matchesAny(host, allowed) {
			continue
		}
		if len(blocked) > 0 && matchesAny(host, blocked) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func matchesAny(host string, domains []string) bool {
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
		if i := strings.IndexByte(d, '/'); i >= 0 {
			d = d[:i]
		}
		d = strings.TrimPrefix(d, "www.")
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) || host == "www."+d {
			return true
		}
	}
	return false
}

// Limit truncates results to max when max > 0.
func Limit(results []Result, max int) []Result {
	if max > 0 && len(results) > max {
		return results[:max]
	}
	return results
}

// DedupeURLs removes results whose URL was already seen, keeping order.
func DedupeURLs(results []Result) []Result {
	seen := make(map[string]bool, len(results))
	out := results[:0:0]
	for _, r := range results {
		if r.URL == "" || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		out = append(out, r)
	}
	return out
}
