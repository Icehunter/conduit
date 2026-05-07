package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/icehunter/conduit/internal/settings"
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

// --- Plugin skills, agents, hooks loading tests ---

// makeTestPlugin builds a minimal plugin directory with the given name and
// optional subdirectories, returning the dir path and a cleanup func.
func makeTestPlugin(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := `{"name":"` + name + `","description":"test plugin"}`
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadPlugin_LoadsSkills(t *testing.T) {
	dir := makeTestPlugin(t, "myplugin")

	skillDir := filepath.Join(dir, "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: my-skill\ndescription: Does something cool\ntools: Read, Bash\n---\n# Skill body\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := loadPlugin(dir)
	if err != nil {
		t.Fatalf("loadPlugin: %v", err)
	}
	if len(p.Skills) != 1 {
		t.Fatalf("Skills count = %d; want 1", len(p.Skills))
	}
	sk := p.Skills[0]
	if sk.Name != "my-skill" {
		t.Errorf("Name = %q; want my-skill", sk.Name)
	}
	if sk.QualifiedName != "myplugin:my-skill" {
		t.Errorf("QualifiedName = %q; want myplugin:my-skill", sk.QualifiedName)
	}
	if sk.Description != "Does something cool" {
		t.Errorf("Description = %q; want %q", sk.Description, "Does something cool")
	}
	if len(sk.Tools) != 2 {
		t.Errorf("Tools = %v; want [Read Bash]", sk.Tools)
	}
	if sk.Body != "# Skill body\n" {
		t.Errorf("Body = %q", sk.Body)
	}
}

func TestLoadPlugin_LoadsAgents(t *testing.T) {
	dir := makeTestPlugin(t, "myplugin")

	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: reviewer\ndescription: Reviews code\nmodel: opus\ntools: Read, Grep, Glob\n---\n# Agent body\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := loadPlugin(dir)
	if err != nil {
		t.Fatalf("loadPlugin: %v", err)
	}
	if len(p.Agents) != 1 {
		t.Fatalf("Agents count = %d; want 1", len(p.Agents))
	}
	ag := p.Agents[0]
	if ag.Name != "reviewer" {
		t.Errorf("Name = %q; want reviewer", ag.Name)
	}
	if ag.QualifiedName != "myplugin:reviewer" {
		t.Errorf("QualifiedName = %q", ag.QualifiedName)
	}
	if ag.Description != "Reviews code" {
		t.Errorf("Description = %q", ag.Description)
	}
	if ag.Model != "opus" {
		t.Errorf("Model = %q; want opus", ag.Model)
	}
	if len(ag.Tools) != 3 {
		t.Errorf("Tools = %v; want [Read Grep Glob]", ag.Tools)
	}
	if ag.Body != "# Agent body\n" {
		t.Errorf("Body = %q", ag.Body)
	}
}

func TestLoadPlugin_LoadsHooks(t *testing.T) {
	dir := makeTestPlugin(t, "myplugin")

	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksJSON := `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"echo hello"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := loadPlugin(dir)
	if err != nil {
		t.Fatalf("loadPlugin: %v", err)
	}
	if len(p.Hooks.SessionStart) != 1 {
		t.Fatalf("SessionStart matchers = %d; want 1", len(p.Hooks.SessionStart))
	}
	m := p.Hooks.SessionStart[0]
	if m.PluginRoot != dir {
		t.Errorf("PluginRoot = %q; want %q", m.PluginRoot, dir)
	}
	if len(m.Hooks) != 1 || m.Hooks[0].Command != "echo hello" {
		t.Errorf("Hook = %+v", m.Hooks)
	}
}

func TestSkillLoader_PluginSkillFound(t *testing.T) {
	dir := makeTestPlugin(t, "sp")
	skillDir := filepath.Join(dir, "skills", "brainstorm")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: brainstorm\ndescription: Brainstorm stuff\n---\n# Body\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, _ := loadPlugin(dir)
	loader := NewSkillLoader([]*Plugin{p})

	tests := []struct {
		query string
	}{
		{"brainstorm"},
		{"sp:brainstorm"},
		{"/brainstorm"},
		{"/sp:brainstorm"},
	}
	for _, tt := range tests {
		cmd := loader.FindCommand(tt.query)
		if cmd == nil {
			t.Errorf("FindCommand(%q) = nil; want hit", tt.query)
			continue
		}
		if cmd.Body != "# Body\n" {
			t.Errorf("FindCommand(%q).Body = %q", tt.query, cmd.Body)
		}
	}
}

func TestAgentRegistry_FindAgent(t *testing.T) {
	dir := makeTestPlugin(t, "toolkit")
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(agentsDir, "reviewer.md"),
		[]byte("---\ndescription: Reviewer\n---\n# Body\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	p, _ := loadPlugin(dir)
	reg := NewAgentRegistry([]*Plugin{p})

	if reg.FindAgent("reviewer") == nil {
		t.Error("FindAgent(bare name) = nil")
	}
	if reg.FindAgent("toolkit:reviewer") == nil {
		t.Error("FindAgent(qualified) = nil")
	}
	if reg.FindAgent("missing") != nil {
		t.Error("FindAgent(missing) should return nil")
	}
}

func TestMergeHooksFrom_CombinesPluginAndBase(t *testing.T) {
	dir := makeTestPlugin(t, "sp")
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksJSON := `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"plugin-hook"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	p, _ := loadPlugin(dir)

	base := &settings.HooksSettings{
		SessionStart: []settings.HookMatcher{{
			Matcher: "",
			Hooks:   []settings.Hook{{Type: "command", Command: "base-hook"}},
		}},
	}

	merged := MergeHooksFrom([]*Plugin{p}, base)
	if len(merged.SessionStart) != 2 {
		t.Fatalf("SessionStart matchers = %d; want 2", len(merged.SessionStart))
	}
	// Base hook comes first.
	if merged.SessionStart[0].Hooks[0].Command != "base-hook" {
		t.Errorf("first hook = %q; want base-hook", merged.SessionStart[0].Hooks[0].Command)
	}
	if merged.SessionStart[1].Hooks[0].Command != "plugin-hook" {
		t.Errorf("second hook = %q; want plugin-hook", merged.SessionStart[1].Hooks[0].Command)
	}
	if merged.SessionStart[1].PluginRoot != dir {
		t.Errorf("PluginRoot = %q; want %q", merged.SessionStart[1].PluginRoot, dir)
	}
}
