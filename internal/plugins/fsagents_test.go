package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAgentFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverFSAgents(t *testing.T) {
	conduitHome := t.TempDir()
	claudeHome := t.TempDir()
	projectDir := t.TempDir()

	t.Setenv("CONDUIT_CONFIG_DIR", conduitHome)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)

	t.Run("basic frontmatter parse", func(t *testing.T) {
		writeAgentFile(t, filepath.Join(claudeHome, "agents"), "graph-skeptic.md",
			"---\ndescription: Attack a finding\ntools: Glob, Grep, Read\n---\n# Body\n")

		agents := DiscoverFSAgents("")
		var found *AgentDef
		for i := range agents {
			if agents[i].Name == "graph-skeptic" {
				found = &agents[i]
			}
		}
		if found == nil {
			t.Fatal("graph-skeptic not discovered")
		}
		if found.QualifiedName != "graph-skeptic" {
			t.Errorf("QualifiedName = %q, want bare name", found.QualifiedName)
		}
		if found.Description != "Attack a finding" {
			t.Errorf("Description = %q", found.Description)
		}
		if len(found.Tools) != 3 || found.Tools[0] != "Glob" {
			t.Errorf("Tools = %v", found.Tools)
		}
		if found.Body != "# Body\n" {
			t.Errorf("Body = %q", found.Body)
		}
	})

	t.Run("model and role fields", func(t *testing.T) {
		writeAgentFile(t, filepath.Join(claudeHome, "agents"), "planner.md",
			"---\ndescription: Plans work\nmodel: claude-opus-4\nrole: planning\n---\nBody\n")

		agents := DiscoverFSAgents("")
		var found *AgentDef
		for i := range agents {
			if agents[i].Name == "planner" {
				found = &agents[i]
			}
		}
		if found == nil {
			t.Fatal("planner not discovered")
		}
		if found.Model != "claude-opus-4" {
			t.Errorf("Model = %q", found.Model)
		}
		if found.Role != "planning" {
			t.Errorf("Role = %q", found.Role)
		}
	})

	t.Run("precedence: cwd overrides claude home overrides conduit home", func(t *testing.T) {
		writeAgentFile(t, filepath.Join(conduitHome, "agents"), "shared.md",
			"---\ndescription: from conduit\n---\nBody\n")
		writeAgentFile(t, filepath.Join(claudeHome, "agents"), "shared.md",
			"---\ndescription: from claude\n---\nBody\n")
		writeAgentFile(t, filepath.Join(projectDir, ".claude", "agents"), "shared.md",
			"---\ndescription: from project\n---\nBody\n")

		agents := DiscoverFSAgents(projectDir)
		var found *AgentDef
		for i := range agents {
			if agents[i].Name == "shared" {
				found = &agents[i]
			}
		}
		if found == nil {
			t.Fatal("shared not discovered")
		}
		if found.Description != "from project" {
			t.Errorf("Description = %q, want project dir to win", found.Description)
		}
	})

	t.Run("symlinked agent file is followed", func(t *testing.T) {
		srcDir := t.TempDir()
		writeAgentFile(t, srcDir, "linked.md", "---\ndescription: linked agent\n---\nBody\n")

		agentsDir := filepath.Join(claudeHome, "agents")
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(srcDir, "linked.md"), filepath.Join(agentsDir, "linked.md")); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}

		agents := DiscoverFSAgents("")
		var found *AgentDef
		for i := range agents {
			if agents[i].Name == "linked" {
				found = &agents[i]
			}
		}
		if found == nil {
			t.Fatal("symlinked agent not discovered")
		}
		if found.Description != "linked agent" {
			t.Errorf("Description = %q", found.Description)
		}
	})

	t.Run("non-md files ignored", func(t *testing.T) {
		writeAgentFile(t, filepath.Join(claudeHome, "agents"), "notes.txt", "not an agent")
		agents := DiscoverFSAgents("")
		for _, a := range agents {
			if a.Name == "notes" {
				t.Error("non-.md file was discovered as an agent")
			}
		}
	})
}

func TestAgentRegistry_AddFS(t *testing.T) {
	claudeHome := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)

	writeAgentFile(t, filepath.Join(claudeHome, "agents"), "graph-skeptic.md",
		"---\ndescription: Attack a finding\n---\nBody\n")

	dir := makeTestPlugin(t, "toolkit")
	agentsDir := filepath.Join(dir, "agents")
	writeAgentFile(t, agentsDir, "reviewer.md", "---\ndescription: Reviewer\n---\n# Body\n")
	p, _ := loadPlugin(dir)

	reg := NewAgentRegistry([]*Plugin{p})
	if reg.FindAgent("graph-skeptic") != nil {
		t.Fatal("FindAgent found FS agent before AddFS was called")
	}

	reg.AddFS("")

	if reg.FindAgent("graph-skeptic") == nil {
		t.Error("FindAgent(\"graph-skeptic\") = nil after AddFS")
	}
	if reg.FindAgent("toolkit:reviewer") == nil {
		t.Error("plugin agent lost after AddFS")
	}
}
