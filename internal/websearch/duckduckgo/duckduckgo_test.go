package duckduckgo

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

func serve(t *testing.T, status int, body []byte) (*Provider, *http.Request, *string) {
	t.Helper()
	var got http.Request
	var form string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		_ = r.ParseForm()
		form = r.PostForm.Get("q")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return newWithClient(srv.Client(), srv.URL), &got, &form
}

func TestSearch_ParsesFixture(t *testing.T) {
	p, req, q := serve(t, 200, fixture(t, "results.html"))
	res, err := p.Search(context.Background(), websearch.Query{Text: "go blog"})
	if err != nil {
		t.Fatal(err)
	}
	if *q != "go blog" || req.Method != http.MethodPost {
		t.Errorf("request: method=%s q=%q", req.Method, *q)
	}
	if ua := req.Header.Get("User-Agent"); ua != websearch.UserAgent {
		t.Errorf("User-Agent = %q", ua)
	}
	if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
		t.Error("credentials must not be sent")
	}
	want := []websearch.Result{
		{Title: "The Go Blog & News", URL: "https://go.dev/blog?a=1&b=2", Snippet: "Official Go blog 'posts' and releases."},
		{Title: "Direct link, no snippet", URL: "https://example.com/direct"},
	}
	if len(res) != len(want) {
		t.Fatalf("got %d results %+v, want %d (ad, dup, titleless skipped)", len(res), res, len(want))
	}
	for i := range want {
		if res[i] != want[i] {
			t.Errorf("result[%d] = %+v, want %+v", i, res[i], want[i])
		}
	}
}

func TestSearch_DomainFilterAndMax(t *testing.T) {
	tests := []struct {
		name string
		q    websearch.Query
		want []string
	}{
		{"allowed", websearch.Query{Text: "x", AllowedDomains: []string{"go.dev"}}, []string{"https://go.dev/blog?a=1&b=2"}},
		{"blocked", websearch.Query{Text: "x", BlockedDomains: []string{"go.dev"}}, []string{"https://example.com/direct"}},
		{"max", websearch.Query{Text: "x", MaxResults: 1}, []string{"https://go.dev/blog?a=1&b=2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := serve(t, 200, fixture(t, "results.html"))
			res, err := p.Search(context.Background(), tt.q)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range res {
				got = append(got, r.URL)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSearch_Errors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
		want   string
	}{
		{"anomaly on 200", 200, fixture(t, "anomaly.html"), "bot challenge"},
		{"anomaly on 202", 202, fixture(t, "anomaly.html"), "bot challenge"},
		{"server error", 503, []byte("down"), "status 503"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := serve(t, tt.status, tt.body)
			_, err := p.Search(context.Background(), websearch.Query{Text: "x"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSearch_EmptyPageIsNoResults(t *testing.T) {
	p, _, _ := serve(t, 200, []byte("<html><body>nothing</body></html>"))
	res, err := p.Search(context.Background(), websearch.Query{Text: "x"})
	if err != nil || len(res) != 0 {
		t.Errorf("res=%v err=%v, want empty and nil", res, err)
	}
}

func TestSearch_ContextCancelled(t *testing.T) {
	p, _, _ := serve(t, 200, fixture(t, "results.html"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Search(ctx, websearch.Query{Text: "x"}); err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestUnwrapURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fa.com%2Fp%3Fq%3D1&rut=z", "https://a.com/p?q=1"},
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fa.com&amp;rut=z", "https://a.com"},
		{"//example.org/x", "https://example.org/x"},
		{"http://example.org/x", "http://example.org/x"},
		{"HTTPS://Example.org/x", "HTTPS://Example.org/x"},
		{"//duckduckgo.com/l/?uddg=HTTPS%3A%2F%2Fa.com", "HTTPS://a.com"},
		{"//duckduckgo.com/y.js?ad=1", ""},
		{"/relative", ""},
		{"javascript:alert(1)", ""},
		{"//duckduckgo.com/l/?uddg=javascript%3Aalert(1)", ""},
	}
	for _, tt := range tests {
		if got := unwrapURL(tt.in); got != tt.want {
			t.Errorf("unwrapURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
