package brave

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/websearch"
)

// braveFixture builds a Brave API JSON response body from the given entries.
func braveFixture(entries []struct{ Title, URL, Description string }) []byte {
	type item struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
	}
	type webBlock struct {
		Results []item `json:"results"`
	}
	type apiResp struct {
		Web webBlock `json:"web"`
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		items = append(items, item{Title: e.Title, URL: e.URL, Description: e.Description})
	}
	b, _ := json.Marshal(apiResp{Web: webBlock{Results: items}})
	return b
}

// mockServer starts an httptest.Server that returns the given body.
// It records the received X-Subscription-Token header for verification.
func mockServer(t *testing.T, body []byte, status int) (*httptest.Server, *string) {
	t.Helper()
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Subscription-Token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotToken
}

func providerForTest(t *testing.T, srv *httptest.Server, apiKey string) *Provider {
	t.Helper()
	mem := secure.NewMemoryStorage()
	if apiKey != "" {
		if err := mem.Set("conduit-credentials", "websearch.brave", []byte(apiKey)); err != nil {
			t.Fatalf("set credential: %v", err)
		}
	}
	return newWithClient(mem, srv.Client(), srv.URL)
}

func TestSearch_SuccessfulSearch(t *testing.T) {
	fixture := braveFixture([]struct{ Title, URL, Description string }{
		{"Go Blog", "https://go.dev/blog", "The official Go blog"},
		{"Go Playground", "https://play.golang.org", "Run Go code online"},
	})
	srv, gotToken := mockServer(t, fixture, http.StatusOK)

	p := providerForTest(t, srv, "my-api-key")
	results, err := p.Search(context.Background(), websearch.Query{Text: "go language"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if *gotToken != "my-api-key" {
		t.Errorf("token sent = %q, want %q", *gotToken, "my-api-key")
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].Title != "Go Blog" {
		t.Errorf("results[0].Title = %q, want %q", results[0].Title, "Go Blog")
	}
	if results[0].URL != "https://go.dev/blog" {
		t.Errorf("results[0].URL = %q", results[0].URL)
	}
	if results[0].Snippet != "The official Go blog" {
		t.Errorf("results[0].Snippet = %q", results[0].Snippet)
	}
}

func TestSearch_AllowedDomains(t *testing.T) {
	fixture := braveFixture([]struct{ Title, URL, Description string }{
		{"Go Blog", "https://go.dev/blog", "Official blog"},
		{"Random site", "https://example.com/go", "Not relevant"},
	})
	srv, _ := mockServer(t, fixture, http.StatusOK)
	p := providerForTest(t, srv, "key")

	results, err := p.Search(context.Background(), websearch.Query{
		Text:           "go",
		AllowedDomains: []string{"go.dev"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1 (only go.dev)", len(results))
	}
	if results[0].URL != "https://go.dev/blog" {
		t.Errorf("unexpected URL %q", results[0].URL)
	}
}

func TestSearch_BlockedDomains(t *testing.T) {
	fixture := braveFixture([]struct{ Title, URL, Description string }{
		{"Go Blog", "https://go.dev/blog", "Official blog"},
		{"Spam", "https://spam.com/go", "Unwanted"},
	})
	srv, _ := mockServer(t, fixture, http.StatusOK)
	p := providerForTest(t, srv, "key")

	results, err := p.Search(context.Background(), websearch.Query{
		Text:           "go",
		BlockedDomains: []string{"spam.com"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1 (spam blocked)", len(results))
	}
	if results[0].URL != "https://go.dev/blog" {
		t.Errorf("unexpected URL %q", results[0].URL)
	}
}

func TestSearch_MissingAPIKey(t *testing.T) {
	// Do not start a server — the key resolution must fail before any HTTP call.
	mem := secure.NewMemoryStorage() // empty — no key stored
	p := newWithClient(mem, http.DefaultClient, defaultAPIBase)

	t.Setenv("BRAVE_API_KEY", "") // ensure env is clear

	_, err := p.Search(context.Background(), websearch.Query{Text: "test"})
	if err == nil {
		t.Fatal("expected error for missing API key, got nil")
	}
	if !strings.HasPrefix(err.Error(), "brave: no API key configured") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "brave: no API key configured")
	}
}

func TestSearch_EmptyResults(t *testing.T) {
	fixture := braveFixture(nil) // empty results array
	srv, _ := mockServer(t, fixture, http.StatusOK)
	p := providerForTest(t, srv, "key")

	results, err := p.Search(context.Background(), websearch.Query{Text: "unlikely query"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestSearch_NonOKStatus(t *testing.T) {
	srv, _ := mockServer(t, []byte("forbidden"), http.StatusForbidden)
	p := providerForTest(t, srv, "key")

	_, err := p.Search(context.Background(), websearch.Query{Text: "test"})
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
}

func TestName(t *testing.T) {
	p := New(secure.NewMemoryStorage())
	if p.Name() != "Brave" {
		t.Errorf("Name() = %q, want %q", p.Name(), "Brave")
	}
}

func TestApplyDomainFilter(t *testing.T) {
	input := []websearch.Result{
		{URL: "https://allowed.com/page"},
		{URL: "https://allowed.com/blocked-path"},
		{URL: "https://other.com/page"},
		{URL: "https://spam.com/stuff"},
	}

	tests := []struct {
		name    string
		allowed []string
		blocked []string
		wantLen int
		wantURL string // first result URL, if wantLen > 0
	}{
		{
			name:    "no filter — all pass",
			allowed: nil, blocked: nil,
			wantLen: 4,
		},
		{
			name:    "allowed only — only allowed.com results",
			allowed: []string{"allowed.com"}, blocked: nil,
			wantLen: 2,
			wantURL: "https://allowed.com/page",
		},
		{
			name:    "blocked only — spam.com excluded",
			allowed: nil, blocked: []string{"spam.com"},
			wantLen: 3,
			wantURL: "https://allowed.com/page",
		},
		{
			name:    "both rules — allowed.com but not blocked-path",
			allowed: []string{"allowed.com"}, blocked: []string{"blocked-path"},
			wantLen: 1,
			wantURL: "https://allowed.com/page",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := applyDomainFilter(input, tc.allowed, tc.blocked)
			if len(got) != tc.wantLen {
				t.Errorf("len(got) = %d, want %d", len(got), tc.wantLen)
			}
			if tc.wantURL != "" && len(got) > 0 && got[0].URL != tc.wantURL {
				t.Errorf("got[0].URL = %q, want %q", got[0].URL, tc.wantURL)
			}
		})
	}
}

// FuzzParseResponse ensures the Brave API response parser never panics on
// arbitrary input. The parser processes external (untrusted) JSON.
func FuzzParseResponse(f *testing.F) {
	f.Add([]byte(`{"web":{"results":[{"title":"Go","url":"https://go.dev","description":"The Go language"}]}}`))
	f.Add([]byte(`{"web":{"results":[]}}`))
	f.Add([]byte(`{"web":{}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _ = parseResponse(data)
	})
}
