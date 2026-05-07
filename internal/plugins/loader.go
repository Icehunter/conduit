// Package plugins discovers and loads conduit plugins.
//
// A plugin is a directory containing a .claude-plugin/plugin.json manifest
// and optional subdirectories:
//   - commands/   — slash commands (*.md files, registered as /plugin:command)
//   - agents/     — subagent definitions (*.md files)
//   - skills/     — skill definitions (*.md files)
//   - hooks/      — hook scripts (hooks.json + *.py)
//
// Plugin search path (in order, later entries override earlier):
//  1. Built-in plugins in the conduit binary's plugin dir (via embed)
//  2. ~/.conduit/plugins/<pluginName>/
//  3. ~/.claude/plugins/<pluginName>/ (legacy fallback only)
//  4. <cwd>/.claude/plugins/<pluginName>/
//
// Mirrors src/utils/plugins/pluginLoader.ts.
package plugins

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
)

// Manifest is the parsed .claude-plugin/plugin.json.
type Manifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version,omitempty"`
	Author      struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
}

// CommandDef is one slash command defined by a plugin.
type CommandDef struct {
	// PluginName is the providing plugin's name.
	PluginName string
	// Name is the base name (filename without .md).
	Name string
	// QualifiedName is "pluginName:name" — used as the slash command.
	QualifiedName string
	// Description is from frontmatter.
	Description string
	// Body is the full markdown content (frontmatter stripped).
	Body string
	// AllowedTools is from frontmatter.
	AllowedTools []string
}

// Plugin is a loaded plugin with its manifest and discovered content.
type Plugin struct {
	Dir      string
	Manifest Manifest
	Commands []CommandDef
}

// installedPluginsFile is the subset of ~/.conduit/plugins/installed_plugins.json we need.
// Real Claude Code v2 format: {"version":2,"plugins":{"name@marketplace":[{...}]}}
type installedPluginsFile struct {
	Version int                          `json:"version"`
	Plugins map[string][]pluginInstEntry `json:"plugins"`
}

type pluginInstEntry struct {
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
	Version     string `json:"version"`
}

// pluginsDir returns the path to the Conduit-owned plugin directory.
func pluginsDir() string {
	if dir := os.Getenv("CONDUIT_PLUGIN_CACHE_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("CLAUDE_CODE_PLUGIN_CACHE_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(settings.ConduitDir(), "plugins")
}

func legacyPluginsDir() string {
	home, _ := os.UserHomeDir()
	claudeHome := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	return filepath.Join(claudeHome, "plugins")
}

func ensurePluginStorageImported() {
	dst := pluginsDir()
	if os.Getenv("CONDUIT_PLUGIN_CACHE_DIR") != "" || os.Getenv("CLAUDE_CODE_PLUGIN_CACHE_DIR") != "" {
		return
	}
	if _, err := os.Stat(dst); err == nil {
		return
	}
	src := legacyPluginsDir()
	if _, err := os.Stat(src); err != nil {
		return
	}
	if err := copyPluginTree(src, dst); err == nil {
		_ = rewriteLegacyPluginPaths(src, dst)
	}
}

func copyPluginTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := os.Stat(target); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(target, data, mode)
	})
}

func rewriteLegacyPluginPaths(legacyRoot, conduitRoot string) error {
	installedPath := filepath.Join(conduitRoot, "installed_plugins.json")
	if data, err := os.ReadFile(installedPath); err == nil {
		var installed InstalledPluginsV2
		if json.Unmarshal(data, &installed) == nil {
			for id, entries := range installed.Plugins {
				for i := range entries {
					entries[i].InstallPath = rewritePluginPath(entries[i].InstallPath, legacyRoot, conduitRoot)
				}
				installed.Plugins[id] = entries
			}
			var raw map[string]json.RawMessage
			if json.Unmarshal(data, &raw) != nil || raw == nil {
				raw = map[string]json.RawMessage{}
			}
			if versionRaw, err := json.Marshal(installed.Version); err == nil {
				raw["version"] = versionRaw
			}
			if pluginsRaw, err := json.Marshal(installed.Plugins); err == nil {
				raw["plugins"] = pluginsRaw
			}
			if out, err := json.MarshalIndent(raw, "", "  "); err == nil {
				_ = os.WriteFile(installedPath, append(out, '\n'), 0o600)
			}
		}
	}

	marketplacesPath := filepath.Join(conduitRoot, "known_marketplaces.json")
	if data, err := os.ReadFile(marketplacesPath); err == nil {
		var marketplaces map[string]MarketplaceEntry
		if json.Unmarshal(data, &marketplaces) == nil {
			for name, entry := range marketplaces {
				entry.InstallLocation = rewritePluginPath(entry.InstallLocation, legacyRoot, conduitRoot)
				marketplaces[name] = entry
			}
			out, err := json.MarshalIndent(marketplaces, "", "  ")
			if err == nil {
				_ = os.WriteFile(marketplacesPath, append(out, '\n'), 0o600)
			}
		}
	}
	return nil
}

func rewritePluginPath(path, legacyRoot, conduitRoot string) string {
	if path == "" {
		return path
	}
	rel, err := filepath.Rel(legacyRoot, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return path
	}
	return filepath.Join(conduitRoot, rel)
}

// loadInstalledPlugins reads ~/.conduit/plugins/installed_plugins.json and
// returns each unique installPath (deduplicated by plugin name, keeping the
// most recent entry). If Conduit has no plugin storage yet, legacy Claude
// plugin storage is imported once as a compatibility source.
func loadInstalledPlugins() []string {
	ensurePluginStorageImported()
	path := filepath.Join(pluginsDir(), "installed_plugins.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f installedPluginsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil
	}
	// Collect one installPath per plugin id (last entry per id wins).
	seen := map[string]bool{}
	var paths []string
	for _, entries := range f.Plugins {
		for _, e := range entries {
			if e.InstallPath != "" && !seen[e.InstallPath] {
				seen[e.InstallPath] = true
				paths = append(paths, e.InstallPath)
			}
		}
	}
	return paths
}

// LoadAll discovers and loads all plugins from:
//  1. ~/.conduit/plugins/installed_plugins.json — plugins installed via /plugin install
//  2. Bundled plugins adjacent to the binary in plugins/
//  3. ~/.conduit/plugins/<name>/ — manually dropped plugin dirs
//  4. ~/.claude/plugins/<name>/ — legacy manually dropped plugin dirs
//  5. <cwd>/.claude/plugins/<name>/ — project-local plugins
func LoadAll(cwd string) ([]*Plugin, error) {
	ensurePluginStorageImported()
	seen := map[string]bool{} // deduplicate by manifest name
	var plugins []*Plugin

	add := func(dir string) {
		p, err := loadPlugin(dir)
		if err != nil || seen[p.Manifest.Name] {
			return
		}
		seen[p.Manifest.Name] = true
		plugins = append(plugins, p)
	}

	// 1. Installed via /plugin install (or real Claude Code's install system).
	for _, installPath := range loadInstalledPlugins() {
		add(installPath)
	}

	// 2. Bundled plugins adjacent to the binary.
	if exe, err := os.Executable(); err == nil {
		bundled := filepath.Join(filepath.Dir(exe), "plugins")
		scanDir(bundled, add)
	}

	// 3. ~/.conduit/plugins/<name>/ — manually placed plugins.
	// Note: skip the well-known subdirs used by the install system.
	scanDirExclude(filepath.Join(pluginsDir()), []string{"cache", "data", "marketplaces"}, add)

	// 4. Legacy ~/.claude/plugins/<name>/ — manually placed plugins.
	if legacy := legacyPluginsDir(); legacy != pluginsDir() {
		scanDirExclude(legacy, []string{"cache", "data", "marketplaces"}, add)
	}

	// 5. Project-local plugins.
	if cwd != "" {
		scanDir(filepath.Join(cwd, ".claude", "plugins"), add)
	}

	return plugins, nil
}

func scanDir(base string, add func(string)) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			add(filepath.Join(base, e.Name()))
		}
	}
}

func scanDirExclude(base string, exclude []string, add func(string)) {
	excl := map[string]bool{}
	for _, e := range exclude {
		excl[e] = true
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && !excl[e.Name()] {
			add(filepath.Join(base, e.Name()))
		}
	}
}

// loadPlugin reads one plugin directory and returns a Plugin, or an error
// if the directory has no valid manifest.
func loadPlugin(dir string) (*Plugin, error) {
	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	p := &Plugin{Dir: dir, Manifest: manifest}

	// Only commands/*.md are loaded. hooks/, agents/, skills/ subdirectories
	// are intentionally ignored — dynamic code execution is not supported.
	// Plugins that need runtime behavior must expose an MCP server instead.
	cmdDir := filepath.Join(dir, "commands")
	entries, _ := os.ReadDir(cmdDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		cmdPath := filepath.Join(cmdDir, e.Name())
		content, err := os.ReadFile(cmdPath)
		if err != nil {
			continue
		}
		baseName := strings.TrimSuffix(e.Name(), ".md")
		cmd := parseCommandFile(manifest.Name, baseName, string(content))
		p.Commands = append(p.Commands, cmd)
	}

	return p, nil
}

// parseCommandFile parses a command markdown file, extracting frontmatter.
func parseCommandFile(pluginName, baseName, content string) CommandDef {
	cmd := CommandDef{
		PluginName:    pluginName,
		Name:          baseName,
		QualifiedName: pluginName + ":" + baseName,
		Body:          content,
	}

	// Parse YAML frontmatter between --- delimiters.
	fm, body, ok := extractFrontmatter(content)
	if ok {
		cmd.Body = body
		cmd.Description = fm["description"]
		if allowed, ok := fm["allowed-tools"]; ok && allowed != "" {
			// Frontmatter allowed-tools can be a YAML list or a quoted JSON array.
			// We handle the simple cases: single-line CSV or JSON array string.
			cmd.AllowedTools = parseAllowedTools(allowed)
		}
	}

	return cmd
}

// extractFrontmatter splits content into (frontmatter map, body, found).
// Only handles simple key: "value" pairs — not full YAML parsing.
func extractFrontmatter(content string) (map[string]string, string, bool) {
	if !strings.HasPrefix(content, "---") {
		return nil, content, false
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return nil, content, false
	}
	fmRaw := content[3 : 3+end]
	body := content[3+end+3:]
	body = strings.TrimPrefix(body, "\n")

	fm := make(map[string]string)
	for _, line := range strings.Split(fmRaw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// Strip surrounding quotes if present.
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		fm[key] = val
	}
	return fm, body, true
}

// parseAllowedTools parses the allowed-tools frontmatter value.
// Accepts: JSON array string or comma-separated list.
func parseAllowedTools(raw string) []string {
	raw = strings.TrimSpace(raw)
	// Try JSON array.
	if strings.HasPrefix(raw, "[") {
		var tools []string
		if err := json.Unmarshal([]byte(raw), &tools); err == nil {
			return tools
		}
	}
	// Comma-separated.
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
