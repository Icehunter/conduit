package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

var installedPluginsMu sync.Mutex

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

// installedPluginsPath returns the path to Conduit's installed_plugins.json.
func installedPluginsPath() string {
	return filepath.Join(pluginsDir(), "installed_plugins.json")
}

// LoadInstalledPlugins reads installed_plugins.json and returns the V2 data.
func LoadInstalledPlugins() (*InstalledPluginsV2, error) {
	ensurePluginStorageImported()
	data, err := os.ReadFile(installedPluginsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &InstalledPluginsV2{Version: 2, Plugins: make(map[string][]PluginInstallationEntry)}, nil
		}
		return nil, err
	}
	var f InstalledPluginsV2
	if err := json.Unmarshal(data, &f); err != nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		if decErr := dec.Decode(&f); decErr != nil {
			return nil, err
		}
		// The registry had a valid leading object with trailing garbage. Keep
		// the usable data and rewrite the file cleanly on best effort.
		_ = saveInstalledPlugins(&f)
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
	raw := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			raw = make(map[string]json.RawMessage)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("save installed plugins: read existing: %w", err)
	}
	versionRaw, err := json.Marshal(f.Version)
	if err != nil {
		return err
	}
	pluginsRaw, err := json.Marshal(f.Plugins)
	if err != nil {
		return err
	}
	raw["version"] = versionRaw
	raw["plugins"] = pluginsRaw
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writePluginFileAtomic(path, append(data, '\n'), 0o644)
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
		if err := ensureMarketplaceConfigured(ctx, marketplaceName); err != nil {
			return nil, fmt.Errorf("install: load marketplaces: %w", err)
		}
		// Don't git pull — it can fail on detached/rebased repos. Just look up manifest.
		srcDir = MarketplacePluginDir(marketplaceName, pluginName)
		if srcDir == "" {
			if localDir := marketplacePluginSourceDir(marketplaceName, pluginName); localDir != "" {
				srcDir = localDir
			}
		}
		if srcDir == "" {
			// Plugin may be "external" — defined in marketplace.json with its own source URL.
			// Try to clone it from the marketplace manifest's source field.
			clonedDir, err := cloneExternalPlugin(ctx, marketplaceName, pluginName, pluginsDir())
			if err != nil {
				return nil, fmt.Errorf("install: plugin %q not found in marketplace %q: %w", pluginName, marketplaceName, err)
			}
			srcDir = clonedDir
		}
	}

	// Read the plugin manifest to get version.
	p, err := loadPlugin(srcDir)
	if err != nil {
		p, err = loadMarketplacePluginFallback(marketplaceName, pluginName, srcDir)
		if err != nil {
			return nil, fmt.Errorf("install: load plugin manifest: %w", err)
		}
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
	if err := writePluginManifestIfMissing(cachePath, p.Manifest); err != nil {
		return nil, fmt.Errorf("install: cache plugin manifest: %w", err)
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

	if err := updateInstalledPlugins(func(installed *InstalledPluginsV2) {
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
	}); err != nil {
		return nil, err
	}

	// Mark enabled in settings.
	if err := settings.SetPluginEnabled(pluginID, true); err != nil {
		return nil, fmt.Errorf("install: update settings: %w", err)
	}

	return &entry, nil
}

func updateInstalledPlugins(fn func(*InstalledPluginsV2)) error {
	installedPluginsMu.Lock()
	defer installedPluginsMu.Unlock()
	installed, err := LoadInstalledPlugins()
	if err != nil {
		return err
	}
	fn(installed)
	return saveInstalledPlugins(installed)
}

func writePluginManifestIfMissing(pluginDir string, manifest Manifest) error {
	manifestPath := filepath.Join(pluginDir, ".claude-plugin", "plugin.json")
	if _, err := os.Stat(manifestPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(data, '\n'), 0o644)
}

// Uninstall removes a plugin installation from the given scope.
func Uninstall(pluginSpec, scope, cwd string) error {
	if scope == "" {
		scope = "user"
	}
	pluginName, marketplaceName := parsePluginSpec(pluginSpec)
	pluginID := pluginName + "@" + marketplaceName

	var removed *PluginInstallationEntry
	var hasKept bool
	if err := updateInstalledPlugins(func(installed *InstalledPluginsV2) {
		entries := installed.Plugins[pluginID]
		var kept []PluginInstallationEntry
		for _, e := range entries {
			match := e.Scope == scope
			if scope == "project" || scope == "local" {
				match = match && e.ProjectPath == cwd
			}
			if match && removed == nil {
				copyEntry := e
				removed = &copyEntry
			} else {
				kept = append(kept, e)
			}
		}
		hasKept = len(kept) > 0
		if len(kept) == 0 {
			delete(installed.Plugins, pluginID)
		} else {
			installed.Plugins[pluginID] = kept
		}
	}); err != nil {
		return err
	}
	if removed == nil {
		return fmt.Errorf("plugin %q not installed at scope %q", pluginID, scope)
	}
	if !hasKept && removed.InstallPath != "" {
		_ = os.RemoveAll(removed.InstallPath)
	}
	if !hasKept {
		return settings.RemovePlugin(pluginID)
	}
	return settings.SetPluginEnabled(pluginID, false)
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
// Symlinks are skipped to prevent zip-slip / symlink-following attacks.
// Paths that escape dst via ".." components are also skipped.
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

		// Zip-slip guard: dstPath must remain inside dst.
		rel, err := filepath.Rel(dst, dstPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}

		// Use Lstat so we see the symlink itself, not its target.
		info, err := os.Lstat(srcPath)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Skip symlinks — they could point outside the plugin directory.
			continue
		}

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
