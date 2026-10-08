// Package mojeek implements a keyless websearch.SearchProvider that parses
// Mojeek's HTML results page.
//
// Mojeek serves an ALTCHA proof-of-work wall to scripted clients from many
// networks. This provider does not attempt to solve it; the wall is reported
// as an error so the caller moves on to the next provider.
package mojeek

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/icehunter/conduit/internal/websearch"
)

const (
	defaultEndpoint = "https://www.mojeek.com/search"
	defaultMax      = 10
	maxResults      = 20
)

// Provider is a keyless Mojeek search provider.
type Provider struct {
	httpClient *http.Client
	endpoint   string
}

// New returns a Mojeek provider.
func New() *Provider {
	return &Provider{httpClient: websearch.NewHTTPClient(), endpoint: defaultEndpoint}
}

func newWithClient(c *http.Client, endpoint string) *Provider {
	return &Provider{httpClient: c, endpoint: endpoint}
}

func (p *Provider) Name() string { return "Mojeek" }

// Search fetches the results page and parses the organic results.
func (p *Provider) Search(ctx context.Context, q websearch.Query) ([]websearch.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, websearch.DefaultTimeout)
	defer cancel()
	max := q.MaxResults
	if max <= 0 {
		max = defaultMax
	}
	u, err := url.Parse(p.endpoint)
	if err != nil {
		return nil, fmt.Errorf("mojeek: bad endpoint: %w", err)
	}
	vals := u.Query()
	vals.Set("q", q.Text)
	vals.Set("t", strconv.Itoa(min(max, maxResults)))
	u.RawQuery = vals.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("mojeek: build request: %w", err)
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", websearch.UserAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mojeek: http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := websearch.ReadBody(resp)
	if err != nil {
		return nil, fmt.Errorf("mojeek: read response: %w", err)
	}
	page := string(body)
	if isRobotWall(page) {
		return nil, fmt.Errorf("mojeek: blocked by automated-queries wall")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mojeek: status %d", resp.StatusCode)
	}

	results := websearch.DedupeURLs(parseResults(page))
	results = websearch.FilterDomains(results, q.AllowedDomains, q.BlockedDomains)
	return websearch.Limit(results, max), nil
}

var robotRe = regexp.MustCompile(`(?i)altcha-widget|captcha-wrap|sending automated queries`)

func isRobotWall(page string) bool {
	return robotRe.MatchString(page) && !strings.Contains(page, "results-standard")
}

var (
	itemRe    = regexp.MustCompile(`(?is)<li\b[^>]*>(.*?)</li>`)
	anchorRe  = regexp.MustCompile(`(?is)<a\b([^>]*\bclass="[^"]*\btitle\b[^"]*"[^>]*)>(.*?)</a>`)
	hrefRe    = regexp.MustCompile(`(?i)\bhref="([^"]+)"`)
	snippetRe = regexp.MustCompile(`(?is)<p\b[^>]*\bclass="[^"]*\bs\b[^"]*"[^>]*>(.*?)</p>`)
)

// parseResults reads the list items of the standard results list. Each organic
// row has an <a class="title"> linking straight to the target and a
// <p class="s"> preview; rows without a title anchor are skipped.
func parseResults(page string) []websearch.Result {
	start := strings.Index(page, "results-standard")
	if start < 0 {
		return nil
	}
	list := page[start:]
	if end := strings.Index(list, "</ul>"); end >= 0 {
		list = list[:end]
	}
	var out []websearch.Result
	for _, li := range itemRe.FindAllStringSubmatch(list, -1) {
		a := anchorRe.FindStringSubmatch(li[1])
		if a == nil {
			continue
		}
		h := hrefRe.FindStringSubmatch(a[1])
		if h == nil {
			continue
		}
		target := normalizeURL(strings.ReplaceAll(h[1], "&amp;", "&"))
		title := websearch.HTMLText(a[2])
		if target == "" || title == "" {
			continue
		}
		var snippet string
		if m := snippetRe.FindStringSubmatch(li[1]); m != nil {
			snippet = websearch.HTMLText(m[1])
		}
		out = append(out, websearch.Result{Title: title, URL: target, Snippet: snippet})
	}
	return out
}

// normalizeURL accepts only absolute http(s) links off mojeek's own domains.
func normalizeURL(href string) string {
	u, err := url.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	for _, d := range []string{"mojeek.com", "mojeek.co.uk", "mojeek.fr", "mojeek.de"} {
		if h == d || strings.HasSuffix(h, "."+d) {
			return ""
		}
	}
	return href
}
