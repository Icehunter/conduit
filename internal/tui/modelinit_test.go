package tui

import (
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/catalog"
)

// TestCatalogNeedsRefresh_BuiltinSourceAlwaysNeedsRefresh is a regression
// test: catalog.Builtin() stamps FetchedAt as time.Now() on every call, so a
// naive `cat.IsStale(catalog.DefaultTTL)` check is never true for it — the
// auto-refresh at startup would never fire for exactly the "no cache file at
// all, fell back to builtin" case that most needs a live fetch. That was the
// actual bug caught by live testing: after deleting ~/.conduit/catalog.json,
// no refresh happened and the cache file never got created.
func TestCatalogNeedsRefresh_BuiltinSourceAlwaysNeedsRefresh(t *testing.T) {
	b := catalog.Builtin()
	if b.IsStale(catalog.DefaultTTL) {
		t.Fatal("test premise wrong: catalog.Builtin() unexpectedly reports stale — its FetchedAt must have changed to not be time.Now() anymore")
	}
	if !catalogNeedsRefresh(b) {
		t.Error("catalogNeedsRefresh(Builtin()) = false, want true — builtin fallback always needs a live refresh regardless of its self-stamped FetchedAt")
	}
}

func TestCatalogNeedsRefresh(t *testing.T) {
	tests := []struct {
		name string
		cat  *catalog.Catalog
		want bool
	}{
		{"nil catalog", nil, true},
		{"builtin source", &catalog.Catalog{Source: "builtin", FetchedAt: time.Now()}, true},
		{"fresh cache", &catalog.Catalog{Source: "cache", FetchedAt: time.Now()}, false},
		{"fresh openrouter+live", &catalog.Catalog{Source: "openrouter+live", FetchedAt: time.Now()}, false},
		{"stale cache", &catalog.Catalog{Source: "cache", FetchedAt: time.Now().Add(-2 * catalog.DefaultTTL)}, true},
		{"zero FetchedAt (never fetched)", &catalog.Catalog{Source: "cache"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := catalogNeedsRefresh(tt.cat); got != tt.want {
				t.Errorf("catalogNeedsRefresh(%+v) = %v, want %v", tt.cat, got, tt.want)
			}
		})
	}
}
