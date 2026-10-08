package websearch

import (
	"context"
	"strings"
	"testing"
)

func TestFilterDomains(t *testing.T) {
	in := []Result{
		{URL: "https://go.dev/blog"},
		{URL: "https://pkg.go.dev/std"},
		{URL: "https://www.example.com/a"},
		{URL: "https://notgo.dev/x"},
		{URL: "https://evil.com/?u=go.dev"},
		{URL: "::bad"},
	}
	tests := []struct {
		name             string
		allowed, blocked []string
		want             []string
	}{
		{"none", nil, nil, nil},
		{"allowed host and subdomain", []string{"go.dev"}, nil, []string{"https://go.dev/blog", "https://pkg.go.dev/std"}},
		{"allowed with scheme and path", []string{"https://go.dev/blog"}, nil, []string{"https://go.dev/blog", "https://pkg.go.dev/std"}},
		{"www normalised", []string{"example.com"}, nil, []string{"https://www.example.com/a"}},
		{"blocked", nil, []string{"go.dev"}, []string{"https://www.example.com/a", "https://notgo.dev/x", "https://evil.com/?u=go.dev"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterDomains(in, tt.allowed, tt.blocked)
			if tt.want == nil {
				if len(got) != len(in) {
					t.Fatalf("no rules must pass everything, got %d", len(got))
				}
				return
			}
			var urls []string
			for _, r := range got {
				urls = append(urls, r.URL)
			}
			if strings.Join(urls, " ") != strings.Join(tt.want, " ") {
				t.Errorf("got %v, want %v", urls, tt.want)
			}
		})
	}
}

func TestHTMLText(t *testing.T) {
	got := HTMLText("  A <b>bold</b>&nbsp;&amp;\n\t&#x27;q&#x27; &lt;x&gt; ")
	if got != "A bold & 'q' <x>" {
		t.Errorf("got %q", got)
	}
}

func TestScanSSE(t *testing.T) {
	in := ": c\nevent: a\ndata: one\ndata: two\n\ndata:three\n\ndata: tail"
	var got []string
	if err := ScanSSE(context.Background(), strings.NewReader(in), func(d string) bool {
		got = append(got, d)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "one\ntwo|three|tail" {
		t.Errorf("got %q", got)
	}
	got = nil
	_ = ScanSSE(context.Background(), strings.NewReader(in), func(d string) bool { got = append(got, d); return false })
	if len(got) != 1 {
		t.Errorf("fn returning false must stop, got %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ScanSSE(ctx, strings.NewReader(in), func(string) bool { return true }); err == nil {
		t.Error("cancelled ctx must error")
	}
}

func TestDedupeAndLimit(t *testing.T) {
	r := []Result{{URL: "a"}, {URL: "a"}, {URL: ""}, {URL: "b"}, {URL: "c"}}
	d := DedupeURLs(r)
	if len(d) != 3 {
		t.Fatalf("dedupe = %v", d)
	}
	if len(Limit(d, 2)) != 2 || len(Limit(d, 0)) != 3 {
		t.Error("Limit misbehaves")
	}
}
