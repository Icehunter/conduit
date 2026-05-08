package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSemver(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want [3]int
		ok   bool
	}{
		{"plain", "1.4.0", [3]int{1, 4, 0}, true},
		{"v-prefix", "v1.4.0", [3]int{1, 4, 0}, true},
		{"prerelease suffix", "1.5.0-rc.1", [3]int{1, 5, 0}, true},
		{"build metadata", "1.5.0+abc", [3]int{1, 5, 0}, true},
		{"too few parts", "1.5", [3]int{}, false},
		{"non-numeric", "1.x.0", [3]int{}, false},
		{"empty", "", [3]int{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseSemver(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok=%v want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestNewer(t *testing.T) {
	tests := []struct {
		name            string
		latest, current string
		want            bool
	}{
		{"newer minor", "1.5.0", "1.4.0", true},
		{"newer patch", "1.4.1", "1.4.0", true},
		{"newer major", "2.0.0", "1.99.99", true},
		{"same", "1.4.0", "1.4.0", false},
		{"older", "1.3.0", "1.4.0", false},
		{"v-prefix tolerated", "v1.5.0", "v1.4.0", true},
		{"current dev returns false", "1.5.0", "dev", false},
		{"current empty returns false", "1.5.0", "", false},
		{"latest unparseable returns false", "garbage", "1.4.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newer(tt.latest, tt.current); got != tt.want {
				t.Errorf("newer(%q,%q)=%v want %v", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}

func TestIsDev(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"dev", true},
		{"1.4.0-dirty", true},
		{"1.4.0", false},
		{"v1.4.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isDev(tt.in); got != tt.want {
				t.Errorf("isDev(%q)=%v want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestUpgradeCmd(t *testing.T) {
	tests := []struct {
		m       InstallMethod
		wantSub string
	}{
		{InstallHomebrew, "brew upgrade"},
		{InstallScoop, "scoop update"},
		{InstallWinget, "winget upgrade"},
		{InstallGoInstall, "go install"},
		{InstallDirect, "github.com"},
		{InstallUnknown, "github.com"},
	}
	for _, tt := range tests {
		t.Run(tt.wantSub, func(t *testing.T) {
			got := upgradeCmd(tt.m)
			if !contains(got, tt.wantSub) {
				t.Errorf("upgradeCmd(%v)=%q, expected to contain %q", tt.m, got, tt.wantSub)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}())
}

func TestCheck_FetchesAndCaches(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.5.0"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	c := &Checker{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		CacheDir:   tmp,
		Now:        func() time.Time { return now },
	}

	res, err := c.Check(context.Background(), "1.4.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Latest != "1.5.0" || !res.HasUpdate {
		t.Errorf("first check: got latest=%q hasUpdate=%v", res.Latest, res.HasUpdate)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call, got %d", calls)
	}

	// Second call within TTL should hit cache.
	res2, err := c.Check(context.Background(), "1.4.0")
	if err != nil {
		t.Fatalf("Check 2: %v", err)
	}
	if res2.Latest != "1.5.0" || !res2.HasUpdate {
		t.Errorf("cached check: got latest=%q hasUpdate=%v", res2.Latest, res2.HasUpdate)
	}
	if calls != 1 {
		t.Errorf("cache should have prevented second HTTP call, got %d", calls)
	}

	// Verify cache file exists.
	if _, err := os.Stat(filepath.Join(tmp, "update-cache.json")); err != nil {
		t.Errorf("cache file: %v", err)
	}
}

func TestCheck_CacheExpires(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.5.0"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	t0 := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	now := t0
	c := &Checker{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		CacheDir:   tmp,
		Now:        func() time.Time { return now },
	}

	if _, err := c.Check(context.Background(), "1.4.0"); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(CacheTTL + time.Minute)
	if _, err := c.Check(context.Background(), "1.4.0"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected 2 HTTP calls after TTL expiry, got %d", calls)
	}
}

func TestCheck_DevSkipsNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected HTTP call for dev build")
	}))
	defer srv.Close()

	c := &Checker{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		CacheDir:   t.TempDir(),
		Now:        time.Now,
	}
	res, err := c.Check(context.Background(), "dev")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.HasUpdate {
		t.Errorf("dev build should never report HasUpdate=true")
	}
}

func TestCheck_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &Checker{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		CacheDir:   t.TempDir(),
		Now:        time.Now,
	}
	_, err := c.Check(context.Background(), "1.4.0")
	if err == nil {
		t.Errorf("expected error on 500, got nil")
	}
}

func TestCheck_PrereleaseRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v2.0.0", Prerelease: true})
	}))
	defer srv.Close()

	c := &Checker{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		CacheDir:   t.TempDir(),
		Now:        time.Now,
	}
	_, err := c.Check(context.Background(), "1.4.0")
	if err == nil {
		t.Errorf("expected error when latest is prerelease, got nil")
	}
}

func TestCheck_CtxCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.5.0"})
	}))
	defer srv.Close()

	c := &Checker{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		CacheDir:   t.TempDir(),
		Now:        time.Now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Check(ctx, "1.4.0")
	if err == nil {
		t.Errorf("expected error from cancelled ctx, got nil")
	}
}
