package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a test helper that creates parent dirs and writes data.
func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func TestParseFSFrontmatter_Present(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Does a thing\n---\n\nBody here.\n"
	fm, body, ok := parseFSFrontmatter(content)
	if !ok {
		t.Fatal("expected frontmatter to be found")
	}
	if fm["name"] != "my-skill" {
		t.Errorf("name = %q; want %q", fm["name"], "my-skill")
	}
	if fm["description"] != "Does a thing" {
		t.Errorf("description = %q; want %q", fm["description"], "Does a thing")
	}
	if body != "Body here.\n" {
		t.Errorf("body = %q; want %q", body, "Body here.\n")
	}
}

func TestParseFSFrontmatter_Absent(t *testing.T) {
	content := "# Just a body\n\nNo frontmatter here.\n"
	fm, body, ok := parseFSFrontmatter(content)
	if ok {
		t.Fatalf("expected no frontmatter; got fm=%v", fm)
	}
	if body != content {
		t.Errorf("body should be unchanged; got %q", body)
	}
}

func TestParseFSFrontmatter_NestedConduit(t *testing.T) {
	content := "---\nname: tagged\ndescription: Tagged skill\nmetadata:\n  conduit:\n    tags: [go, testing]\n    alwaysOn: false\n---\nBody.\n"
	fm, _, ok := parseFSFrontmatter(content)
	if !ok {
		t.Fatal("expected frontmatter")
	}
	if fm["tags"] != "[go, testing]" {
		t.Errorf("tags = %q; want %q", fm["tags"], "[go, testing]")
	}
}

func TestParseFSTags(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"[go, testing]", []string{"go", "testing"}},
		{"[single]", []string{"single"}},
		{"bare", []string{"bare"}},
		{"", nil},
	}
	for _, tt := range tests {
		got := parseFSTags(tt.raw)
		if len(got) != len(tt.want) {
			t.Errorf("parseFSTags(%q) = %v; want %v", tt.raw, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseFSTags(%q)[%d] = %q; want %q", tt.raw, i, got[i], tt.want[i])
			}
		}
	}
}

func TestDiscoverFS_BasicDiscovery(t *testing.T) {
	// Build a fake cwd with one skill.
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".claude", "skills", "my-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"),
		"---\nname: my-skill\ndescription: A test skill\n---\n\nDo the thing.\n")

	// Point conduit/claude dirs at empty temp dirs so they don't pick up real skills.
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	skills := DiscoverFS(cwd)
	if len(skills) != 1 {
		t.Fatalf("DiscoverFS returned %d skills; want 1", len(skills))
	}
	sk := skills[0]
	if sk.Name != "my-skill" {
		t.Errorf("Name = %q; want %q", sk.Name, "my-skill")
	}
	if sk.Description != "A test skill" {
		t.Errorf("Description = %q; want %q", sk.Description, "A test skill")
	}
	if sk.Body != "Do the thing.\n" {
		t.Errorf("Body = %q; want %q", sk.Body, "Do the thing.\n")
	}
}

func TestDiscoverFS_LaterDirOverrides(t *testing.T) {
	conduitDir := t.TempDir()
	cwd := t.TempDir()

	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	// Write skill to conduit dir.
	conduitSkill := filepath.Join(conduitDir, "skills", "shared")
	writeFile(t, filepath.Join(conduitSkill, "SKILL.md"),
		"---\nname: shared\ndescription: From conduit dir\n---\nConduit version.\n")

	// Write same-named skill to cwd — should win.
	cwdSkill := filepath.Join(cwd, ".claude", "skills", "shared")
	writeFile(t, filepath.Join(cwdSkill, "SKILL.md"),
		"---\nname: shared\ndescription: From cwd\n---\nCwd version.\n")

	skills := DiscoverFS(cwd)
	if len(skills) != 1 {
		t.Fatalf("DiscoverFS returned %d skills; want 1", len(skills))
	}
	if skills[0].Description != "From cwd" {
		t.Errorf("Description = %q; want %q", skills[0].Description, "From cwd")
	}
}

func TestDiscoverFS_NoFrontmatter_FallbackDescription(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	skillDir := filepath.Join(cwd, ".claude", "skills", "bare-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"),
		"# Bare skill heading\n\nBody text here.\n")

	skills := DiscoverFS(cwd)
	if len(skills) != 1 {
		t.Fatalf("DiscoverFS returned %d; want 1", len(skills))
	}
	sk := skills[0]
	if sk.Name != "bare-skill" {
		t.Errorf("Name = %q; want %q", sk.Name, "bare-skill")
	}
	// Description should come from first non-blank, heading-stripped line.
	if sk.Description != "Bare skill heading" {
		t.Errorf("Description = %q; want %q", sk.Description, "Bare skill heading")
	}
}

func TestDiscoverFS_References(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	skillDir := filepath.Join(cwd, ".claude", "skills", "ref-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"),
		"---\nname: ref-skill\ndescription: Has refs\n---\nMain body.\n")
	writeFile(t, filepath.Join(skillDir, "references", "extra.md"), "# Extra Reference\n\nSome content.")

	skills := DiscoverFS(cwd)
	if len(skills) != 1 {
		t.Fatalf("DiscoverFS returned %d; want 1", len(skills))
	}
	body := skills[0].Body
	if body == "" {
		t.Fatal("Body is empty")
	}
	// References should be appended under a ## References header.
	if !containsString(body, "## References") {
		t.Errorf("Body missing ## References header; got:\n%s", body)
	}
	if !containsString(body, "Some content.") {
		t.Errorf("Body missing reference content; got:\n%s", body)
	}
}

func TestLoadFS_ReturnsCommands(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	skillDir := filepath.Join(cwd, ".claude", "skills", "cmd-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"),
		"---\nname: cmd-skill\ndescription: A command skill\n---\nDo stuff.\n")

	cmds := LoadFS(cwd)
	if len(cmds) != 1 {
		t.Fatalf("LoadFS returned %d commands; want 1", len(cmds))
	}
	if cmds[0].QualifiedName != "cmd-skill" {
		t.Errorf("QualifiedName = %q; want %q", cmds[0].QualifiedName, "cmd-skill")
	}
	if cmds[0].Description != "A command skill" {
		t.Errorf("Description = %q; want %q", cmds[0].Description, "A command skill")
	}
}

func TestDiscoverFS_EmptyCwd(t *testing.T) {
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// Should not panic with empty cwd.
	skills := DiscoverFS("")
	_ = skills // result doesn't matter; no crash is the assertion
}

// containsString reports whether haystack contains needle.
func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(haystack) > 0 && len(needle) > 0 && func() bool {
			for i := 0; i <= len(haystack)-len(needle); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}
