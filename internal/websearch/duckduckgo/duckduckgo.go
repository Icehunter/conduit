// Package duckduckgo implements a keyless websearch.SearchProvider that parses
// DuckDuckGo's no-JS HTML results page (https://html.duckduckgo.com/html/).
//
// The page is scraped with tolerant regular expressions rather than a strict
// HTML parser: DuckDuckGo reorders attributes and wraps outbound links in a
// redirect, so the parser keys on class names and unwraps the target URL.
package duckduckgo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/icehunter/conduit/internal/websearch"
)

const (
	defaultEndpoint = "https://html.duckduckgo.com/html/"
	defaultMax      = 10
)

// Provider is a keyless DuckDuckGo search provider.
type Provider struct {
	httpClient *http.Client
	endpoint   string
}

// New returns a DuckDuckGo provider.
func New() *Provider {
	return &Provider{httpClient: websearch.NewHTTPClient(), endpoint: defaultEndpoint}
}

func newWithClient(c *http.Client, endpoint string) *Provider {
	return &Provider{httpClient: c, endpoint: endpoint}
}

func (p *Provider) Name() string { return "DuckDuckGo" }

// Search posts the query to the HTML endpoint and parses the result blocks.
func (p *Provider) Search(ctx context.Context, q websearch.Query) ([]websearch.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, websearch.DefaultTimeout)
	defer cancel()
	form := url.Values{"q": {q.Text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", websearch.UserAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := websearch.ReadBody(resp)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: read response: %w", err)
	}
	page := string(body)
	// DDG mixes 200 and 202 for its bot challenge, so check the body first.
	if isAnomaly(page) {
		return nil, fmt.Errorf("duckduckgo: blocked by bot challenge")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo: status %d", resp.StatusCode)
	}

	max := q.MaxResults
	if max <= 0 {
		max = defaultMax
	}
	results := websearch.DedupeURLs(parseResults(page))
	results = websearch.FilterDomains(results, q.AllowedDomains, q.BlockedDomains)
	return websearch.Limit(results, max), nil
}

func isAnomaly(page string) bool {
	return strings.Contains(page, "anomaly-modal") || strings.Contains(page, "anomaly.js")
}

var (
	blockStartRe = regexp.MustCompile(`(?i)<div\b[^>]*\bclass="[^"]*\bresult\b[^"]*"[^>]*>`)
	adClassRe    = regexp.MustCompile(`(?i)\bclass="[^"]*\bresult--ad\b`)
	titleRe      = regexp.MustCompile(`(?is)<a\b[^>]*\bclass="[^"]*\bresult__a\b[^"]*"[^>]*>(.*?)</a>`)
	hrefRe       = regexp.MustCompile(`(?i)\bhref="([^"]+)"`)
	snippetRe    = regexp.MustCompile(`(?is)<(?:a|div|span)\b[^>]*\bclass="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</(?:a|div|span)>`)
)

// parseResults extracts result blocks in document order. A block runs from one
// result container to the next; sponsored rows, rows without a title link and
// links that stay on duckduckgo.com are skipped.
func parseResults(page string) []websearch.Result {
	starts := blockStartRe.FindAllStringIndex(page, -1)
	var out []websearch.Result
	for i, s := range starts {
		end := len(page)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		block := page[s[0]:end]
		if adClassRe.MatchString(block[:s[1]-s[0]]) {
			continue
		}
		t := titleRe.FindStringSubmatchIndex(block)
		if t == nil {
			continue
		}
		anchor := block[t[0]:t[1]]
		openTag := anchor[:strings.IndexByte(anchor, '>')+1]
		h := hrefRe.FindStringSubmatch(openTag)
		if h == nil {
			continue
		}
		target := unwrapURL(h[1])
		title := websearch.HTMLText(block[t[2]:t[3]])
		if target == "" || title == "" {
			continue
		}
		var snippet string
		if m := snippetRe.FindStringSubmatch(block); m != nil {
			snippet = websearch.HTMLText(m[1])
		}
		out = append(out, websearch.Result{Title: title, URL: target, Snippet: snippet})
	}
	return out
}

// unwrapURL resolves a result href to the target URL. DDG routes clicks through
// //duckduckgo.com/l/?uddg=<encoded>; protocol-relative and absolute links are
// also accepted. Anything that still points at duckduckgo.com is rejected.
func unwrapURL(href string) string {
	href = strings.ReplaceAll(href, "&amp;", "&")
	var target string
	switch {
	case strings.Contains(href, "uddg="):
		u, err := url.Parse(href)
		if err != nil {
			return ""
		}
		target = u.Query().Get("uddg")
	case strings.HasPrefix(href, "//"):
		target = "https:" + href
	case hasHTTPPrefix(href):
		target = href
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h == "duckduckgo.com" || strings.HasSuffix(h, ".duckduckgo.com") {
		return ""
	}
	return target
}

func hasHTTPPrefix(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}
