package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	provcodex "github.com/icehunter/conduit/internal/provider/codex"
	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/websearch"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func credentialed(t *testing.T) secure.Storage {
	t.Helper()
	store := secure.NewMemoryStorage()
	err := settings.SaveStructuredProviderCredential(store, provcodex.ProviderID, &providerauth.ProviderCredential{
		AccessToken: "tok-123",
		Expiry:      time.Now().Add(time.Hour),
		Metadata:    map[string]string{"account_id": "acct-9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func serve(t *testing.T, store secure.Storage, status int, body []byte) (*Provider, *http.Request, *map[string]any) {
	t.Helper()
	var got http.Request
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	p := New(store)
	p.httpClient = srv.Client()
	p.endpoint = srv.URL
	return p, &got, &sent
}

func TestSearch_ParsesStreamAndSendsHostedTool(t *testing.T) {
	p, req, sent := serve(t, credentialed(t), 200, fixture(t, "search.sse"))
	res, err := p.Search(context.Background(), websearch.Query{Text: "go 1.26"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer tok-123" || req.Header.Get("ChatGPT-Account-Id") != "acct-9" {
		t.Errorf("auth headers = %v", req.Header)
	}
	tools, _ := (*sent)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "web_search" {
		t.Errorf("tools = %v", (*sent)["tools"])
	}
	if (*sent)["store"] != false || (*sent)["stream"] != true || (*sent)["model"] != DefaultModel {
		t.Errorf("body = %v", *sent)
	}

	if len(res) != 3 {
		t.Fatalf("got %+v, want 3 unique sources", res)
	}
	// utm_source=openai stripped and merged with the cited duplicate; the
	// annotation title replaces nothing because the action title was set.
	if res[0].URL != "https://go.dev/blog" || res[0].Title != "Go Blog" {
		t.Errorf("res[0] = %+v", res[0])
	}
	if res[0].Snippet == "" {
		t.Error("citation snippet should fill the merged source")
	}
	if !strings.HasPrefix(res[0].Markdown, "Search summary:\nGo 1.26 is out.") {
		t.Errorf("answer = %q", res[0].Markdown)
	}
	if res[1].URL != "https://example.com/only-in-action" || res[1].Title != res[1].URL {
		t.Errorf("res[1] = %+v", res[1])
	}
	if res[2].URL != "https://news.example.org/go" {
		t.Errorf("res[2] = %+v", res[2])
	}
}

func TestSearch_DomainFilter(t *testing.T) {
	p, _, _ := serve(t, credentialed(t), 200, fixture(t, "search.sse"))
	res, err := p.Search(context.Background(), websearch.Query{Text: "x", AllowedDomains: []string{"news.example.org"}})
	if err != nil || len(res) != 1 || res[0].URL != "https://news.example.org/go" {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestSearch_NoCredentialMakesNoRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	p := New(secure.NewMemoryStorage())
	p.httpClient, p.endpoint = srv.Client(), srv.URL
	if p.Credentialed() {
		t.Error("Credentialed() = true with empty store")
	}
	if _, err := p.Search(context.Background(), websearch.Query{Text: "x"}); err == nil {
		t.Error("expected error without credential")
	}
	if called {
		t.Error("no request may be made without a credential")
	}
}

func TestSearch_Errors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
		want   string
	}{
		{"http error", 401, []byte("nope\n  bad\x01token"), "status 401: nope bad token"},
		{"no web_search call", 200, fixture(t, "no_search.sse"), "did not run web search"},
		{"response.failed", 200, fixture(t, "failed.sse"), "usage limit reached"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := serve(t, credentialed(t), tt.status, tt.body)
			_, err := p.Search(context.Background(), websearch.Query{Text: "x"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCleanURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://a.com/x?utm_source=openai", "https://a.com/x"},
		{"https://a.com/x?q=1&utm_source=openai", "https://a.com/x?q=1"},
		{"https://a.com/x?utm_source=other", "https://a.com/x?utm_source=other"},
		{"https://a.com/x", "https://a.com/x"},
	}
	for _, tt := range tests {
		if got := cleanURL(tt.in); got != tt.want {
			t.Errorf("cleanURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCitationSnippetClamps(t *testing.T) {
	one, huge := 1, 10_000
	if got := citationSnippet("héllo wörld", &one, &huge); got == "" {
		t.Error("expected snippet")
	}
	bad := -50
	if got := citationSnippet("abc", &huge, &bad); got != "" {
		t.Errorf("inverted/out-of-range indices should yield empty, got %q", got)
	}
	if citationSnippet("abc", nil, &one) != "" {
		t.Error("nil index must yield empty")
	}
}

func TestSearch_RedirectNotFollowed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(target.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)
	p := New(credentialed(t))
	p.endpoint = origin.URL
	_, err := p.Search(context.Background(), websearch.Query{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "status 307") {
		t.Errorf("err = %v, want status 307", err)
	}
	if hits.Load() != 0 {
		t.Error("redirect target must not receive the credential-bearing request")
	}
}

func TestSearch_ModelSelection(t *testing.T) {
	tests := []struct {
		name string
		fn   func() string
		want string
	}{
		{"default", nil, DefaultModel},
		{"user model", func() string { return "gpt-5.5" }, "gpt-5.5"},
		{"empty falls back", func() string { return " " }, DefaultModel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, sent := serve(t, credentialed(t), 200, fixture(t, "search.sse"))
			p.WithModel(tt.fn)
			if _, err := p.Search(context.Background(), websearch.Query{Text: "x"}); err != nil {
				t.Fatal(err)
			}
			if (*sent)["model"] != tt.want {
				t.Errorf("model = %v, want %v", (*sent)["model"], tt.want)
			}
		})
	}
}

func TestSearch_TimeoutCoversBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // body never finishes
	}))
	t.Cleanup(srv.Close)
	p := New(credentialed(t))
	p.endpoint = srv.URL
	p.timeout = 50 * time.Millisecond
	if _, err := p.Search(context.Background(), websearch.Query{Text: "x"}); err == nil {
		t.Error("expected deadline error for stalled body")
	}
}

func TestAvailable(t *testing.T) {
	if New(secure.NewMemoryStorage()).Available() || !New(credentialed(t)).Available() {
		t.Error("Available must follow stored credential")
	}
}
