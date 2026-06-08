package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantDesc    string
		wantBodyHas string
	}{
		{
			name:        "no frontmatter",
			content:     "Just a body",
			wantDesc:    "",
			wantBodyHas: "Just a body",
		},
		{
			name:        "frontmatter with description",
			content:     "---\ndescription: My command\n---\nBody text",
			wantDesc:    "My command",
			wantBodyHas: "Body text",
		},
		{
			name:        "frontmatter extra keys ignored",
			content:     "---\ndescription: The desc\nwhen_to_use: when needed\n---\nContent",
			wantDesc:    "The desc",
			wantBodyHas: "Content",
		},
		{
			name:        "no description in frontmatter",
			content:     "---\nwhen_to_use: when needed\n---\nContent",
			wantDesc:    "",
			wantBodyHas: "Content",
		},
		{
			name:        "unclosed frontmatter treated as body",
			content:     "---\ndescription: Orphan\nno closing delimiter",
			wantDesc:    "",
			wantBodyHas: "---",
		},
		{
			name:        "description with extra whitespace",
			content:     "---\ndescription:   Trimmed   \n---\nBody",
			wantDesc:    "Trimmed",
			wantBodyHas: "Body",
		},
		{
			name:        "empty content",
			content:     "",
			wantDesc:    "",
			wantBodyHas: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			desc, body := parseFrontmatter(tc.content)
			if desc != tc.wantDesc {
				t.Errorf("description: got %q, want %q", desc, tc.wantDesc)
			}
			if tc.wantBodyHas != "" && !strings.Contains(body, tc.wantBodyHas) {
				t.Errorf("body %q does not contain %q", body, tc.wantBodyHas)
			}
		})
	}
}

func TestCommandNameFromPath(t *testing.T) {
	tests := []struct {
		relPath string
		want    string
	}{
		{"foo.md", "foo"},
		{"FOO.md", "foo"},
		{"my-workflow.md", "my-workflow"},
		{"dir/name.md", "dir:name"},
		{"dir/Name.MD", "dir:name"},
		{"dir/SKILL.md", "dir"},
		{"dir/skill.md", "dir"},
		{"dir/Skill.MD", "dir"},
		{"SKILL.md", ""}, // SKILL.md at root = invalid
		{"noext", ""},    // missing .md
		{"foo.txt", ""},  // wrong extension
		{"", ""},
	}
	for _, tc := range tests {
		got := commandNameFromPath(tc.relPath)
		if got != tc.want {
			t.Errorf("commandNameFromPath(%q) = %q, want %q", tc.relPath, got, tc.want)
		}
	}
}

func TestRegisterCustomCommands(t *testing.T) {
	dir := t.TempDir()
	commandsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Flat command with frontmatter
	writeFile(t, filepath.Join(commandsDir, "greet.md"),
		"---\ndescription: Greet someone\n---\nHello $ARGUMENTS!")

	// Flat command without frontmatter
	writeFile(t, filepath.Join(commandsDir, "simple.md"), "Just run this")

	// Nested command
	sub := filepath.Join(commandsDir, "work")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "deploy.md"),
		"---\ndescription: Deploy to prod\n---\nDeploy $ARGUMENTS to production")

	// SKILL.md → directory name
	writeFile(t, filepath.Join(sub, "SKILL.md"),
		"---\ndescription: Work skill\n---\nWork prompt")

	// Non-.md file — should be ignored
	writeFile(t, filepath.Join(commandsDir, "ignored.txt"), "ignored")

	// Hidden file — should be ignored
	writeFile(t, filepath.Join(commandsDir, ".hidden.md"), "hidden")

	r := New()
	// Simulate what RegisterCustomCommands does but for a temp dir.
	loadCommandsFromDir(r, commandsDir)

	// greet
	greet, ok := r.cmds["greet"]
	if !ok {
		t.Fatal("expected command 'greet' to be registered")
	}
	if greet.Description != "Greet someone" {
		t.Errorf("greet.Description = %q, want %q", greet.Description, "Greet someone")
	}
	res := greet.Handler("world")
	if res.Type != "prompt" {
		t.Errorf("greet result type = %q, want prompt", res.Type)
	}
	if res.Text != "Hello world!" {
		t.Errorf("greet result text = %q, want %q", res.Text, "Hello world!")
	}

	// simple (no frontmatter)
	simple, ok := r.cmds["simple"]
	if !ok {
		t.Fatal("expected command 'simple' to be registered")
	}
	if simple.Description != "/simple" {
		t.Errorf("simple.Description = %q, want /simple", simple.Description)
	}
	res = simple.Handler("")
	if res.Text != "Just run this" {
		t.Errorf("simple result = %q, want 'Just run this'", res.Text)
	}

	// work:deploy
	deploy, ok := r.cmds["work:deploy"]
	if !ok {
		t.Fatal("expected command 'work:deploy' to be registered")
	}
	if deploy.Description != "Deploy to prod" {
		t.Errorf("deploy.Description = %q", deploy.Description)
	}
	res = deploy.Handler("staging")
	if res.Text != "Deploy staging to production" {
		t.Errorf("deploy result = %q", res.Text)
	}

	// work (from SKILL.md)
	work, ok := r.cmds["work"]
	if !ok {
		t.Fatal("expected command 'work' to be registered (from SKILL.md)")
	}
	if work.Description != "Work skill" {
		t.Errorf("work.Description = %q", work.Description)
	}

	// ignored.txt should not register
	if _, ok := r.cmds["ignored"]; ok {
		t.Error("ignored.txt should not be registered as a command")
	}

	// .hidden.md should not register
	if _, ok := r.cmds[".hidden"]; ok {
		t.Error(".hidden.md should not be registered")
	}
}

func TestRegisterCustomCommandsArgumentsAbsent(t *testing.T) {
	dir := t.TempDir()
	commandsDir := filepath.Join(dir, "commands")
	_ = os.MkdirAll(commandsDir, 0o755)
	writeFile(t, filepath.Join(commandsDir, "noargs.md"), "Do the thing")

	r := New()
	loadCommandsFromDir(r, commandsDir)

	cmd, ok := r.cmds["noargs"]
	if !ok {
		t.Fatal("expected 'noargs' command")
	}
	// No $ARGUMENTS in body — args are ignored, body returned as-is
	res := cmd.Handler("ignored")
	if res.Text != "Do the thing" {
		t.Errorf("got %q, want 'Do the thing'", res.Text)
	}
}

func TestRegisterCustomCommandsProjectOverridesUser(t *testing.T) {
	base := t.TempDir()
	userDir := filepath.Join(base, "user-commands")
	projDir := filepath.Join(base, "proj-commands")
	_ = os.MkdirAll(userDir, 0o755)
	_ = os.MkdirAll(projDir, 0o755)

	writeFile(t, filepath.Join(userDir, "foo.md"), "User version")
	writeFile(t, filepath.Join(projDir, "foo.md"), "Project version")

	r := New()
	loadCommandsFromDir(r, userDir)
	loadCommandsFromDir(r, projDir)

	res := r.cmds["foo"].Handler("")
	if res.Text != "Project version" {
		t.Errorf("expected project to win, got %q", res.Text)
	}
}

func TestRegisterCustomCommandsNonexistentDir(t *testing.T) {
	r := New()
	// Should not panic or error
	loadCommandsFromDir(r, "/nonexistent/path/commands")
	if len(r.cmds) != 0 {
		t.Errorf("expected empty registry, got %d commands", len(r.cmds))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}
