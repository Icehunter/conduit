package outputstyles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	styles, err := loadDir(dir)
	if err != nil {
		t.Fatalf("loadDir: %v", err)
	}
	if len(styles) != 0 {
		t.Errorf("expected 0 styles from empty dir; got %d", len(styles))
	}
}

// TestLoadAll_IncludesBuiltins verifies that the picker is never empty —
// "default", "Explanatory", "Learning" always show up even with no
// user/project styles on disk.
func TestLoadAll_IncludesBuiltins(t *testing.T) {
	// Use a tempdir as cwd and unset HOME so neither user nor project dir
	// exists; only built-ins should remain.
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	styles, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	want := map[string]bool{"default": false, "Explanatory": false, "Learning": false}
	for _, s := range styles {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("built-in style %q missing from LoadAll", name)
		}
	}
}

// TestLoadAll_ProjectOverridesBuiltin ensures a user-defined style with
// the same name as a built-in wins.
func TestLoadAll_ProjectOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	stylesDir := filepath.Join(dir, ".claude", "output-styles")
	if err := os.MkdirAll(stylesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(stylesDir, "Explanatory.md"), "---\ndescription: my override\n---\noverridden")

	styles, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	for _, s := range styles {
		if s.Name == "Explanatory" {
			if s.Description != "my override" {
				t.Errorf("project Explanatory should override built-in; got desc %q", s.Description)
			}
			return
		}
	}
	t.Fatal("Explanatory missing")
}

func TestLoad_PlainMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "concise.md"), "Be concise. Use bullet points.")
	styles, err := loadDir(dir)
	if err != nil {
		t.Fatalf("loadDir: %v", err)
	}
	if len(styles) != 1 {
		t.Fatalf("expected 1 style; got %d", len(styles))
	}
	s := styles[0]
	if s.Name != "concise" {
		t.Errorf("name = %q, want %q", s.Name, "concise")
	}
	if s.Prompt != "Be concise. Use bullet points." {
		t.Errorf("prompt = %q", s.Prompt)
	}
}

func TestLoad_WithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "verbose.md"), `---
name: Verbose Mode
description: Explain everything in detail
keep-coding-instructions: true
---

Always provide thorough explanations with examples.`)
	styles, err := loadDir(dir)
	if err != nil {
		t.Fatalf("loadDir: %v", err)
	}
	if len(styles) != 1 {
		t.Fatalf("expected 1 style; got %d", len(styles))
	}
	s := styles[0]
	if s.Name != "Verbose Mode" {
		t.Errorf("name = %q, want %q", s.Name, "Verbose Mode")
	}
	if s.Description != "Explain everything in detail" {
		t.Errorf("description = %q", s.Description)
	}
	if !s.KeepCodingInstructions {
		t.Error("expected KeepCodingInstructions=true")
	}
	if s.Prompt != "Always provide thorough explanations with examples." {
		t.Errorf("prompt = %q", s.Prompt)
	}
}

func TestLoad_NonMdIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "style.md"), "some style")
	writeFile(t, filepath.Join(dir, "ignore.txt"), "not a style")
	writeFile(t, filepath.Join(dir, "ignore.json"), `{"not": "style"}`)
	styles, err := loadDir(dir)
	if err != nil {
		t.Fatalf("loadDir: %v", err)
	}
	if len(styles) != 1 {
		t.Errorf("expected 1 style (only .md); got %d", len(styles))
	}
}

func TestLoad_FrontmatterFalseKeepCoding(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "terse.md"), `---
keep-coding-instructions: false
---
Be terse.`)
	styles, _ := loadDir(dir)
	if len(styles) == 1 && styles[0].KeepCodingInstructions {
		t.Error("expected KeepCodingInstructions=false")
	}
}

func TestLoadAll_MergesUserAndProject(t *testing.T) {
	home := t.TempDir()
	userDir := filepath.Join(home, ".claude", "output-styles")
	os.MkdirAll(userDir, 0o755)
	writeFile(t, filepath.Join(userDir, "user-style.md"), "user style")

	cwd := t.TempDir()
	projDir := filepath.Join(cwd, ".claude", "output-styles")
	os.MkdirAll(projDir, 0o755)
	writeFile(t, filepath.Join(projDir, "proj-style.md"), "project style")

	t.Setenv("HOME", home)
	styles, err := LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	names := map[string]bool{}
	for _, s := range styles {
		names[s.Name] = true
	}
	if !names["user-style"] {
		t.Error("expected user-style")
	}
	if !names["proj-style"] {
		t.Error("expected proj-style")
	}
}

func TestLoadAll_ProjectOverridesUser(t *testing.T) {
	home := t.TempDir()
	userDir := filepath.Join(home, ".claude", "output-styles")
	os.MkdirAll(userDir, 0o755)
	writeFile(t, filepath.Join(userDir, "shared.md"), "user version")

	cwd := t.TempDir()
	projDir := filepath.Join(cwd, ".claude", "output-styles")
	os.MkdirAll(projDir, 0o755)
	writeFile(t, filepath.Join(projDir, "shared.md"), "project version")

	t.Setenv("HOME", home)
	styles, _ := LoadAll(cwd)
	for _, s := range styles {
		if s.Name == "shared" && s.Prompt != "project version" {
			t.Errorf("project should override user; got %q", s.Prompt)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
