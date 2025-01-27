package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helpers

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// isolateHome creates a temp dir, sets HOME to it, and sets CLAUDE_CONFIG_DIR
// to home/.claude. This ensures tests work on all platforms: on Windows,
// os.UserHomeDir() ignores HOME but claudemd.Load respects CLAUDE_CONFIG_DIR.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

// --- Load order ---

func TestLoad_NoFiles_ReturnsEmpty(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files; got %d", len(files))
	}
}

func TestLoad_UserGlobal(t *testing.T) {
	cwd := t.TempDir()
	home := isolateHome(t)
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# Global instructions")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file; got %d", len(files))
	}
	if !strings.Contains(files[0].Content, "Global instructions") {
		t.Errorf("content missing: %q", files[0].Content)
	}
	if files[0].Type != TypeUser {
		t.Errorf("expected TypeUser; got %v", files[0].Type)
	}
}

func TestLoad_ProjectCLAUDEmd(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "# Project instructions")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file; got %d", len(files))
	}
	if files[0].Type != TypeProject {
		t.Errorf("expected TypeProject; got %v", files[0].Type)
	}
}

func TestLoad_DotClaudeCLAUDEmd(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)
	writeFile(t, filepath.Join(cwd, ".claude", "CLAUDE.md"), "# Dot-claude project instructions")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, f := range files {
		if strings.Contains(f.Content, "Dot-claude project instructions") {
			found = true
		}
	}
	if !found {
		t.Error(".claude/CLAUDE.md not loaded")
	}
}

func TestLoad_RulesDir(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)
	writeFile(t, filepath.Join(cwd, ".claude", "rules", "no-yolo.md"), "# No yolo commits")
	writeFile(t, filepath.Join(cwd, ".claude", "rules", "style.md"), "# Use tabs")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 rule files; got %d", len(files))
	}
}

func TestLoad_LocalCLAUDEmd(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)
	writeFile(t, filepath.Join(cwd, "CLAUDE.local.md"), "# Local private instructions")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file; got %d", len(files))
	}
	if files[0].Type != TypeLocal {
		t.Errorf("expected TypeLocal; got %v", files[0].Type)
	}
}

func TestLoad_ParentDirWalk(t *testing.T) {
	// cwd/sub/ — CLAUDE.md in cwd should be found when cwd is the parent
	parent := t.TempDir()
	sub := filepath.Join(parent, "sub")
	os.MkdirAll(sub, 0o755)
	isolateHome(t)
	writeFile(t, filepath.Join(parent, "CLAUDE.md"), "# Parent instructions")

	files, err := Load(sub)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, f := range files {
		if strings.Contains(f.Content, "Parent instructions") {
			found = true
		}
	}
	if !found {
		t.Error("parent CLAUDE.md not found via directory walk")
	}
}

func TestLoad_PriorityOrder(t *testing.T) {
	// Files should be ordered: global first, project last (closer = higher priority = later)
	home := isolateHome(t)
	cwd := t.TempDir()

	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "GLOBAL")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "PROJECT")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("expected at least 2 files; got %d", len(files))
	}
	// Global must come before project
	globalIdx, projectIdx := -1, -1
	for i, f := range files {
		if strings.Contains(f.Content, "GLOBAL") {
			globalIdx = i
		}
		if strings.Contains(f.Content, "PROJECT") {
			projectIdx = i
		}
	}
	if globalIdx == -1 || projectIdx == -1 {
		t.Fatal("missing global or project file")
	}
	if globalIdx >= projectIdx {
		t.Errorf("global (%d) should come before project (%d)", globalIdx, projectIdx)
	}
}

// --- @include ---

func TestLoad_AtInclude_RelativePath(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)

	writeFile(t, filepath.Join(cwd, "extra.md"), "# Extra content")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "@extra.md\n# Main")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// extra.md should be loaded as a separate entry before CLAUDE.md
	found := false
	for _, f := range files {
		if strings.Contains(f.Content, "Extra content") {
			found = true
		}
	}
	if !found {
		t.Error("@include file not loaded")
	}
}

func TestLoad_AtInclude_AbsolutePath(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)

	extra := filepath.Join(cwd, "absolute.md")
	writeFile(t, extra, "# Absolute")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "@"+extra)

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, f := range files {
		if strings.Contains(f.Content, "Absolute") {
			found = true
		}
	}
	if !found {
		t.Error("@include absolute path not loaded")
	}
}

func TestLoad_AtInclude_CircularPrevented(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)

	// a.md includes b.md which includes a.md
	writeFile(t, filepath.Join(cwd, "a.md"), "@b.md\n# A")
	writeFile(t, filepath.Join(cwd, "b.md"), "@a.md\n# B")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "@a.md")

	// Must not hang or stack overflow
	_, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load should handle circular includes gracefully: %v", err)
	}
}

func TestLoad_AtInclude_NonExistentSilentlyIgnored(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)

	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "@nonexistent.md\n# Main")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Main file should still load
	found := false
	for _, f := range files {
		if strings.Contains(f.Content, "Main") {
			found = true
		}
	}
	if !found {
		t.Error("main file should load even if @include target is missing")
	}
}

// --- AGENTS.md ---

func TestLoad_AgentsMd(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "# Agents convention")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got *File
	for i := range files {
		if strings.Contains(files[i].Content, "Agents convention") {
			got = &files[i]
		}
	}
	if got == nil {
		t.Fatal("AGENTS.md not loaded")
	}
	if got.Type != TypeAgents {
		t.Errorf("expected TypeAgents; got %v", got.Type)
	}
}

func TestLoad_AgentsMd_WalksUp(t *testing.T) {
	parent := t.TempDir()
	sub := filepath.Join(parent, "sub", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	isolateHome(t)
	writeFile(t, filepath.Join(parent, "AGENTS.md"), "# Parent agents")

	files, err := Load(sub)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, f := range files {
		if strings.Contains(f.Content, "Parent agents") {
			found = true
		}
	}
	if !found {
		t.Error("parent AGENTS.md not found via walk-up")
	}
}

// --- Copilot ---

func TestLoad_CopilotInstructions_RequiresGitRoot(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)
	// No .git → copilot file should be ignored even if present.
	writeFile(t, filepath.Join(cwd, ".github", "copilot-instructions.md"), "# copilot")

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, f := range files {
		if f.Type == TypeCopilot {
			t.Error("copilot file loaded without .git root")
		}
	}
}

func TestLoad_CopilotInstructions_AtGitRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	isolateHome(t)
	writeFile(t, filepath.Join(root, ".github", "copilot-instructions.md"), "# copilot rules")

	files, err := Load(sub)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got *File
	for i := range files {
		if files[i].Type == TypeCopilot {
			got = &files[i]
		}
	}
	if got == nil {
		t.Fatal("copilot-instructions.md not loaded from git root")
	}
	if !strings.Contains(got.Content, "copilot rules") {
		t.Errorf("unexpected content: %q", got.Content)
	}
}

// --- Cursor rules ---

func TestLoad_CursorRules_AlwaysApply(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	isolateHome(t)
	writeFile(t, filepath.Join(root, ".cursor", "rules", "always.mdc"),
		"---\ndescription: foo\nalwaysApply: true\n---\n# Always content\n")

	files, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, f := range files {
		if f.Type == TypeCursor && strings.Contains(f.Content, "Always content") {
			found = true
			if strings.Contains(f.Content, "alwaysApply") {
				t.Error("frontmatter leaked into body")
			}
		}
	}
	if !found {
		t.Error("alwaysApply cursor rule not loaded")
	}
}

func TestLoad_CursorRules_NoFrontmatter_LoadsAlways(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	isolateHome(t)
	writeFile(t, filepath.Join(root, ".cursor", "rules", "bare.mdc"), "# Bare content\n")

	files, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, f := range files {
		if f.Type == TypeCursor && strings.Contains(f.Content, "Bare content") {
			found = true
		}
	}
	if !found {
		t.Error("bare cursor rule not loaded")
	}
}

func TestLoad_CursorRules_GlobsScopeToCwd(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src", "components")
	docsDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	isolateHome(t)
	writeFile(t, filepath.Join(root, ".cursor", "rules", "src-only.mdc"),
		"---\nglobs: src/**/*.ts\n---\n# Src rule\n")
	writeFile(t, filepath.Join(root, ".cursor", "rules", "docs-only.mdc"),
		"---\nglobs: docs/**\n---\n# Docs rule\n")

	// From src/components: src rule applies, docs rule does not.
	files, err := Load(srcDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var sawSrc, sawDocs bool
	for _, f := range files {
		if f.Type != TypeCursor {
			continue
		}
		if strings.Contains(f.Content, "Src rule") {
			sawSrc = true
		}
		if strings.Contains(f.Content, "Docs rule") {
			sawDocs = true
		}
	}
	if !sawSrc {
		t.Error("src-scoped cursor rule should apply when cwd is under src/")
	}
	if sawDocs {
		t.Error("docs-scoped cursor rule should NOT apply when cwd is under src/")
	}
}

func TestSplitCursorFrontmatter_HandlesQuotedGlobs(t *testing.T) {
	body, fm := splitCursorFrontmatter("---\nglobs: \"src/**/*.ts\", 'docs/**'\nalwaysApply: false\n---\nbody\n")
	if body != "body\n" {
		t.Errorf("body = %q", body)
	}
	if len(fm.globs) != 2 || fm.globs[0] != "src/**/*.ts" || fm.globs[1] != "docs/**" {
		t.Errorf("globs = %v", fm.globs)
	}
	if fm.alwaysApply {
		t.Error("alwaysApply should be false")
	}
}

func TestMatchCursorGlob(t *testing.T) {
	tests := []struct {
		glob, rel string
		want      bool
	}{
		{"src/**/*.ts", "src", true},
		{"src/**/*.ts", "src/components", true},
		{"src/**/*.ts", "docs", false},
		{"docs/**", "docs/foo", true},
		{"docs/**", "src", false},
		{"**", ".", true},
		{"**/*", "anywhere", true},
		{"./", ".", true},
		{"foo", "foo", true},
		{"foo", "foo/bar", true},
		{"foo", "bar", false},
	}
	for _, tt := range tests {
		got := matchCursorGlob(tt.glob, tt.rel)
		if got != tt.want {
			t.Errorf("matchCursorGlob(%q, %q) = %v; want %v", tt.glob, tt.rel, got, tt.want)
		}
	}
}

func TestBuildPrompt_Empty(t *testing.T) {
	p := BuildPrompt(nil)
	if p != "" {
		t.Errorf("expected empty prompt for no files; got %q", p)
	}
}

func TestBuildPrompt_IncludesContent(t *testing.T) {
	files := []File{
		{Path: "/foo/CLAUDE.md", Content: "# Do things", Type: TypeProject},
	}
	p := BuildPrompt(files)
	if !strings.Contains(p, "Do things") {
		t.Errorf("prompt should contain file content; got %q", p)
	}
	if !strings.Contains(p, "IMPORTANT") {
		t.Errorf("prompt should contain instruction header")
	}
}

func TestBuildPrompt_MultipleFiles(t *testing.T) {
	files := []File{
		{Path: "/home/.claude/CLAUDE.md", Content: "global rule", Type: TypeUser},
		{Path: "/proj/CLAUDE.md", Content: "project rule", Type: TypeProject},
	}
	p := BuildPrompt(files)
	if !strings.Contains(p, "global rule") || !strings.Contains(p, "project rule") {
		t.Errorf("prompt should contain all file contents: %q", p)
	}
}

// --- MaxCharCount ---

func TestLoad_TruncatesLargeFiles(t *testing.T) {
	cwd := t.TempDir()
	isolateHome(t)

	// Write a file larger than MaxCharCount
	large := strings.Repeat("x", MaxCharCount+1000)
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), large)

	files, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, f := range files {
		if len(f.Content) > MaxCharCount+100 { // small buffer for truncation message
			t.Errorf("file content exceeds MaxCharCount: %d", len(f.Content))
		}
	}
}
