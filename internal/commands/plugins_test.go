package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterFSSkillCommands(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())

	// Real skill, symlinked into place — mirrors graph-eng's install.sh.
	real := t.TempDir()
	realSkillDir := filepath.Join(real, "graph-engineering")
	if err := os.MkdirAll(realSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(realSkillDir, "SKILL.md"),
		"---\nname: graph-engineering\ndescription: Graph engineering doctrine\n---\nFan out, reduce, verify, synthesize.")

	skillsDir := filepath.Join(claudeDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSkillDir, filepath.Join(skillsDir, "graph-engineering")); err != nil {
		t.Fatal(err)
	}

	r := New()
	RegisterFSSkillCommands(r, "")

	cmd, ok := r.cmds["graph-engineering"]
	if !ok {
		t.Fatal("expected command 'graph-engineering' to be registered")
	}
	if cmd.Description != "Graph engineering doctrine" {
		t.Errorf("Description = %q; want %q", cmd.Description, "Graph engineering doctrine")
	}
	res := cmd.Handler("")
	if res.Text != "Fan out, reduce, verify, synthesize." {
		t.Errorf("Handler text = %q", res.Text)
	}
}

func TestRegisterFSSkillCommands_DoesNotOverrideExisting(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())

	simplifyDir := filepath.Join(claudeDir, "skills", "simplify")
	if err := os.MkdirAll(simplifyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(simplifyDir, "SKILL.md"),
		"---\nname: simplify\ndescription: FS override attempt\n---\nShould not win.")

	r := New()
	r.Register(Command{
		Name:        "simplify",
		Description: "Bundled simplify",
		Handler:     func(string) Result { return Result{Type: "text", Text: "bundled"} },
	})

	RegisterFSSkillCommands(r, "")

	cmd := r.cmds["simplify"]
	if cmd.Description != "Bundled simplify" {
		t.Errorf("Description = %q; want existing registration preserved", cmd.Description)
	}
}
