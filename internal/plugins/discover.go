package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const installCountsURL = "https://raw.githubusercontent.com/anthropics/claude-plugins-official/refs/heads/stats/stats/plugin-installs.json"

// pluginSource is the decoded shape of a marketplace plugin's "source" field.
type pluginSource struct {
	Source string `json:"source"` // "git-subdir"|"url"|"github"|etc.
	URL    string `json:"url,omitempty"`
	Repo   string `json:"repo,omitempty"` // for github shorthand
	Ref    string `json:"ref,omitempty"`
	Path   string `json:"path,omitempty"` // subpath within the repo
	SHA    string `json:"sha,omitempty"`
}

// MarketplacePluginEntry is one plugin listing from a marketplace's marketplace.json.
// Only the fields we actually use are typed; everything else is json.RawMessage so
// unexpected object shapes (source, author, lspServers, etc.) don't cause parse failures.
type MarketplacePluginEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version,omitempty"`
	Category    string          `json:"category,omitempty"`
	Source      json.RawMessage `json:"source,omitempty"` // object — decoded on demand
	Author      json.RawMessage `json:"author,omitempty"` // object — we don't use it
}

// SourceURL returns the git URL for this plugin, or "" if not applicable.
func (e *MarketplacePluginEntry) SourceURL() string {
	if len(e.Source) == 0 {
		return ""
	}
	var s pluginSource
	if err := json.Unmarshal(e.Source, &s); err != nil {
		return ""
	}
	if s.URL != "" {
		return s.URL
	}
	if s.Repo != "" {
		return "https://github.com/" + s.Repo + ".git"
	}
	return ""
}

// SourcePath returns a local path from the marketplace entry source.
// The official marketplace uses string sources like "./plugins/name" for
// plugins that live inside the marketplace repository.
func (e *MarketplacePluginEntry) SourcePath() string {
	if len(e.Source) == 0 {
		return ""
	}
	var path string
	if err := json.Unmarshal(e.Source, &path); err == nil {
		return path
	}
	var s pluginSource
	if err := json.Unmarshal(e.Source, &s); err != nil {
		return ""
	}
	return s.Path
}

// SourceRef returns the git ref (branch/tag) for this plugin, or "".
func (e *MarketplacePluginEntry) SourceRef() string {
	if len(e.Source) == 0 {
		return ""
	}
	var s pluginSource
	if err := json.Unmarshal(e.Source, &s); err != nil {
		return ""
	}
	return s.Ref
}

func (e *MarketplacePluginEntry) SourceSHA() string {
	if len(e.Source) == 0 {
		return ""
	}
	var s pluginSource
	if err := json.Unmarshal(e.Source, &s); err != nil {
		return ""
	}
	return s.SHA
}

// MarketplaceManifest is the .claude-plugin/marketplace.json shape inside a cloned marketplace
type MarketplaceManifest struct {
	Name    string                   `json:"name"`
	Owner   json.RawMessage          `json:"owner,omitempty"` // object — we don't use it
	Plugins []MarketplacePluginEntry `json:"plugins"`
}

// InstallCountEntry is one entry in the GitHub stats JSON
type InstallCountEntry struct {
	Plugin         string `json:"plugin"` // "name@marketplace"
	UniqueInstalls int    `json:"unique_installs"`
}

// installCountsCache is install-counts-cache.json shape
type installCountsCache struct {
	Version   int                 `json:"version"`
	FetchedAt time.Time           `json:"fetchedAt"`
	Counts    []InstallCountEntry `json:"counts"`
}

// installCountsCachePath returns ~/.conduit/plugins/install-counts-cache.json
func installCountsCachePath() string {
	return filepath.Join(pluginsDir(), "install-counts-cache.json")
}

// LoadMarketplaceManifest reads .claude-plugin/marketplace.json from a materialized marketplace.
// marketplaceName must match an entry in known_marketplaces.json.
func LoadMarketplaceManifest(marketplaceName string) (*MarketplaceManifest, error) {
	known, err := LoadKnownMarketplaces()
	if err != nil {
		return nil, fmt.Errorf("discover: load marketplaces: %w", err)
	}
	entry, ok := known[marketplaceName]
	if !ok {
		return nil, fmt.Errorf("discover: marketplace %q not configured", marketplaceName)
	}
	manifestPath := filepath.Join(entry.InstallLocation, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("discover: read marketplace.json: %w", err)
	}
	var m MarketplaceManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("discover: parse marketplace.json: %w", err)
	}
	return &m, nil
}

// LoadInstallCounts returns a map of "pluginName@marketplace" → unique_installs.
// Uses cache with 24h TTL; fetches fresh from GitHub if stale.
// If fetch fails and cache exists, returns stale cache silently.
func LoadInstallCounts() (map[string]int, error) {
	ensurePluginStorageImported()
	cachePath := installCountsCachePath()

	// Try to read cache.
	if data, err := os.ReadFile(cachePath); err == nil {
		var cache installCountsCache
		if json.Unmarshal(data, &cache) == nil && time.Since(cache.FetchedAt) < 24*time.Hour {
			return countsToMap(cache.Counts), nil
		}
	}

	// Fetch fresh.
	fresh, fetchErr := fetchInstallCounts()
	if fetchErr != nil {
		// Return stale cache if available.
		if data, err := os.ReadFile(cachePath); err == nil {
			var cache installCountsCache
			if json.Unmarshal(data, &cache) == nil {
				return countsToMap(cache.Counts), nil
			}
		}
		return nil, fmt.Errorf("discover: fetch install counts: %w", fetchErr)
	}

	// Write cache.
	cache := installCountsCache{
		Version:   1,
		FetchedAt: time.Now().UTC(),
		Counts:    fresh,
	}
	if cacheData, err := json.MarshalIndent(cache, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
		_ = os.WriteFile(cachePath, append(cacheData, '\n'), 0o644)
	}

	return countsToMap(fresh), nil
}

// fetchInstallCounts fetches the install stats JSON from GitHub.
func fetchInstallCounts() ([]InstallCountEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, installCountsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var entries []InstallCountEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return entries, nil
}

func countsToMap(counts []InstallCountEntry) map[string]int {
	m := make(map[string]int, len(counts))
	for _, c := range counts {
		m[c.Plugin] = c.UniqueInstalls
	}
	return m
}
