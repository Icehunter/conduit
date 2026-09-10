package plugins

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
)

// DiscoverFSAgents scans the standard personal (non-plugin) agent
// directories and returns every discovered AgentDef. These are bare
// "name: ..." markdown files under an agents/ directory — the same shape
// plugins use, but not bundled inside a plugin.
//
// Discovery order (later entries override earlier ones with the same name):
//  1. ~/.conduit/agents/*.md
//  2. ~/.claude/agents/*.md
//  3. <cwd>/.claude/agents/*.md
//
// PluginName is left empty and QualifiedName equals the bare Name, so these
// agents are found by AgentRegistry.FindAgent's bare-name fallback.
func DiscoverFSAgents(cwd string) []AgentDef {
	dirs := []string{
		filepath.Join(settings.ConduitDir(), "agents"),
		filepath.Join(settings.ClaudeDir(), "agents"),
	}
	if cwd != "" {
		dirs = append(dirs, filepath.Join(cwd, ".claude", "agents"))
	}

	byName := make(map[string]AgentDef)
	var order []string

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			agentPath := filepath.Join(dir, e.Name())
			// e.IsDir() reports the entry's own type, which is false for a
			// symlink even when it points at a directory. Agent entries are
			// files, but the entry itself may be a symlink (as graph-eng's
			// install.sh creates); Stat follows the link so a symlinked file
			// is still recognized as a regular file, not skipped.
			info, err := os.Stat(agentPath)
			if err != nil || info.IsDir() {
				continue
			}
			content, err := os.ReadFile(agentPath) //nolint:gosec // agent files are user-owned
			if err != nil {
				continue
			}
			baseName := strings.TrimSuffix(e.Name(), ".md")
			fm, body, _ := extractFrontmatter(string(content))
			ad := AgentDef{
				Name:          baseName,
				QualifiedName: baseName,
				Body:          body,
			}
			if fm != nil {
				if name := fm["name"]; name != "" {
					ad.Name = name
					ad.QualifiedName = name
				}
				ad.Description = fm["description"]
				ad.Model = fm["model"]
				ad.Role = fm["role"]
				if raw, ok := fm["tools"]; ok && raw != "" {
					ad.Tools = parseAllowedTools(raw)
				}
			}

			if _, exists := byName[ad.Name]; !exists {
				order = append(order, ad.Name)
			}
			byName[ad.Name] = ad
		}
	}

	out := make([]AgentDef, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}
