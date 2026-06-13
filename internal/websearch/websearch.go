// Package websearch defines the core types for multi-provider web search.
//
// A SearchProvider can be wired into websearchtool.NewWithProviders to add
// an external search backend. Providers are tried in order; the first that
// returns results wins. If all fail the tool falls back to the Anthropic-native
// web_search_20250305 path.
package websearch

import "context"

// Result is a single search hit returned by a provider.
type Result struct {
	Title   string
	URL     string
	Snippet string
	Score   float64
	// Markdown holds the full page content fetched via webfetchtool.Fetch.
	// May be empty if not fetched or if the fetch failed.
	Markdown string
}

// Query carries search parameters from the caller to a provider.
type Query struct {
	Text           string
	AllowedDomains []string
	BlockedDomains []string
	// MaxResults is a hint; 0 means use the provider's default.
	MaxResults int
}

// SearchProvider is implemented by any search backend that can be plugged
// into the WebSearch tool.
type SearchProvider interface {
	Name() string
	Search(ctx context.Context, q Query) ([]Result, error)
}
