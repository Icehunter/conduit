// Package parallel implements a keyless websearch.SearchProvider backed by
// Parallel's public MCP search endpoint (https://search.parallel.ai/mcp).
//
// The endpoint is Parallel's anonymous, rate-limited MCP server; it needs no
// credential and none is sent. The query is the only user data transmitted.
package parallel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/icehunter/conduit/internal/websearch"
)

const (
	defaultEndpoint = "https://search.parallel.ai/mcp"
	defaultMax      = 10
)

// Provider is a keyless Parallel MCP search provider.
type Provider struct {
	httpClient *http.Client
	endpoint   string
}

// New returns a Parallel provider.
func New() *Provider {
	return &Provider{httpClient: websearch.NewHTTPClient(), endpoint: defaultEndpoint}
}

func newWithClient(c *http.Client, endpoint string) *Provider {
	return &Provider{httpClient: c, endpoint: endpoint}
}

func (p *Provider) Name() string { return "Parallel" }

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	Result *struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Search calls the web_search tool over MCP streamable HTTP and normalizes the
// results. Domain rules are applied client-side.
func (p *Provider) Search(ctx context.Context, q websearch.Query) ([]websearch.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, websearch.DefaultTimeout)
	defer cancel()
	body, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: map[string]any{
			"name": "web_search",
			"arguments": map[string]any{
				"objective":      q.Text,
				"search_queries": []string{q.Text},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("parallel: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parallel: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", websearch.UserAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("parallel: http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := websearch.ReadBody(resp)
	if err != nil {
		return nil, fmt.Errorf("parallel: read response: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("parallel: public MCP rate limit reached (429)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("parallel: status %d", resp.StatusCode)
	}

	rpc, err := decodeRPC(ctx, resp.Header.Get("Content-Type"), raw)
	if err != nil {
		return nil, err
	}
	results, err := extractResults(rpc)
	if err != nil {
		return nil, err
	}
	max := q.MaxResults
	if max <= 0 {
		max = defaultMax
	}
	results = websearch.DedupeURLs(results)
	results = websearch.FilterDomains(results, q.AllowedDomains, q.BlockedDomains)
	return websearch.Limit(results, max), nil
}

// decodeRPC reads a JSON-RPC response sent either as plain JSON or as an SSE
// stream whose data events carry the JSON-RPC message.
func decodeRPC(ctx context.Context, contentType string, raw []byte) (*rpcResponse, error) {
	if !strings.Contains(contentType, "text/event-stream") {
		var r rpcResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("parallel: parse response: %w", err)
		}
		return &r, nil
	}
	var found *rpcResponse
	err := websearch.ScanSSE(ctx, bytes.NewReader(raw), func(data string) bool {
		var r rpcResponse
		if json.Unmarshal([]byte(data), &r) != nil {
			return true
		}
		if r.Result != nil || r.Error != nil {
			found = &r
			return false
		}
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("parallel: read event stream: %w", err)
	}
	if found == nil {
		return nil, fmt.Errorf("parallel: no result in event stream")
	}
	return found, nil
}

type payload struct {
	Results []struct {
		URL      string   `json:"url"`
		Title    string   `json:"title"`
		Excerpts []string `json:"excerpts"`
	} `json:"results"`
}

func extractResults(r *rpcResponse) ([]websearch.Result, error) {
	if r.Error != nil {
		return nil, fmt.Errorf("parallel: mcp error: %s", r.Error.Message)
	}
	if r.Result == nil {
		return nil, fmt.Errorf("parallel: unexpected response shape")
	}
	if r.Result.IsError {
		msg := "tool error"
		for _, c := range r.Result.Content {
			if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
				msg = strings.TrimSpace(c.Text)
				break
			}
		}
		return nil, fmt.Errorf("parallel: %s", msg)
	}
	var pl payload
	switch {
	case len(r.Result.StructuredContent) > 0 && string(r.Result.StructuredContent) != "null":
		if err := json.Unmarshal(r.Result.StructuredContent, &pl); err != nil {
			return nil, fmt.Errorf("parallel: parse structured content: %w", err)
		}
	default:
		parsed := false
		for _, c := range r.Result.Content {
			if c.Type == "text" && json.Unmarshal([]byte(c.Text), &pl) == nil {
				parsed = true
				break
			}
		}
		if !parsed {
			return nil, nil
		}
	}
	out := make([]websearch.Result, 0, len(pl.Results))
	for _, it := range pl.Results {
		if !strings.HasPrefix(it.URL, "http://") && !strings.HasPrefix(it.URL, "https://") {
			continue
		}
		title := it.Title
		if title == "" {
			title = it.URL
		}
		out = append(out, websearch.Result{
			Title:   title,
			URL:     it.URL,
			Snippet: truncate(strings.TrimSpace(strings.Join(it.Excerpts, "\n\n"))),
		})
	}
	return out, nil
}

// maxSnippet caps one result's excerpts; Parallel returns long page extracts.
const maxSnippet = 1500

func truncate(s string) string {
	if len(s) <= maxSnippet {
		return s
	}
	return strings.ToValidUTF8(s[:maxSnippet], "") + "..."
}
