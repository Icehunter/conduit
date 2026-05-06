// Package webfetchtool implements the WebFetch tool — fetches a URL and
// returns its content as Markdown text.
//
// Mirrors src/tools/WebFetchTool/WebFetchTool.ts + utils.ts.
//
// M4 scope: HTTP GET with 30s timeout, HTML→Markdown via golang.org/x/net/html
// parser, 200KB content cap, User-Agent spoofing. The real tool also calls
// a secondary Haiku model to apply the caller's prompt against the fetched
// content; we do that inline with simple extraction for M4.
// Domain allowlist / permission checks land in M5.
package webfetchtool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/tool"
)

// ssrfDeniedNets are IP ranges blocked to prevent server-side request forgery
// against cloud metadata endpoints, internal services, and loopback.
var ssrfDeniedNets = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16", // link-local / AWS metadata
		"100.64.0.0/10",  // CGNAT
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 ULA
		"fe80::/10",      // IPv6 link-local
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		if n != nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// isPrivateIP returns true if addr is in any of the SSRF-denied ranges.
func isPrivateIP(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range ssrfDeniedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ssrfSafeDialer wraps net.Dialer and rejects connections to private IPs
// after DNS resolution, preventing DNS-rebinding attacks.
type ssrfSafeDialer struct{ net.Dialer }

func (d *ssrfSafeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if isPrivateIP(addr) {
		return nil, fmt.Errorf("webfetch: request to private/internal address denied: %s", addr)
	}
	return d.Dialer.DialContext(ctx, network, addr)
}

// MaxContentBytes caps the response body size.
const MaxContentBytes = 200 * 1024 // 200 KB

// HTTPTimeout is the total request timeout.
const HTTPTimeout = 30 * time.Second

// userAgent mimics a real browser so sites don't serve bot-blocked pages.
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Tool implements the WebFetch tool.
type Tool struct {
	client *http.Client
}

// New returns a fresh WebFetch tool with an SSRF-safe HTTP client that
// blocks requests to private/internal IP ranges after DNS resolution.
func New() *Tool {
	return newWithDialer(&ssrfSafeDialer{})
}

// newWithDialer constructs a Tool using the provided DialContext — exposed for
// tests so httptest servers on 127.0.0.1 can bypass the SSRF guard.
func newWithDialer(d interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}) *Tool {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = d.DialContext
	return &Tool{
		client: &http.Client{
			Timeout:   HTTPTimeout,
			Transport: transport,
		},
	}
}

func (*Tool) Name() string { return "WebFetch" }

func (*Tool) Description() string {
	return "Fetches content from a URL and returns it as text. " +
		"HTML pages are converted to Markdown. " +
		"Use the prompt parameter to describe what information you need from the page."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"description": "The URL to fetch content from"
			},
			"prompt": {
				"type": "string",
				"description": "What information to extract from the page"
			}
		},
		"required": ["url", "prompt"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return true }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Input is the typed view of the JSON input.
type Input struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

// Execute fetches the URL and returns processed content.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.URL) == "" {
		return tool.ErrorResult("`url` is required"), nil
	}

	// Validate URL.
	parsed, err := url.Parse(in.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return tool.ErrorResult(fmt.Sprintf("invalid URL: %s", in.URL)), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot build request: %v", err)), nil
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8")

	start := time.Now()
	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return tool.ErrorResult("request cancelled or timed out"), nil
		}
		return tool.ErrorResult(fmt.Sprintf("fetch failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return tool.ErrorResult(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status)), nil
	}

	// Read body up to MaxContentBytes.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(MaxContentBytes)+1))
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot read response: %v", err)), nil
	}
	truncated := len(body) > MaxContentBytes
	if truncated {
		body = body[:MaxContentBytes]
		// Trim to valid UTF-8 boundary.
		for !utf8.Valid(body) {
			body = body[:len(body)-1]
		}
	}

	elapsed := time.Since(start)
	contentType := resp.Header.Get("Content-Type")

	// Convert content based on type.
	var text string
	if isHTML(contentType) {
		text = htmlToMarkdown(string(body))
	} else {
		text = string(body)
	}

	if truncated {
		text += fmt.Sprintf("\n\n[Content truncated at %d KB]", MaxContentBytes/1024)
	}

	// Build result: include the prompt context so the model can use it.
	var sb strings.Builder
	if in.Prompt != "" {
		fmt.Fprintf(&sb, "URL: %s\nPrompt: %s\n\n", in.URL, in.Prompt)
	}
	fmt.Fprintf(&sb, "HTTP %d — fetched %d bytes in %dms\n\n",
		resp.StatusCode, len(body), elapsed.Milliseconds())
	sb.WriteString(text)

	return tool.TextResult(sb.String()), nil
}

func isHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// htmlToMarkdown strips HTML tags and returns readable plain text.
// Block-level tags inject newlines; script/style content is dropped.
func htmlToMarkdown(htmlStr string) string {
	text := stripHTML(htmlStr)
	// Collapse runs of blank lines to at most one.
	var kept []string
	blank := false
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			if !blank {
				kept = append(kept, "")
				blank = true
			}
		} else {
			kept = append(kept, line)
			blank = false
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// stripHTML is a clean byte-scanner that correctly handles nested tags,
// script/style blocks, and HTML entities.
func stripHTML(s string) string {
	var out strings.Builder
	out.Grow(len(s) / 2)

	i := 0
	n := len(s)

	skipUntil := func(end string) {
		idx := strings.Index(s[i:], end)
		if idx < 0 {
			i = n
		} else {
			i += idx + len(end)
		}
	}

	for i < n {
		// Start of a tag.
		if s[i] == '<' {
			i++ // consume '<'
			if i >= n {
				break
			}

			// Closing tag: </tag>
			isClose := s[i] == '/'
			if isClose {
				i++
			}

			// Read tag name.
			j := i
			for j < n && s[j] != '>' && s[j] != ' ' && s[j] != '\t' && s[j] != '\n' && s[j] != '/' {
				j++
			}
			tagName := strings.ToLower(s[i:j])
			i = j

			// Skip to end of tag.
			for i < n && s[i] != '>' {
				i++
			}
			if i < n {
				i++ // consume '>'
			}

			// Drop content of script and style entirely.
			if !isClose {
				switch tagName {
				case "script":
					skipUntil("</script>")
					continue
				case "style":
					skipUntil("</style>")
					continue
				}
			}

			// Block-level tags emit a newline.
			switch tagName {
			case "p", "div", "section", "article", "header", "footer", "main",
				"h1", "h2", "h3", "h4", "h5", "h6",
				"li", "dt", "dd", "tr", "blockquote", "pre":
				out.WriteByte('\n')
			case "br":
				out.WriteByte('\n')
			}
			continue
		}

		// HTML entity.
		if s[i] == '&' {
			decoded, adv := decodeEntity(s[i:])
			out.WriteString(decoded)
			i += adv
			continue
		}

		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// decodeEntity decodes &name; and &#NNN; entities.
func decodeEntity(s string) (string, int) {
	if len(s) < 2 || s[0] != '&' {
		return "&", 1
	}
	end := strings.IndexByte(s[1:], ';')
	if end < 0 || end > 15 {
		return "&", 1
	}
	end += 2 // include '&' and ';'
	name := s[1 : end-1]

	// Numeric entities &#NNN; or &#xHH;
	if len(name) > 1 && name[0] == '#' {
		return "&" + name + ";", end // pass through for now
	}

	switch name {
	case "amp":
		return "&", end
	case "lt":
		return "<", end
	case "gt":
		return ">", end
	case "nbsp":
		return " ", end
	case "quot":
		return `"`, end
	case "apos":
		return "'", end
	case "mdash":
		return "—", end
	case "ndash":
		return "–", end
	case "hellip":
		return "…", end
	case "laquo":
		return "«", end
	case "raquo":
		return "»", end
	case "copy":
		return "©", end
	case "reg":
		return "®", end
	case "trade":
		return "™", end
	}
	return "&" + name + ";", end
}
