// Package websearchtool implements the WebSearch tool.
//
// Unlike most tools that call external APIs directly, WebSearch delegates to
// Anthropic's native web_search_20250305 server-side tool. It makes a
// secondary streaming API call with only the native search tool declared, then
// assembles the results (text, citations, search hits) into a single text
// block returned to the main agent.
//
// Provider order: providers passed to NewWithProviders (key-based), then the
// Anthropic-native path when the client speaks the Anthropic Messages API,
// then the WithFallbackProviders chain (Codex subscription), then the
// WithKeylessProviders tier. The first that returns a non-empty result set
// wins. Providers that report themselves unavailable (no key or credential)
// are skipped. The keyless tier is gated by webSearch.keyless.
//
// Mirrors src/tools/WebSearchTool/WebSearchTool.ts.
// max_uses=8 is hardcoded to match the TS implementation.
package websearchtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/truncate"
	"github.com/icehunter/conduit/internal/websearch"
)

// searchModel is the small fast model used for web search sub-calls.
// Mirrors getSmallFastModel() in the TS source.
const searchModel = "claude-haiku-4-5-20251001"

// maxSearchUses is the maximum number of searches per tool call.
const maxSearchUses = 8

// Tool implements the WebSearch tool.
type Tool struct {
	tool.NotDeferrable
	client    *api.Client
	providers []websearch.SearchProvider
	// fallbacks run after the Anthropic-native path (when the client can use
	// it): subscription search first, then keyless providers.
	fallbacks []websearch.SearchProvider
	// keyless run last and send the query to third parties; keylessModeFn
	// returns the webSearch.keyless setting ("auto" when nil).
	keyless       []websearch.SearchProvider
	keylessModeFn func() string
}

// New returns a WebSearch tool backed by the given API client.
// It uses only the Anthropic-native path — equivalent to NewWithProviders
// called with no providers.
func New(client *api.Client) *Tool {
	return &Tool{client: client}
}

// NewWithProviders returns a WebSearch tool that tries each provider in order
// before falling back to the Anthropic-native path. If no providers are
// configured or all fail, the Anthropic native search is used.
func NewWithProviders(client *api.Client, providers ...websearch.SearchProvider) *Tool {
	return &Tool{client: client, providers: providers}
}

// WithFallbackProviders sets providers tried after the configured providers
// and, for clients that speak the Anthropic Messages API, after the native
// web_search path (the Codex subscription). Order is preserved. It returns t for chaining.
func (t *Tool) WithFallbackProviders(providers ...websearch.SearchProvider) *Tool {
	t.fallbacks = providers
	return t
}

// WithKeylessProviders sets the keyless tier and a function returning the
// webSearch.keyless mode ("auto", "always" or "never"; read per search so
// settings changes apply live). In "auto" the tier runs only when no
// configured provider was usable; "always" also runs it after a failure.
func (t *Tool) WithKeylessProviders(mode func() string, providers ...websearch.SearchProvider) *Tool {
	t.keyless, t.keylessModeFn = providers, mode
	return t
}

func (*Tool) Name() string { return "WebSearch" }

func (*Tool) Description() string {
	return "Search the web for current information. " +
		"Returns text summaries and source URLs. " +
		"Use for questions that require up-to-date information beyond your training data."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "The search query to use",
				"minLength": 2
			},
			"allowed_domains": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Only include results from these domains"
			},
			"blocked_domains": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Never include results from these domains"
			}
		},
		"required": ["query"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return true }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Input is the typed view of the JSON input.
type Input struct {
	Query          string   `json:"query"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

// Execute runs a web search. It tries each configured provider in order,
// returning the first successful result. If all providers fail or none are
// configured, it falls back to Anthropic's native search tool.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Query) == "" {
		return tool.ErrorResult("`query` is required"), nil
	}

	query := websearch.Query{
		Text:           in.Query,
		AllowedDomains: in.AllowedDomains,
		BlockedDomains: in.BlockedDomains,
		MaxResults:     10,
	}
	var failures []string
	// attempted is true once any configured (non-keyless) provider was usable
	// and tried. In "auto" keyless mode that closes the keyless tier: a
	// configured provider that fails is an error to report, not a reason to
	// send the query to third parties.
	attempted := false

	// 1. Configured (key-based) providers.
	if res, ok := t.tryProviders(ctx, t.providers, query, &failures, &attempted); ok {
		return res, nil
	}

	// 2. Anthropic-native search, only for clients that can run it.
	var nativeRes *tool.Result
	if ctx.Err() == nil && t.client.SpeaksAnthropicMessages() {
		attempted = true
		res, err := t.anthropicSearch(ctx, in)
		if err != nil || !res.IsError {
			return res, err
		}
		nativeRes = &res
		failures = append(failures, "Anthropic: "+resultText(res))
	}

	// 3. Codex subscription search.
	if res, ok := t.tryProviders(ctx, t.fallbacks, query, &failures, &attempted); ok {
		return res, nil
	}

	// 4. Keyless tier, per webSearch.keyless.
	mode := t.keylessMode()
	useKeyless := mode == settings.WebSearchKeylessAlways || (mode == settings.WebSearchKeylessAuto && !attempted)
	if useKeyless {
		var unused bool
		if res, ok := t.tryProviders(ctx, t.keyless, query, &failures, &unused); ok {
			return res, nil
		}
	}

	if ctx.Err() != nil {
		return tool.ErrorResult("search cancelled"), nil
	}
	if nativeRes != nil && len(failures) == 1 {
		return *nativeRes, nil
	}
	if len(failures) == 0 {
		if mode == settings.WebSearchKeylessNever {
			return tool.ErrorResult("no web search provider is available and keyless search is disabled (webSearch.keyless=never): configure a search API key or sign in to a provider with web search"), nil
		}
		return tool.ErrorResult("no web search provider is available: configure a search API key or sign in to a provider with web search"), nil
	}
	msg := "web search failed: " + strings.Join(failures, "; ")
	if !useKeyless && len(t.keyless) > 0 && mode == settings.WebSearchKeylessAuto {
		msg += " (keyless fallback not used: webSearch.keyless is \"auto\"; set it to \"always\" to allow it)"
	}
	return tool.ErrorResult(msg), nil
}

// keylessMode returns the normalized webSearch.keyless mode; "auto" by default.
func (t *Tool) keylessMode() string {
	if t.keylessModeFn != nil {
		switch m := strings.ToLower(strings.TrimSpace(t.keylessModeFn())); m {
		case settings.WebSearchKeylessAlways, settings.WebSearchKeylessNever:
			return m
		}
	}
	return settings.WebSearchKeylessAuto
}

// resultText joins a result's text blocks into one trimmed line for error
// messages.
func resultText(r tool.Result) string {
	var parts []string
	for _, b := range r.Content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	text := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	if len(text) > 400 {
		text = strings.ToValidUTF8(text[:400], "") + "..."
	}
	return text
}

// tryProviders runs the available providers in order and formats the first
// non-empty result set. Unavailable providers are skipped without being
// recorded; failures and empty sets are appended to failures and set
// *attempted.
func (t *Tool) tryProviders(ctx context.Context, providers []websearch.SearchProvider, q websearch.Query, failures *[]string, attempted *bool) (tool.Result, bool) {
	for _, p := range providers {
		if ctx.Err() != nil {
			return tool.Result{}, false
		}
		if !websearch.Available(p) {
			continue
		}
		*attempted = true
		results, err := p.Search(ctx, q)
		switch {
		case err != nil:
			*failures = append(*failures, fmt.Sprintf("%s: %v", p.Name(), err))
		case len(results) == 0:
			*failures = append(*failures, p.Name()+": no results")
		default:
			return t.formatProviderResults(q.Text, p.Name(), results), true
		}
	}
	return tool.Result{}, false
}

// formatProviderResults formats a slice of search results into a tool.Result.
// Results are deduplicated by URL before formatting.
func (t *Tool) formatProviderResults(query, providerName string, results []websearch.Result) tool.Result {
	// Deduplicate by URL.
	seen := make(map[string]bool, len(results))
	deduped := make([]websearch.Result, 0, len(results))
	for _, r := range results {
		if seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		deduped = append(deduped, r)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Web search results for: %q (via %s)\n\n", query, providerName)
	for _, r := range deduped {
		fmt.Fprintf(&sb, "## %s\n%s\n%s\n\n", r.Title, r.URL, r.Snippet)
		if r.Markdown != "" {
			sb.WriteString("---\n")
			sb.WriteString(r.Markdown)
			sb.WriteString("\n\n")
		}
	}

	content := strings.TrimSpace(sb.String())
	maxLines, maxBytes := truncate.Limits()
	tr, _ := truncate.Apply(content, truncate.Options{
		MaxLines:  maxLines,
		MaxBytes:  maxBytes,
		Direction: "head",
		HasTask:   false,
	})
	return tool.TextResult(tr.Content)
}

// anthropicSearch performs a web search via Anthropic's native search tool.
func (t *Tool) anthropicSearch(ctx context.Context, in Input) (tool.Result, error) {
	if t.client == nil {
		return tool.ErrorResult("no API client configured for Anthropic-native web search"), nil
	}

	// Build the native web_search_20250305 tool definition.
	searchToolExtra := map[string]any{
		"max_uses": maxSearchUses,
	}
	if len(in.AllowedDomains) > 0 {
		searchToolExtra["allowed_domains"] = in.AllowedDomains
	}
	if len(in.BlockedDomains) > 0 {
		searchToolExtra["blocked_domains"] = in.BlockedDomains
	}

	nativeSearchTool := api.ToolDef{
		Type:  "web_search_20250305",
		Name:  "web_search",
		Extra: searchToolExtra,
	}

	// Build the search request. We ask the model to answer the query using
	// the web search tool; it will perform searches and return text + citations.
	req := &api.MessageRequest{
		Model:     searchModel,
		MaxTokens: 4096,
		Messages: []api.Message{
			{
				Role: "user",
				Content: []api.ContentBlock{{
					Type: "text",
					Text: fmt.Sprintf("Search the web and answer: %s\n\nInclude URLs/sources for all facts.", in.Query),
				}},
			},
		},
		Stream: true,
		Tools:  []api.ToolDef{nativeSearchTool},
		Hints:  &api.WireHints{RequestClass: "auxiliary"},
	}

	stream, err := t.client.StreamMessage(ctx, req)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("search request failed: %v", err)), nil
	}
	defer func() { _ = stream.Close() }()

	result, err := drainSearchStream(ctx, stream, in.Query)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return tool.ErrorResult("search cancelled"), nil
		}
		return tool.ErrorResult(fmt.Sprintf("search failed: %v", err)), nil
	}

	// Apply truncate-to-disk for large search results.
	maxLines, maxBytes := truncate.Limits()
	tr, _ := truncate.Apply(result, truncate.Options{
		MaxLines:  maxLines,
		MaxBytes:  maxBytes,
		Direction: "head",
		HasTask:   false,
	})
	return tool.TextResult(tr.Content), nil
}

// drainSearchStream reads all events from the search stream and assembles
// the text response. The model interleaves server_tool_use, web_search_tool_result,
// text, and citation blocks; we collect all text and format source URLs.
func drainSearchStream(ctx context.Context, stream *api.Stream, query string) (string, error) {
	// Track accumulated text per block index.
	type blockInfo struct {
		blockType string
		text      strings.Builder
	}
	blocks := map[int]*blockInfo{}

	var orderedIndexes []int

	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}

		switch ev.Type {
		case "content_block_start":
			cbs, err := ev.AsContentBlockStart()
			if err != nil {
				continue
			}
			var raw struct {
				Type string `json:"type"`
			}
			if e := json.Unmarshal(cbs.ContentBlock, &raw); e == nil {
				bi := &blockInfo{blockType: raw.Type}
				blocks[cbs.Index] = bi
				orderedIndexes = append(orderedIndexes, cbs.Index)
			}

		case "content_block_delta":
			cbd, err := ev.AsContentBlockDelta()
			if err != nil {
				continue
			}
			bi, ok := blocks[cbd.Index]
			if !ok {
				continue
			}
			switch cbd.Delta.Type {
			case "text_delta":
				bi.text.WriteString(cbd.Delta.Text)
			case "input_json_delta":
				bi.text.WriteString(cbd.Delta.PartialJSON)
			}
		}
	}

	// Assemble output: collect text blocks; skip server_tool_use and web_search_tool_result.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Web search results for: %q\n\n", query)

	seenIdx := make(map[int]bool)
	for _, idx := range orderedIndexes {
		if seenIdx[idx] {
			continue
		}
		seenIdx[idx] = true
		bi := blocks[idx]
		if bi == nil {
			continue
		}
		if bi.blockType == "text" && bi.text.Len() > 0 {
			sb.WriteString(bi.text.String())
			sb.WriteByte('\n')
		}
	}

	result := strings.TrimSpace(sb.String())
	if result == fmt.Sprintf("Web search results for: %q", query) {
		return fmt.Sprintf("Web search for %q returned no text results.", query), nil
	}
	return result, nil
}
