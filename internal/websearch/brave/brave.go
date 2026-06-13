// Package brave implements a websearch.SearchProvider backed by the Brave
// Search API (https://api.search.brave.com/res/v1/web/search).
//
// Credential lookup priority:
//  1. secure.Storage.Get("conduit-credentials", "websearch.brave")
//  2. BRAVE_API_KEY environment variable
//  3. Error if neither is set
package brave

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/websearch"
)

const (
	defaultAPIBase = "https://api.search.brave.com/res/v1/web/search"
	credKey        = "websearch.brave"
	credSvc        = "conduit-credentials"
	defaultMax     = 10
)

// Provider is a websearch.SearchProvider backed by Brave Search.
type Provider struct {
	storage    secure.Storage
	httpClient *http.Client
	// apiBase allows tests to override the endpoint URL.
	apiBase string
}

// New returns a Brave Search provider that resolves its API key from storage
// or from the BRAVE_API_KEY environment variable.
func New(storage secure.Storage) *Provider {
	return &Provider{
		storage:    storage,
		httpClient: http.DefaultClient,
		apiBase:    defaultAPIBase,
	}
}

// newWithClient is used in tests to inject a custom HTTP client and base URL.
func newWithClient(storage secure.Storage, c *http.Client, base string) *Provider {
	return &Provider{storage: storage, httpClient: c, apiBase: base}
}

func (p *Provider) Name() string { return "Brave" }

// Search executes a web search against the Brave API and returns results.
// Domain filtering is applied client-side after the response is parsed.
func (p *Provider) Search(ctx context.Context, q websearch.Query) ([]websearch.Result, error) {
	key, err := p.resolveKey()
	if err != nil {
		return nil, err
	}

	count := q.MaxResults
	if count <= 0 {
		count = defaultMax
	}

	reqURL := fmt.Sprintf("%s?q=%s&count=%d", p.apiBase, url.QueryEscape(q.Text), count)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", key)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave: api returned status %d", resp.StatusCode)
	}

	body, err := readBody(resp)
	if err != nil {
		return nil, fmt.Errorf("brave: read response: %w", err)
	}

	results, err := parseResponse(body)
	if err != nil {
		return nil, fmt.Errorf("brave: parse response: %w", err)
	}

	return applyDomainFilter(results, q.AllowedDomains, q.BlockedDomains), nil
}

// resolveKey returns the API key from storage or the environment.
func (p *Provider) resolveKey() (string, error) {
	if p.storage != nil {
		if raw, err := p.storage.Get(credSvc, credKey); err == nil && len(raw) > 0 {
			return strings.TrimSpace(string(raw)), nil
		}
	}
	if v := os.Getenv("BRAVE_API_KEY"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("brave: no API key configured (set websearch.brave credential or BRAVE_API_KEY env)")
}

// readBody decompresses gzip-encoded responses transparently.
func readBody(resp *http.Response) ([]byte, error) {
	var r io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	return io.ReadAll(r)
}

// braveResponse is the subset of the Brave API response we care about.
type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}

func parseResponse(body []byte) ([]websearch.Result, error) {
	var resp braveResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	results := make([]websearch.Result, 0, len(resp.Web.Results))
	for _, r := range resp.Web.Results {
		results = append(results, websearch.Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
			// Score and Markdown are not populated by default.
		})
	}
	return results, nil
}

// applyDomainFilter returns results that pass the allowed/blocked domain rules.
// AllowedDomains: include only results whose URL contains any allowed domain.
// BlockedDomains: exclude results whose URL contains any blocked domain.
func applyDomainFilter(results []websearch.Result, allowed, blocked []string) []websearch.Result {
	if len(allowed) == 0 && len(blocked) == 0 {
		return results
	}

	filtered := make([]websearch.Result, 0, len(results))
	for _, r := range results {
		if len(allowed) > 0 && !containsAny(r.URL, allowed) {
			continue
		}
		if len(blocked) > 0 && containsAny(r.URL, blocked) {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, substrings []string) bool {
	for _, sub := range substrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
