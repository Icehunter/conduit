package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLoadAllFromExternalSource(t *testing.T) {
	// Test loadPlugin against the official plugins repo if it exists on this machine.
	const officialPlugins = "/Volumes/Engineering/Icehunter/claude-plugins-official/plugins/code-review"
	p, err := loadPlugin(officialPlugins)
	if err != nil {
		t.Skipf("official plugins not available at %s: %v", officialPlugins, err)
	}
	if p.Manifest.Name == "" {
		t.Error("expected non-empty plugin name")
	}
	if len(p.Commands) == 0 {
		t.Error("expected at least one command from code-review plugin")
	}
	t.Logf("loaded plugin %q with %d commands", p.Manifest.Name, len(p.Commands))
}

func TestExtractFrontmatter(t *testing.T) {
	content := `---
description: "Test command"
allowed-tools: ["Bash", "Read"]
---
# Body text here
`
	fm, body, ok := extractFrontmatter(content)
	if !ok {
		t.Fatal("expected frontmatter to be found")
	}
	if fm["description"] != "Test command" {
		t.Errorf("description = %q, want %q", fm["description"], "Test command")
	}
	if body != "# Body text here\n" {
		t.Errorf("body = %q", body)
	}
}

func TestNormalizeServerNameInPlugin(t *testing.T) {
	// Test parseAllowedTools JSON array form.
	tools := parseAllowedTools(`["Bash", "Read", "Glob"]`)
	if len(tools) != 3 {
		t.Errorf("expected 3 tools, got %d: %v", len(tools), tools)
	}
	// CSV form.
	tools2 := parseAllowedTools("Bash, Read, Glob")
	if len(tools2) != 3 {
		t.Errorf("expected 3 tools from CSV, got %d: %v", len(tools2), tools2)
	}
}

func TestSaveInstalledPlugins_PreservesUnknownFieldsAndRepairsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", dir)
	path := installedPluginsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	before := []byte(`{"version":2,"plugins":{},"external":{"keep":true}}`)
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}

	err := saveInstalledPlugins(&InstalledPluginsV2{
		Plugins: map[string][]PluginInstallationEntry{
			"demo@local": {{
				Scope:       "user",
				InstallPath: "/tmp/demo",
				InstalledAt: "2026-05-01T12:00:00Z",
			}},
		},
	})
	if err != nil {
		t.Fatalf("saveInstalledPlugins: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(after, &raw); err != nil {
		t.Fatal(err)
	}
	var external map[string]bool
	if err := json.Unmarshal(raw["external"], &external); err != nil {
		t.Fatal(err)
	}
	if !external["keep"] {
		t.Fatalf("external field not preserved: %s", raw["external"])
	}

	bad := []byte(`{"version":`)
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveInstalledPlugins(&InstalledPluginsV2{
		Plugins: map[string][]PluginInstallationEntry{},
	}); err != nil {
		t.Fatalf("saveInstalledPlugins should repair invalid existing JSON: %v", err)
	}
	repaired, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(repaired, &raw); err != nil {
		t.Fatalf("repaired registry is invalid: %v\n%s", err, repaired)
	}
}

func TestLoadInstalledPluginsSalvagesLeadingJSONObject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", dir)
	path := installedPluginsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	corrupt := `{"version":2,"plugins":{"demo@market":[{"scope":"user","installPath":"/tmp/demo","installedAt":"2026-05-07T00:00:00Z"}]}}` + "\n-demo"
	if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, err := LoadInstalledPlugins()
	if err != nil {
		t.Fatalf("LoadInstalledPlugins: %v", err)
	}
	if len(installed.Plugins["demo@market"]) != 1 {
		t.Fatalf("salvaged plugins = %#v", installed.Plugins)
	}
	cleaned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(cleaned, &InstalledPluginsV2{}); err != nil {
		t.Fatalf("registry was not cleaned: %v\n%s", err, cleaned)
	}
}

func TestInstallMarketplaceLocalSourceWithoutPluginManifest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))
	t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", dir)
	marketplaceDir := filepath.Join(dir, "marketplaces", "local-market")
	pluginDir := filepath.Join(marketplaceDir, "plugins", "rust-analyzer-lsp")
	if err := os.MkdirAll(filepath.Join(marketplaceDir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "README.md"), []byte("# Rust LSP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"local-market","plugins":[{"name":"rust-analyzer-lsp","description":"Rust language server","version":"1.0.0","source":"./plugins/rust-analyzer-lsp"}]}`
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".claude-plugin", "marketplace.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	known := map[string]MarketplaceEntry{
		"local-market": {
			Source:          MarketplaceSource{Source: "directory", Path: marketplaceDir},
			InstallLocation: marketplaceDir,
		},
	}
	if err := saveKnownMarketplaces(known); err != nil {
		t.Fatal(err)
	}

	entry, err := Install(t.Context(), "rust-analyzer-lsp@local-market", "user", "")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	manifestPath := filepath.Join(entry.InstallPath, ".claude-plugin", "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("synthetic plugin manifest missing: %v", err)
	}
	var got Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("synthetic plugin manifest invalid: %v", err)
	}
	if got.Name != "rust-analyzer-lsp" || got.Version != "1.0.0" {
		t.Fatalf("manifest = %#v", got)
	}
}

func TestPluginStorageImportsLegacyClaudePluginsToConduit(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	conduitDir := filepath.Join(t.TempDir(), ".conduit")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)
	t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", "")
	t.Setenv("CONDUIT_PLUGIN_CACHE_DIR", "")

	legacyPluginRoot := filepath.Join(claudeDir, "plugins")
	legacyInstall := filepath.Join(legacyPluginRoot, "cache", "market", "demo", "1.0.0")
	if err := os.MkdirAll(filepath.Join(legacyInstall, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyInstall, ".claude-plugin", "plugin.json"), []byte(`{"name":"demo","description":"Demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	installed := InstalledPluginsV2{
		Version: 2,
		Plugins: map[string][]PluginInstallationEntry{
			"demo@market": {{
				Scope:       "user",
				InstallPath: legacyInstall,
				Version:     "1.0.0",
				InstalledAt: "2026-05-06T00:00:00Z",
			}},
		},
	}
	installedData, err := json.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyPluginRoot, "installed_plugins.json"), installedData, 0o644); err != nil {
		t.Fatal(err)
	}
	knownData := []byte(`{"market":{"source":{"source":"directory","path":"/tmp/market"},"installLocation":` + strconv.Quote(filepath.Join(legacyPluginRoot, "marketplaces", "market")) + `,"lastUpdated":"2026-05-06T00:00:00Z"}}`)
	if err := os.MkdirAll(filepath.Join(legacyPluginRoot, "marketplaces", "market"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyPluginRoot, "known_marketplaces.json"), knownData, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadInstalledPlugins()
	if err != nil {
		t.Fatalf("LoadInstalledPlugins: %v", err)
	}
	conduitInstall := filepath.Join(conduitDir, "plugins", "cache", "market", "demo", "1.0.0")
	if got.Plugins["demo@market"][0].InstallPath != conduitInstall {
		t.Fatalf("installPath = %q, want %q", got.Plugins["demo@market"][0].InstallPath, conduitInstall)
	}
	if _, err := os.Stat(filepath.Join(conduitInstall, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("plugin files were not copied into conduit storage: %v", err)
	}
	known, err := LoadKnownMarketplaces()
	if err != nil {
		t.Fatalf("LoadKnownMarketplaces: %v", err)
	}
	wantMarketplace := filepath.Join(conduitDir, "plugins", "marketplaces", "market")
	if known["market"].InstallLocation != wantMarketplace {
		t.Fatalf("marketplace location = %q, want %q", known["market"].InstallLocation, wantMarketplace)
	}
}

func TestLoadKnownMarketplacesSelfHealsOfficialMarketplace(t *testing.T) {
	conduitDir := filepath.Join(t.TempDir(), ".conduit")
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)
	t.Setenv("CONDUIT_PLUGIN_CACHE_DIR", "")
	t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", "")

	officialDir := filepath.Join(conduitDir, "plugins", "marketplaces", defaultMarketplaceName)
	if err := os.MkdirAll(filepath.Join(officialDir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(officialDir, ".claude-plugin", "marketplace.json"), []byte(`{"name":"claude-plugins-official","plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conduitDir, "plugins", "known_marketplaces.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	known, err := LoadKnownMarketplaces()
	if err != nil {
		t.Fatalf("LoadKnownMarketplaces: %v", err)
	}
	entry, ok := known[defaultMarketplaceName]
	if !ok {
		t.Fatalf("default marketplace missing: %#v", known)
	}
	if entry.InstallLocation != officialDir {
		t.Fatalf("InstallLocation = %q, want %q", entry.InstallLocation, officialDir)
	}
	if entry.Source.Repo != defaultMarketplaceSource {
		t.Fatalf("Source.Repo = %q, want %q", entry.Source.Repo, defaultMarketplaceSource)
	}
}
