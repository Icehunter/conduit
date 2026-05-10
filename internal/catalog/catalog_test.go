package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuiltin_nonEmpty(t *testing.T) {
	c := Builtin()
	if len(c.Models) == 0 {
		t.Fatal("builtin catalog must contain at least one model")
	}
	for _, m := range c.Models {
		if m.ID == "" {
			t.Error("builtin model has empty ID")
		}
		if m.Provider == "" {
			t.Error("builtin model has empty provider:", m.ID)
		}
		if m.ContextWindow <= 0 {
			t.Errorf("builtin model %s has non-positive context window: %d", m.ID, m.ContextWindow)
		}
	}
}

func TestCatalog_Lookup(t *testing.T) {
	c := Builtin()
	tests := []struct {
		query  string
		wantID string
		wantOK bool
	}{
		{"claude-opus-4-7", "claude-opus-4-7", true},
		{"claude-sonnet-4-6", "claude-sonnet-4-6", true},
		{"CLAUDE-OPUS-4-7", "claude-opus-4-7", true},           // case-insensitive
		{"anthropic/claude-opus-4-7", "claude-opus-4-7", true}, // provider-prefixed
		{"no-such-model-xyz", "", false},
	}
	for _, tt := range tests {
		got, ok := c.Lookup(tt.query)
		if ok != tt.wantOK {
			t.Errorf("Lookup(%q) ok=%v; want %v", tt.query, ok, tt.wantOK)
			continue
		}
		if ok && got.ID != tt.wantID {
			t.Errorf("Lookup(%q) id=%q; want %q", tt.query, got.ID, tt.wantID)
		}
	}
}

func TestCatalog_LookupDotDashVariant(t *testing.T) {
	c := &Catalog{Models: []ModelInfo{{
		ID:              "anthropic/claude-sonnet-4.6",
		Name:            "Claude Sonnet 4.6",
		Provider:        "anthropic",
		ContextWindow:   200_000,
		InputCostPer1M:  3,
		OutputCostPer1M: 15,
	}}}

	tests := []string{
		"claude-sonnet-4-6",
		"anthropic/claude-sonnet-4-6",
		"CLAUDE-SONNET-4-6",
	}
	for _, query := range tests {
		got, ok := c.Lookup(query)
		if !ok {
			t.Fatalf("Lookup(%q) ok=false; want true", query)
		}
		if got.ID != "anthropic/claude-sonnet-4.6" {
			t.Errorf("Lookup(%q) id=%q; want %q", query, got.ID, "anthropic/claude-sonnet-4.6")
		}
	}
}

func TestCatalog_LookupProviderKeySuffix(t *testing.T) {
	c := &Catalog{Models: []ModelInfo{
		{
			ID:              "openai/gpt-5.5",
			Name:            "GPT 5.5",
			Provider:        "openai",
			ContextWindow:   400_000,
			InputCostPer1M:  5,
			OutputCostPer1M: 20,
		},
		{
			ID:              "google/gemini-flash-latest",
			Name:            "Gemini Flash Latest",
			Provider:        "google",
			ContextWindow:   1_000_000,
			InputCostPer1M:  0.3,
			OutputCostPer1M: 2.5,
		},
	}}

	tests := []struct {
		query  string
		wantID string
	}{
		{"provider:openai-compatible.OpenAPI.gpt-5.5", "openai/gpt-5.5"},
		{"provider:openai-compatible.syndicated.life@gmail.com.gemini-flash-latest", "google/gemini-flash-latest"},
	}
	for _, tt := range tests {
		got, ok := c.Lookup(tt.query)
		if !ok {
			t.Fatalf("Lookup(%q) ok=false; want true", tt.query)
		}
		if got.ID != tt.wantID {
			t.Errorf("Lookup(%q) id=%q; want %q", tt.query, got.ID, tt.wantID)
		}
	}
}

func TestCatalog_Lookup_nil(t *testing.T) {
	var c *Catalog
	_, ok := c.Lookup("anything")
	if ok {
		t.Error("nil Catalog.Lookup must return false")
	}
}

func TestCatalog_ForProvider(t *testing.T) {
	c := Builtin()
	anthropic := c.ForProvider("anthropic")
	if len(anthropic) == 0 {
		t.Fatal("ForProvider(anthropic) must return models")
	}
	for _, m := range anthropic {
		if m.Provider != "anthropic" {
			t.Errorf("unexpected provider %q in ForProvider(anthropic)", m.Provider)
		}
	}
	empty := c.ForProvider("no-such-provider-xyz")
	if len(empty) != 0 {
		t.Errorf("ForProvider(unknown) must return empty; got %d", len(empty))
	}
}

func TestCatalog_ForProvider_nil(t *testing.T) {
	var c *Catalog
	if got := c.ForProvider("anthropic"); got != nil {
		t.Error("nil Catalog.ForProvider must return nil")
	}
}

func TestCatalog_IsStale(t *testing.T) {
	var nilC *Catalog
	if !nilC.IsStale(time.Hour) {
		t.Error("nil catalog must be stale")
	}

	fresh := &Catalog{FetchedAt: time.Now()}
	if fresh.IsStale(time.Hour) {
		t.Error("just-created catalog must not be stale for 1h TTL")
	}

	old := &Catalog{FetchedAt: time.Now().Add(-25 * time.Hour)}
	if !old.IsStale(24 * time.Hour) {
		t.Error("25h-old catalog must be stale for 24h TTL")
	}
}

func TestCache_roundTrip(t *testing.T) {
	dir := t.TempDir()
	orig := Builtin()
	orig.FetchedAt = time.Now().Round(0) // strip monotonic for comparison

	if err := SaveCache(dir, orig); err != nil {
		t.Fatal("SaveCache:", err)
	}

	loaded, err := LoadCache(dir)
	if err != nil {
		t.Fatal("LoadCache:", err)
	}
	if loaded == nil {
		t.Fatal("LoadCache returned nil")
	}
	if len(loaded.Models) != len(orig.Models) {
		t.Errorf("model count: got %d; want %d", len(loaded.Models), len(orig.Models))
	}
	if loaded.Source != "cache" {
		t.Errorf("Source = %q; want \"cache\"", loaded.Source)
	}
}

func TestCache_missingFile(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadCache(dir)
	if err != nil {
		t.Fatal("LoadCache on missing file must not error:", err)
	}
	if c != nil {
		t.Error("LoadCache on missing file must return nil")
	}
}

func TestCache_malformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte("{bad json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCache(dir)
	if err == nil {
		t.Error("LoadCache on malformed JSON must return error")
	}
}

func TestLoad_fallbackToBuiltin(t *testing.T) {
	dir := t.TempDir()
	c := Load(dir)
	if c == nil {
		t.Fatal("Load must never return nil")
	}
	if len(c.Models) == 0 {
		t.Error("Load must return non-empty catalog")
	}
}

func TestLoad_ignoresStaleCache(t *testing.T) {
	dir := t.TempDir()
	stale := &Catalog{
		Models: []ModelInfo{{
			ID:            "stale-model",
			Name:          "Stale Model",
			Provider:      "test",
			ContextWindow: 1,
		}},
		FetchedAt: time.Now().Add(-DefaultTTL - time.Hour),
		Source:    "openrouter",
	}
	if err := SaveCache(dir, stale); err != nil {
		t.Fatal("SaveCache:", err)
	}

	loaded := Load(dir)
	if loaded == nil {
		t.Fatal("Load returned nil")
	}
	if loaded.Source != "builtin" {
		t.Fatalf("Load stale cache Source = %q; want builtin", loaded.Source)
	}
	if _, ok := loaded.Lookup("stale-model"); ok {
		t.Fatal("Load returned stale cache model; want builtin fallback")
	}
}

func TestParsePricingPer1M(t *testing.T) {
	tests := []struct {
		s    string
		want float64
	}{
		{"0.000003", 3.0},
		{"0.000015", 15.0},
		{"0", 0},
		{"", 0},
		{"bad", 0},
	}
	for _, tt := range tests {
		got := parsePricingPer1M(tt.s)
		if got != tt.want {
			t.Errorf("parsePricingPer1M(%q) = %f; want %f", tt.s, got, tt.want)
		}
	}
}

func TestProviderFromID(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"anthropic/claude-opus-4-7", "anthropic"},
		{"openai/gpt-4o", "openai"},
		{"no-slash", "unknown"},
	}
	for _, tt := range tests {
		if got := providerFromID(tt.id); got != tt.want {
			t.Errorf("providerFromID(%q) = %q; want %q", tt.id, got, tt.want)
		}
	}
}

func TestCachePath(t *testing.T) {
	path := CachePath("/home/user/.conduit")
	if path == "" {
		t.Error("CachePath must not be empty")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	c := Builtin()
	data, err := marshalIndentBuf(c)
	if err != nil {
		t.Fatal("marshal:", err)
	}
	var back Catalog
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal("unmarshal:", err)
	}
	if len(back.Models) != len(c.Models) {
		t.Errorf("round-trip model count: got %d; want %d", len(back.Models), len(c.Models))
	}
}

func TestCatalogFromJSON_invalid(t *testing.T) {
	_, err := catalogFromJSON([]byte("{invalid"))
	if err == nil {
		t.Error("catalogFromJSON must error on invalid JSON")
	}
}
