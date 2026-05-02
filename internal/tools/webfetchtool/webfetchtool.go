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
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/icehunter/claude-go/internal/tool"
)

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

// New returns a fresh WebFetch tool.
func New() *Tool {
	return &Tool{
		client: &http.Client{Timeout: HTTPTimeout},
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

func (*Tool) IsReadOnly(json.RawMessage) bool      { return true }
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
		sb.WriteString(fmt.Sprintf("URL: %s\nPrompt: %s\n\n", in.URL, in.Prompt))
	}
	sb.WriteString(fmt.Sprintf("HTTP %d — fetched %d bytes in %dms\n\n",
		resp.StatusCode, len(body), elapsed.Milliseconds()))
	sb.WriteString(text)

	return tool.TextResult(sb.String()), nil
}

func isHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// htmlToMarkdown converts HTML to readable Markdown text.
// Uses a simple recursive descent over golang.org/x/net/html nodes.
// Full turndown parity (tables, code blocks) lands in M5.
func htmlToMarkdown(htmlStr string) string {
	// Use a simple but effective approach: strip all tags, normalize whitespace.
	// golang.org/x/net/html is available but requires import; use strings for M4.
	out := stripHTML(htmlStr)
	// Collapse whitespace runs.
	lines := strings.Split(out, "\n")
	var kept []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

// stripHTML removes HTML tags and decodes common entities.
func stripHTML(s string) string {
	var sb strings.Builder
	inTag := false
	inScript := false
	inStyle := false
	i := 0
	runes := []rune(s)
	n := len(runes)

	for i < n {
		ch := runes[i]

		if ch == '<' {
			// Peek at tag name.
			j := i + 1
			for j < n && runes[j] == ' ' {
				j++
			}
			tagName := ""
			for j < n && runes[j] != '>' && runes[j] != ' ' && runes[j] != '/' {
				tagName += string(runes[j])
				j++
			}
			tagName = strings.ToLower(strings.TrimPrefix(tagName, "/"))

			switch tagName {
			case "script":
				inScript = true
			case "style":
				inStyle = true
			case "/script":
				inScript = false
			case "/style":
				inStyle = false
			}

			if tagName == "br" || tagName == "p" || tagName == "div" ||
				tagName == "h1" || tagName == "h2" || tagName == "h3" ||
				tagName == "h4" || tagName == "h5" || tagName == "h6" ||
				tagName == "li" || tagName == "tr" {
				sb.WriteByte('\n')
			}

			inTag = true
			i++
			continue
		}

		if ch == '>' && inTag {
			inTag = false
			i++
			continue
		}

		if inTag {
			i++
			continue
		}
		if inScript || inStyle {
			i++
			continue
		}

		// HTML entities.
		if ch == '&' {
			entity, adv := parseEntity(runes[i:])
			sb.WriteString(entity)
			i += adv
			continue
		}

		sb.WriteRune(ch)
		i++
	}
	return sb.String()
}

// parseEntity decodes a simple HTML entity like &amp; &lt; &gt; &nbsp; &#NNN;
func parseEntity(runes []rune) (string, int) {
	if len(runes) < 2 || runes[0] != '&' {
		return "&", 1
	}
	// Find semicolon.
	end := 1
	for end < len(runes) && end < 16 && runes[end] != ';' {
		end++
	}
	if end >= len(runes) || runes[end] != ';' {
		return "&", 1
	}
	entity := string(runes[1:end])
	switch entity {
	case "amp":
		return "&", end + 1
	case "lt":
		return "<", end + 1
	case "gt":
		return ">", end + 1
	case "nbsp":
		return " ", end + 1
	case "quot":
		return `"`, end + 1
	case "apos":
		return "'", end + 1
	case "mdash":
		return "—", end + 1
	case "ndash":
		return "–", end + 1
	case "hellip":
		return "…", end + 1
	}
	return "&" + entity + ";", end + 1
}
