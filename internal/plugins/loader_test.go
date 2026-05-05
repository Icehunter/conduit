package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
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
