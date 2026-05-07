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

func TestSaveInstalledPlugins_PreservesUnknownFieldsAndRejectsInvalidJSON(t *testing.T) {
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
	if err := saveInstalledPlugins(&InstalledPluginsV2{}); err == nil {
		t.Fatal("saveInstalledPlugins should fail on invalid existing JSON")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(bad) {
		t.Fatalf("invalid plugin registry was overwritten: %q", unchanged)
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
