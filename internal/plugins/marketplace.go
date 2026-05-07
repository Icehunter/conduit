package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	defaultMarketplaceName   = "claude-plugins-official"
	defaultMarketplaceSource = "anthropics/claude-plugins-official"
)

var knownMarketplacesMu sync.Mutex

// MarketplaceSource describes where a marketplace comes from.
// Mirrors the TS union type in schemas.ts.
type MarketplaceSource struct {
	Source      string   `json:"source"`                // "github"|"git"|"url"|"file"|"directory"
	Repo        string   `json:"repo,omitempty"`        // for source=github: "owner/repo"
	URL         string   `json:"url,omitempty"`         // for source=git|url
	Path        string   `json:"path,omitempty"`        // for source=file|directory
	Ref         string   `json:"ref,omitempty"`         // git branch/tag
	SparsePaths []string `json:"sparsePaths,omitempty"` // sparse-checkout paths
}

// MarketplaceEntry is one entry in known_marketplaces.json.
type MarketplaceEntry struct {
	Source          MarketplaceSource `json:"source"`
	InstallLocation string            `json:"installLocation"`
	LastUpdated     string            `json:"lastUpdated"`
	AutoUpdate      bool              `json:"autoUpdate,omitempty"`
}

// knownMarketplacesPath returns the path to Conduit's known_marketplaces.json.
func knownMarketplacesPath() string {
	return filepath.Join(pluginsDir(), "known_marketplaces.json")
}

// LoadKnownMarketplaces reads ~/.conduit/plugins/known_marketplaces.json.
func LoadKnownMarketplaces() (map[string]MarketplaceEntry, error) {
	ensurePluginStorageImported()
	data, err := os.ReadFile(knownMarketplacesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return loadKnownMarketplacesWithDefault(make(map[string]MarketplaceEntry)), nil
		}
		return nil, err
	}
	var m map[string]MarketplaceEntry
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = make(map[string]MarketplaceEntry)
	}
	return loadKnownMarketplacesWithDefault(m), nil
}

func loadKnownMarketplacesWithDefault(m map[string]MarketplaceEntry) map[string]MarketplaceEntry {
	if _, ok := m[defaultMarketplaceName]; ok {
		return m
	}
	loc := filepath.Join(pluginsDir(), "marketplaces", defaultMarketplaceName)
	if _, err := os.Stat(filepath.Join(loc, ".claude-plugin", "marketplace.json")); err != nil {
		return m
	}
	m[defaultMarketplaceName] = MarketplaceEntry{
		Source: MarketplaceSource{
			Source: "github",
			Repo:   defaultMarketplaceSource,
		},
		InstallLocation: loc,
	}
	return m
}

func ensureMarketplaceConfigured(ctx context.Context, name string) error {
	known, err := LoadKnownMarketplaces()
	if err != nil {
		return err
	}
	if _, ok := known[name]; ok {
		return nil
	}
	if name != defaultMarketplaceName {
		return fmt.Errorf("marketplace %q not configured\nAdd it with: /plugin marketplace add <source>", name)
	}
	return MarketplaceAdd(ctx, defaultMarketplaceName, defaultMarketplaceSource, nil)
}

// saveKnownMarketplaces writes the marketplace registry atomically.
func saveKnownMarketplaces(m map[string]MarketplaceEntry) error {
	path := knownMarketplacesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePluginFileAtomic(path, append(data, '\n'), 0o644)
}

// MarketplaceAdd adds a new marketplace from a source string.
// source can be: "owner/repo" (GitHub), "https://..." (git URL), or local path.
func MarketplaceAdd(ctx context.Context, name, source string, sparsePaths []string) error {
	ms := parseMarketplaceSource(source, sparsePaths)

	// Clone/materialize the marketplace to get its manifest.
	installLoc := filepath.Join(pluginsDir(), "marketplaces", name)
	if err := materializeMarketplace(ctx, ms, installLoc); err != nil {
		return fmt.Errorf("marketplace add: %w", err)
	}

	entry := MarketplaceEntry{
		Source:          ms,
		InstallLocation: installLoc,
		LastUpdated:     time.Now().UTC().Format(time.RFC3339),
	}
	return updateKnownMarketplaces(func(known map[string]MarketplaceEntry) {
		known[name] = entry
	})
}

// MarketplaceRemove removes a marketplace from the registry.
func MarketplaceRemove(name string) error {
	found := false
	if err := updateKnownMarketplaces(func(known map[string]MarketplaceEntry) {
		if _, ok := known[name]; ok {
			found = true
			delete(known, name)
		}
	}); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("marketplace %q not found", name)
	}
	return nil
}

// MarketplaceUpdate refreshes one or all marketplaces from their source.
func MarketplaceUpdate(ctx context.Context, name string) error {
	known, err := LoadKnownMarketplaces()
	if err != nil {
		return err
	}
	update := func(n string, entry MarketplaceEntry) error {
		if err := materializeMarketplace(ctx, entry.Source, entry.InstallLocation); err != nil {
			return fmt.Errorf("update %s: %w", n, err)
		}
		entry.LastUpdated = time.Now().UTC().Format(time.RFC3339)
		known[n] = entry
		return saveKnownMarketplaces(known)
	}
	if name != "" {
		entry, ok := known[name]
		if !ok {
			return fmt.Errorf("marketplace %q not found", name)
		}
		return update(name, entry)
	}
	for n, entry := range known {
		if err := update(n, entry); err != nil {
			return err
		}
	}
	return nil
}

func updateKnownMarketplaces(fn func(map[string]MarketplaceEntry)) error {
	knownMarketplacesMu.Lock()
	defer knownMarketplacesMu.Unlock()
	known, err := LoadKnownMarketplaces()
	if err != nil {
		return err
	}
	fn(known)
	return saveKnownMarketplaces(known)
}

// parseMarketplaceSource converts a user-supplied source string to a MarketplaceSource.
func parseMarketplaceSource(source string, sparsePaths []string) MarketplaceSource {
	ms := MarketplaceSource{SparsePaths: sparsePaths}
	switch {
	case isGitHubShorthand(source):
		ms.Source = "github"
		ms.Repo = source
	case len(source) > 4 && (source[:8] == "https://" || source[:6] == "git://"):
		ms.Source = "git"
		ms.URL = source
	default:
		ms.Source = "directory"
		ms.Path = source
	}
	return ms
}

// isGitHubShorthand returns true for "owner/repo" format strings.
func isGitHubShorthand(s string) bool {
	if len(s) < 3 {
		return false
	}
	slash := 0
	for _, c := range s {
		if c == '/' {
			slash++
		}
		if c == '.' || c == ':' {
			return false
		}
	}
	return slash == 1
}

// materializeMarketplace clones/copies a marketplace to installLoc.
func materializeMarketplace(ctx context.Context, ms MarketplaceSource, installLoc string) error {
	if err := os.MkdirAll(installLoc, 0o755); err != nil {
		return err
	}
	switch ms.Source {
	case "github":
		url := "https://github.com/" + ms.Repo + ".git"
		return gitCloneOrPull(ctx, url, ms.Ref, ms.SparsePaths, installLoc)
	case "git":
		return gitCloneOrPull(ctx, ms.URL, ms.Ref, ms.SparsePaths, installLoc)
	case "file", "directory":
		// Local path — just use it in-place (no copy needed for marketplace).
		return nil
	default:
		return fmt.Errorf("unsupported marketplace source type %q", ms.Source)
	}
}

// MarketplacePluginDir returns the directory where a specific plugin's files
// live within a materialized marketplace. Checks the standard layouts:
//   - <marketplaceDir>/plugins/<pluginName>/
//   - <marketplaceDir>/<pluginName>/
func MarketplacePluginDir(marketplaceName, pluginName string) string {
	known, err := LoadKnownMarketplaces()
	if err != nil {
		return ""
	}
	entry, ok := known[marketplaceName]
	if !ok {
		return ""
	}
	loc := entry.InstallLocation

	// Official anthropics/claude-plugins-official layout: plugins/<name>/ and external_plugins/<name>/
	for _, sub := range []string{"plugins", "external_plugins", ""} {
		var dir string
		if sub == "" {
			dir = filepath.Join(loc, pluginName)
		} else {
			dir = filepath.Join(loc, sub, pluginName)
		}
		if fi, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err == nil && !fi.IsDir() {
			return dir
		}
	}
	return ""
}

func marketplacePluginSourceDir(marketplaceName, pluginName string) string {
	known, err := LoadKnownMarketplaces()
	if err != nil {
		return ""
	}
	entry, ok := known[marketplaceName]
	if !ok {
		return ""
	}
	manifest, err := LoadMarketplaceManifest(marketplaceName)
	if err != nil {
		return ""
	}
	for _, p := range manifest.Plugins {
		if p.Name != pluginName {
			continue
		}
		srcPath := p.SourcePath()
		if srcPath == "" {
			return ""
		}
		dir := srcPath
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(entry.InstallLocation, dir)
		}
		dir = filepath.Clean(dir)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
	}
	return ""
}

func loadMarketplacePluginFallback(marketplaceName, pluginName, dir string) (*Plugin, error) {
	manifest, err := LoadMarketplaceManifest(marketplaceName)
	if err != nil {
		return nil, err
	}
	for _, entry := range manifest.Plugins {
		if entry.Name != pluginName {
			continue
		}
		p := &Plugin{
			Dir: dir,
			Manifest: Manifest{
				Name:        entry.Name,
				Description: entry.Description,
				Version:     entry.Version,
			},
		}
		if p.Manifest.Version == "" {
			p.Manifest.Version = entry.SourceSHA()
		}
		return p, nil
	}
	return nil, fmt.Errorf("plugin not listed in marketplace manifest")
}

// cloneExternalPlugin handles plugins whose source is a separate git repo (not
// a subdirectory of the marketplace clone). It reads the plugin's source URL
// from the marketplace manifest and clones it to a local cache directory.
// Returns the cloned directory path, or an error if the plugin has no git source.
func cloneExternalPlugin(ctx context.Context, marketplaceName, pluginName, pluginsCacheDir string) (string, error) {
	manifest, err := LoadMarketplaceManifest(marketplaceName)
	if err != nil {
		return "", fmt.Errorf("read marketplace manifest: %w", err)
	}

	// Find the plugin entry in the manifest.
	var pluginURL, pluginRef, pluginPath string
	found := false
	for _, p := range manifest.Plugins {
		if p.Name == pluginName {
			found = true
			pluginURL = p.SourceURL()
			pluginRef = p.SourceRef()
			pluginPath = p.SourcePath()
			break
		}
	}
	if !found {
		return "", fmt.Errorf("plugin not listed in marketplace manifest")
	}
	if pluginURL == "" {
		return "", fmt.Errorf("plugin has no installable source URL")
	}

	dst := filepath.Join(pluginsCacheDir, "external", marketplaceName, pluginName)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := gitCloneOrPull(ctx, pluginURL, pluginRef, nil, dst); err != nil {
		return "", fmt.Errorf("clone %s: %w", pluginURL, err)
	}
	if pluginPath != "" {
		subdir := filepath.Join(dst, pluginPath)
		if fi, err := os.Stat(subdir); err == nil && fi.IsDir() {
			return subdir, nil
		}
	}
	return dst, nil
}
