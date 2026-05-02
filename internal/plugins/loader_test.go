package plugins

import (
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
