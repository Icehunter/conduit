package parallel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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

func serve(t *testing.T, status int, contentType string, body []byte) (*Provider, *http.Request, *[]byte) {
	t.Helper()
	var got http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return newWithClient(srv.Client(), srv.URL), &got, &gotBody
}

func TestSearch_StructuredContent(t *testing.T) {
	p, req, body := serve(t, 200, "application/json", fixture(t, "structured.json"))
	res, err := p.Search(context.Background(), websearch.Query{Text: "go blog"})
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Method string `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Method != "tools/call" || sent.Params.Name != "web_search" || sent.Params.Arguments["objective"] != "go blog" {
		t.Errorf("request body = %s", *body)
	}
	if !strings.Contains(req.Header.Get("Accept"), "text/event-stream") || req.Header.Get("User-Agent") != websearch.UserAgent {
		t.Errorf("headers = %v", req.Header)
	}
	if req.Header.Get("Authorization") != "" || req.Header.Get("X-Api-Key") != "" {
		t.Error("keyless provider must not send credentials")
	}
	if len(res) != 2 {
		t.Fatalf("got %+v, want 2 (bad scheme and dup dropped)", res)
	}
	if res[0].Snippet != "First excerpt.\n\nSecond excerpt." || res[0].Title != "Go Blog" {
		t.Errorf("res[0] = %+v", res[0])
	}
	if res[1].Title != "https://example.com/x" {
		t.Errorf("empty title should fall back to URL, got %q", res[1].Title)
	}
}

func TestSearch_SSETextContent(t *testing.T) {
	p, _, _ := serve(t, 200, "text/event-stream", fixture(t, "text_content.sse"))
	res, err := p.Search(context.Background(), websearch.Query{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].URL != "https://pkg.go.dev" || res[0].Snippet != "Packages" {
		t.Errorf("res = %+v", res)
	}
}

func TestSearch_Errors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		ct     string
		body   []byte
		want   string
	}{
		{"rate limited", 429, "application/json", []byte("{}"), "rate limit"},
		{"http error", 500, "application/json", []byte("x"), "status 500"},
		{"tool error", 200, "application/json", fixture(t, "tool_error.json"), "quota exceeded"},
		{"rpc error", 200, "application/json", []byte(`{"jsonrpc":"2.0","id":1,"error":{"message":"bad"}}`), "mcp error: bad"},
		{"bad json", 200, "application/json", []byte("not json"), "parse response"},
		{"empty sse", 200, "text/event-stream", []byte(": keepalive\n\n"), "no result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := serve(t, tt.status, tt.ct, tt.body)
			_, err := p.Search(context.Background(), websearch.Query{Text: "x"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSearch_DomainFilter(t *testing.T) {
	p, _, _ := serve(t, 200, "application/json", fixture(t, "structured.json"))
	res, err := p.Search(context.Background(), websearch.Query{Text: "x", AllowedDomains: []string{"example.com"}})
	if err != nil || len(res) != 1 || res[0].URL != "https://example.com/x" {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("a", maxSnippet+50)
	if got := truncate(long); len(got) != maxSnippet+3 || !strings.HasSuffix(got, "...") {
		t.Errorf("len=%d", len(got))
	}
	if truncate("short") != "short" {
		t.Error("short strings must pass through")
	}
}
