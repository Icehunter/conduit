package mojeek

import (
	"context"
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

func serve(t *testing.T, status int, body []byte) (*Provider, *http.Request) {
	t.Helper()
	var got http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return newWithClient(srv.Client(), srv.URL+"/search"), &got
}

func TestSearch_ParsesFixture(t *testing.T) {
	p, req := serve(t, 200, fixture(t, "results.html"))
	res, err := p.Search(context.Background(), websearch.Query{Text: "go docs", MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Query().Get("q") != "go docs" || req.URL.Query().Get("t") != "5" {
		t.Errorf("query = %v", req.URL.Query())
	}
	if req.Header.Get("User-Agent") != websearch.UserAgent {
		t.Errorf("User-Agent = %q", req.Header.Get("User-Agent"))
	}
	want := []websearch.Result{
		{Title: "Documentation - The Go Programming Language", URL: "https://go.dev/doc/?x=1&y=2", Snippet: "Official Go documentation & tutorials."},
		{Title: "Standard library", URL: "https://pkg.go.dev/std", Snippet: "Packages in the standard library."},
	}
	if len(res) != len(want) {
		t.Fatalf("got %d: %+v, want %d", len(res), res, len(want))
	}
	for i := range want {
		if res[i] != want[i] {
			t.Errorf("result[%d] = %+v, want %+v", i, res[i], want[i])
		}
	}
}

func TestSearch_DomainFilter(t *testing.T) {
	p, _ := serve(t, 200, fixture(t, "results.html"))
	res, err := p.Search(context.Background(), websearch.Query{Text: "x", BlockedDomains: []string{"go.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	// pkg.go.dev is a subdomain of go.dev, so it is blocked as well.
	if len(res) != 0 {
		t.Errorf("got %+v, want none", res)
	}
}

func TestSearch_Errors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
		want   string
	}{
		{"captcha wall 200", 200, fixture(t, "captcha.html"), "automated-queries wall"},
		{"robot refusal 403", 403, []byte("You are sending automated queries"), "automated-queries wall"},
		{"server error", 500, []byte("oops"), "status 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := serve(t, tt.status, tt.body)
			_, err := p.Search(context.Background(), websearch.Query{Text: "x"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://a.com/x", "https://a.com/x"},
		{"https://www.mojeek.com/about", ""},
		{"https://sub.mojeek.de/x", ""},
		{"/search?q=1", ""},
		{"ftp://a.com", ""},
	}
	for _, tt := range tests {
		if got := normalizeURL(tt.in); got != tt.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSearch_ResultsPageMentioningCaptchaIsNotAWall(t *testing.T) {
	page := append(fixture(t, "results.html"), []byte("<footer><altcha-widget></altcha-widget></footer>")...)
	p, _ := serve(t, 200, page)
	res, err := p.Search(context.Background(), websearch.Query{Text: "x"})
	if err != nil || len(res) != 2 {
		t.Errorf("res=%+v err=%v, want 2 results", res, err)
	}
}
