package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// PluginInstallationEntry mirrors the V2 installed_plugins.json entry shape.
type PluginInstallationEntry struct {
	Scope        string `json:"scope"`
	ProjectPath  string `json:"projectPath,omitempty"`
	InstallPath  string `json:"installPath"`
	Version      string `json:"version,omitempty"`
	InstalledAt  string `json:"installedAt"`
	LastUpdated  string `json:"lastUpdated,omitempty"`
	GitCommitSHA string `json:"gitCommitSha,omitempty"`
}

// InstalledPluginsV2 is the installed_plugins.json V2 file shape.
type InstalledPluginsV2 struct {
	Version int                                  `json:"version"`
	Plugins map[string][]PluginInstallationEntry `json:"plugins"`
}

// installedPluginsPath returns the path to installed_plugins.json.
func installedPluginsPath() string {
	return filepath.Join(pluginsDir(), "installed_plugins.json")
}

// LoadInstalledPlugins reads installed_plugins.json and returns the V2 data.
func LoadInstalledPlugins() (*InstalledPluginsV2, error) {
	data, err := os.ReadFile(installedPluginsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &InstalledPluginsV2{Version: 2, Plugins: make(map[string][]PluginInstallationEntry)}, nil
		}
		return nil, err
	}
	var f InstalledPluginsV2
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Plugins == nil {
		f.Plugins = make(map[string][]PluginInstallationEntry)
	}
	return &f, nil
}

// saveInstalledPlugins writes the V2 file atomically.
func saveInstalledPlugins(f *InstalledPluginsV2) error {
	path := installedPluginsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f.Version = 2
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Install installs a plugin from a marketplace.
// pluginSpec is "pluginName" or "pluginName@marketplaceName".
// scope is "user" (default), "project", or "local".
// cwd is used for project/local scope.
func Install(ctx context.Context, pluginSpec, scope, cwd string) (*PluginInstallationEntry, error) {
	if scope == "" {
		scope = "user"
	}

	// Parse pluginName@marketplace.
	pluginName, marketplaceName := parsePluginSpec(pluginSpec)
	pluginID := pluginName + "@" + marketplaceName

	// Find the plugin source directory in the marketplace.
	srcDir := MarketplacePluginDir(marketplaceName, pluginName)
	if srcDir == "" {
		// Try to load/refresh the marketplace first.
		known, err := LoadKnownMarketplaces()
		if err != nil {
			return nil, fmt.Errorf("install: load marketplaces: %w", err)
		}
		if _, ok := known[marketplaceName]; !ok {
			return nil, fmt.Errorf("install: marketplace %q not configured\nAdd it with: /plugin marketplace add <source>", marketplaceName)
		}
		// Don't git pull — it can fail on detached/rebased repos. Just look up manifest.
		srcDir = MarketplacePluginDir(marketplaceName, pluginName)
		if srcDir == "" {
			// Plugin may be "external" — defined in marketplace.json with its own source URL.
			// Try to clone it from the marketplace manifest's source field.
			srcDir, err = cloneExternalPlugin(ctx, marketplaceName, pluginName, pluginsDir())
			if err != nil {
				return nil, fmt.Errorf("install: plugin %q not found in marketplace %q: %w", pluginName, marketplaceName, err)
			}
		}
	}

	// Read the plugin manifest to get version.
	p, err := loadPlugin(srcDir)
	if err != nil {
		return nil, fmt.Errorf("install: load plugin manifest: %w", err)
	}

	version := p.Manifest.Version
	if version == "" {
		// Use git SHA of the marketplace clone as version.
		if sha, err := gitHead(srcDir); err == nil {
			if len(sha) > 12 {
				sha = sha[:12]
			}
			version = sha
		}
	}
	if version == "" {
		version = "unknown"
	}

	// Copy plugin to versioned cache path.
	safeMarketplace := sanitizePathComponent(marketplaceName)
	safePlugin := sanitizePathComponent(pluginName)
	safeVersion := sanitizePathComponent(version)
	cachePath := filepath.Join(pluginsDir(), "cache", safeMarketplace, safePlugin, safeVersion)

	if err := copyDir(srcDir, cachePath); err != nil {
		return nil, fmt.Errorf("install: cache plugin: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entry := PluginInstallationEntry{
		Scope:       scope,
		InstallPath: cachePath,
		Version:     version,
		InstalledAt: now,
		LastUpdated: now,
	}
	if scope == "project" || scope == "local" {
		entry.ProjectPath = cwd
	}

	// Write to installed_plugins.json.
	installed, err := LoadInstalledPlugins()
	if err != nil {
		return nil, err
	}
	// Upsert: replace existing entry for same scope+projectPath, else append.
	entries := installed.Plugins[pluginID]
	replaced := false
	for i, e := range entries {
		if e.Scope == scope && e.ProjectPath == entry.ProjectPath {
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	installed.Plugins[pluginID] = entries
	if err := saveInstalledPlugins(installed); err != nil {
		return nil, err
	}

	// Mark enabled in settings.
	if err := settings.SetPluginEnabled(pluginID, true); err != nil {
		return nil, fmt.Errorf("install: update settings: %w", err)
	}

	return &entry, nil
}

// Uninstall removes a plugin installation from the given scope.
func Uninstall(pluginSpec, scope, cwd string) error {
	if scope == "" {
		scope = "user"
	}
	pluginName, marketplaceName := parsePluginSpec(pluginSpec)
	pluginID := pluginName + "@" + marketplaceName

	installed, err := LoadInstalledPlugins()
	if err != nil {
		return err
	}
	entries, ok := installed.Plugins[pluginID]
	if !ok || len(entries) == 0 {
		return fmt.Errorf("plugin %q is not installed", pluginID)
	}

	var kept []PluginInstallationEntry
	var removed *PluginInstallationEntry
	for _, e := range entries {
		match := e.Scope == scope
		if scope == "project" || scope == "local" {
			match = match && e.ProjectPath == cwd
		}
		if match && removed == nil {
			removed = &e
		} else {
			kept = append(kept, e)
		}
	}
	if removed == nil {
		return fmt.Errorf("plugin %q not installed at scope %q", pluginID, scope)
	}

	if len(kept) == 0 {
		delete(installed.Plugins, pluginID)
		// Last installation — delete cached files.
		if removed.InstallPath != "" {
			_ = os.RemoveAll(removed.InstallPath)
		}
		// Remove from settings.
		if err := settings.RemovePlugin(pluginID); err != nil {
			return err
		}
	} else {
		installed.Plugins[pluginID] = kept
		// Still installed at other scopes — just disable at this scope.
		if err := settings.SetPluginEnabled(pluginID, false); err != nil {
			return err
		}
	}

	return saveInstalledPlugins(installed)
}

// parsePluginSpec splits "pluginName@marketplace" into components.
// Defaults marketplace to "claude-plugins-official" if absent.
func parsePluginSpec(spec string) (name, marketplace string) {
	at := strings.LastIndex(spec, "@")
	if at < 0 {
		return spec, "claude-plugins-official"
	}
	return spec[:at], spec[at+1:]
}

// sanitizePathComponent replaces non-alphanumeric chars with "-".
func sanitizePathComponent(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	return sb.String()
}

// gitCloneOrPull clones a git repo to dst, or pulls if it already exists.
// Uses sparse checkout if sparsePaths is non-empty.
func gitCloneOrPull(ctx context.Context, url, ref string, sparsePaths []string, dst string) error {
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		// Already cloned — pull.
		args := []string{"-C", dst, "pull", "--ff-only"}
		if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("git pull: %w\n%s", err, out)
		}
		return nil
	}

	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	if len(sparsePaths) > 0 {
		args = append(args, "--filter=tree:0", "--no-checkout")
	}
	args = append(args, url, dst)
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w\n%s", err, out)
	}

	if len(sparsePaths) > 0 {
		sparseArgs := append([]string{"-C", dst, "sparse-checkout", "set", "--cone", "--"}, sparsePaths...)
		if out, err := exec.CommandContext(ctx, "git", sparseArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("git sparse-checkout: %w\n%s", err, out)
		}
		if out, err := exec.CommandContext(ctx, "git", "-C", dst, "checkout").CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout: %w\n%s", err, out)
		}
	}
	return nil
}

// gitHead returns the current HEAD commit SHA for a directory.
func gitHead(dir string) (string, error) {
	// Walk up to find the git root.
	for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			out, err := exec.CommandContext(context.Background(), "git", "-C", d, "rev-parse", "HEAD").Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", fmt.Errorf("no git repo found")
}

// copyDir copies src directory tree to dst, creating dst if needed.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
