// Package codex implements a websearch.SearchProvider that runs the hosted
// web_search tool of the ChatGPT Responses backend using the user's
// ChatGPT/Codex subscription credential.
//
// The credential is read from the same store the Codex provider uses and is
// sent only to the official Codex endpoint (codex.CodexBaseURL). With no
// credential stored the provider fails fast with no network call, so it can
// sit in the provider chain unconditionally.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/provider/codex"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/websearch"
)

const (
	// DefaultModel is a small, fast model from the Codex fallback list.
	DefaultModel = "gpt-5.4-mini"

	// searchTimeout bounds the whole exchange, SSE body included. Hosted search
	// streams for a while, so it gets more than the keyless 20s.
	searchTimeout = 60 * time.Second
	// maxStreamBytes caps the SSE bytes read for one search.
	maxStreamBytes = 8 << 20

	instructions = "You are a helpful assistant with web search capabilities. " +
		"Search the web to answer the user's question accurately and cite your sources."
	defaultMax = 10
)

// Provider is a Codex-subscription web search provider.
type Provider struct {
	store      secure.Storage
	credential string
	model      string
	// modelFn, when set and non-empty, supplies the user's selected Codex
	// model; otherwise model is used.
	modelFn    func() string
	timeout    time.Duration
	httpClient *http.Client
	// endpoint is the Responses URL; tests override it.
	endpoint string
}

// New returns a Codex search provider using the default Codex credential.
func New(store secure.Storage) *Provider {
	return NewForCredential(store, codex.ProviderID)
}

// NewForCredential uses a named Codex credential (an account alias).
func NewForCredential(store secure.Storage, credential string) *Provider {
	return &Provider{
		store:      store,
		credential: credential,
		model:      DefaultModel,
		timeout:    searchTimeout,
		httpClient: newHTTPClient(),
		endpoint:   strings.TrimRight(codex.CodexBaseURL, "/") + "/responses",
	}
}

// newHTTPClient returns a client that never follows redirects: the bearer
// token and account id must go only to the official endpoint, and a redirect
// is surfaced as a failed status. There is no client-level timeout; Search
// bounds the exchange with a context deadline.
func newHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// WithModel makes the provider prefer the model fn returns (the user's
// selected Codex model), falling back to DefaultModel when it returns "".
func (p *Provider) WithModel(fn func() string) *Provider {
	p.modelFn = fn
	return p
}

func (p *Provider) Name() string { return "Codex" }

// Available implements websearch.Availabler.
func (p *Provider) Available() bool { return p.Credentialed() }

// Credentialed reports whether a Codex credential is stored. It makes no
// network call.
func (p *Provider) Credentialed() bool {
	if p.store == nil {
		return false
	}
	cred, err := settings.LoadStructuredProviderCredential(p.store, p.credential)
	return err == nil && (cred.AccessToken != "" || cred.RefreshToken != "")
}

// Search runs the hosted web_search tool and returns the cited sources. The
// model's summary is attached to the first result's Markdown field.
func (p *Provider) Search(ctx context.Context, q websearch.Query) ([]websearch.Result, error) {
	if !p.Credentialed() {
		return nil, fmt.Errorf("codex: no ChatGPT/Codex credential configured")
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	cred, err := codex.NewAuthorizerForCredential(p.store, p.credential).EnsureFresh(ctx)
	if err != nil {
		return nil, fmt.Errorf("codex: credential: %w", err)
	}
	model := p.model
	if p.modelFn != nil {
		if m := strings.TrimSpace(p.modelFn()); m != "" {
			model = m
		}
	}
	body, err := json.Marshal(map[string]any{
		"model":               model,
		"stream":              true,
		"store":               false,
		"include":             []string{"web_search_call.action.sources"},
		"parallel_tool_calls": true,
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": q.Text}},
		}},
		"tools":        []any{map[string]any{"type": "web_search", "search_context_size": "high"}},
		"tool_choice":  map[string]any{"type": "web_search"},
		"instructions": instructions,
	})
	if err != nil {
		return nil, fmt.Errorf("codex: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("codex: build request: %w", err)
	}
	for k, v := range codex.Headers(cred.Metadata["account_id"], "", "websearch") {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if msg := websearch.SanitizeSnippet(detail, 300); msg != "" {
			return nil, fmt.Errorf("codex: status %d: %s", resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("codex: status %d", resp.StatusCode)
	}

	answer, sources, err := readStream(ctx, resp)
	if err != nil {
		return nil, err
	}
	results := websearch.FilterDomains(sources, q.AllowedDomains, q.BlockedDomains)
	max := q.MaxResults
	if max <= 0 {
		max = defaultMax
	}
	results = websearch.Limit(results, max)
	if answer != "" && len(results) > 0 {
		results[0].Markdown = "Search summary:\n" + answer
	}
	return results, nil
}

type source struct {
	URL              string `json:"url"`
	SourceWebsiteURL string `json:"source_website_url"`
	Title            string `json:"title"`
	Caption          string `json:"caption"`
}

type annotation struct {
	Type       string `json:"type"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	StartIndex *int   `json:"start_index"`
	EndIndex   *int   `json:"end_index"`
}

type outputItem struct {
	Type   string `json:"type"`
	Action *struct {
		Sources []source `json:"sources"`
	} `json:"action"`
	Sources []source `json:"sources"`
	Results []source `json:"results"`
	Content []struct {
		Type        string       `json:"type"`
		Text        string       `json:"text"`
		Annotations []annotation `json:"annotations"`
	} `json:"content"`
}

type event struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Item     *outputItem     `json:"item"`
	Error    json.RawMessage `json:"error"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
	Response *struct {
		Error json.RawMessage `json:"error"`
	} `json:"response"`
}

// readStream folds the Responses SSE stream into an answer and source list.
// A stream that never invoked web_search is rejected: its text comes from the
// model's own knowledge, not from a search.
func readStream(ctx context.Context, resp *http.Response) (string, []websearch.Result, error) {
	var (
		answerParts []string
		streamed    strings.Builder
		sources     []websearch.Result
		byURL       = map[string]int{}
		searched    bool
		streamErr   error
	)
	add := func(r websearch.Result) {
		r.URL = cleanURL(r.URL)
		if i, ok := byURL[r.URL]; ok {
			if sources[i].Title == sources[i].URL && r.Title != r.URL {
				sources[i].Title = r.Title
			}
			if sources[i].Snippet == "" {
				sources[i].Snippet = r.Snippet
			}
			return
		}
		byURL[r.URL] = len(sources)
		sources = append(sources, r)
	}
	err := websearch.ScanSSE(ctx, io.LimitReader(resp.Body, maxStreamBytes), func(data string) bool {
		var ev event
		if json.Unmarshal([]byte(data), &ev) != nil || ev.Type == "" {
			return true
		}
		if strings.HasPrefix(ev.Type, "response.web_search_call") {
			searched = true
		}
		switch ev.Type {
		case "response.output_text.delta":
			streamed.WriteString(ev.Delta)
		case "response.output_item.done":
			if ev.Item == nil {
				return true
			}
			switch ev.Item.Type {
			case "web_search_call":
				searched = true
				groups := [][]source{ev.Item.Sources, ev.Item.Results}
				if ev.Item.Action != nil {
					groups = append(groups, ev.Item.Action.Sources)
				}
				for _, g := range groups {
					for _, s := range g {
						u := s.URL
						if u == "" {
							u = s.SourceWebsiteURL
						}
						if !isHTTP(u) {
							continue
						}
						title := s.Title
						if title == "" {
							title = s.Caption
						}
						if title == "" {
							title = u
						}
						add(websearch.Result{Title: title, URL: u})
					}
				}
			case "message":
				for _, part := range ev.Item.Content {
					if part.Type != "output_text" || part.Text == "" {
						continue
					}
					answerParts = append(answerParts, part.Text)
					for _, a := range part.Annotations {
						if a.Type != "url_citation" || !isHTTP(a.URL) {
							continue
						}
						title := a.Title
						if title == "" {
							title = a.URL
						}
						add(websearch.Result{Title: title, URL: a.URL, Snippet: citationSnippet(part.Text, a.StartIndex, a.EndIndex)})
					}
				}
			}
		case "error", "response.failed":
			streamErr = fmt.Errorf("codex: %s", errorMessage(ev))
			return false
		}
		return true
	})
	if err != nil {
		return "", nil, fmt.Errorf("codex: read event stream: %w", err)
	}
	if streamErr != nil {
		return "", nil, streamErr
	}
	if !searched {
		return "", nil, fmt.Errorf("codex: completion did not run web search")
	}
	answer := strings.TrimSpace(strings.Join(answerParts, "\n\n"))
	if answer == "" {
		answer = strings.TrimSpace(streamed.String())
	}
	if len(sources) == 0 {
		return "", nil, nil
	}
	return answer, sources, nil
}

func isHTTP(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

// cleanURL drops the utm_source=openai tracking parameter the backend appends.
func cleanURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Get("utm_source") != "openai" {
		return raw
	}
	q.Del("utm_source")
	u.RawQuery = q.Encode()
	return u.String()
}

// citationSnippet returns the text around a citation, capped at 300 bytes.
// Indices come from the backend and are clamped to the text.
func citationSnippet(text string, start, end *int) string {
	if start == nil || end == nil {
		return ""
	}
	b := min(max(*start-100, 0), len(text))
	a := min(max(*end+100, b), len(text))
	s := strings.TrimSpace(strings.ToValidUTF8(text[b:a], ""))
	if len(s) > 300 {
		s = s[:297] + "..."
	}
	return s
}

func errorMessage(ev event) string {
	var cands [][]byte
	cands = append(cands, []byte(ev.Error))
	if ev.Response != nil {
		cands = append(cands, []byte(ev.Response.Error))
	}
	if ev.Message != "" {
		return ev.Message
	}
	for _, c := range cands {
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(c, &e) == nil && e.Message != "" {
			return e.Message
		}
	}
	if ev.Code != "" {
		return ev.Code
	}
	return "request failed"
}
