package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// pluginsInstalledV2 is the minimal shape of installed_plugins.json we need.
type pluginsInstalledV2 struct {
	Plugins map[string][]struct {
		InstallPath string `json:"installPath"`
	} `json:"plugins"`
}

// pluginsSettingsJSON is the minimal shape of ~/.claude/settings.json we need.
type pluginsSettingsJSON struct {
	EnabledPlugins map[string]interface{} `json:"enabledPlugins"`
}

// claudeJSON is the shape of ~/.claude.json used by real Claude Code.
// We only decode the fields we need for MCP server discovery.
type claudeJSON struct {
	McpServers map[string]ServerConfig            `json:"mcpServers"`
	Projects   map[string]claudeJSONProject       `json:"projects"`
}

type claudeJSONProject struct {
	McpServers          map[string]ServerConfig `json:"mcpServers"`
	DisabledMcpServers  []string                `json:"disabledMcpServers,omitempty"`
	EnabledMcpServers   []string                `json:"enabledMcpServers,omitempty"`
}

// globalClaudeFile returns the path to the global Claude config file.
// Mirrors getGlobalClaudeFile() in src/utils/env.ts:
//   - $CLAUDE_CONFIG_DIR/.claude.json if set
//   - ~/.claude/.config.json if it exists (legacy)
//   - ~/.claude.json otherwise
func globalClaudeFile() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	legacy := filepath.Join(home, ".claude", ".config.json")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return filepath.Join(home, ".claude.json")
}

// LoadConfigs loads MCP server configs from all config sources, in priority
// order (later entries override earlier ones with the same name):
//
//  1. user   — ~/.claude.json → mcpServers  (global)
//  2. local  — ~/.claude.json → projects[cwd].mcpServers  (per-project)
//  3. project — every .mcp.json from filesystem root down to cwd (closer wins)
//
// Mirrors getMcpConfigsByScope() in src/services/mcp/config.ts.
func LoadConfigs(cwd string) (map[string]ServerConfig, error) {
	merged := make(map[string]ServerConfig)

	// 1 + 2. ~/.claude.json
	claudePath := globalClaudeFile()
	if data, err := os.ReadFile(claudePath); err == nil {
		var cfg claudeJSON
		if json.Unmarshal(data, &cfg) == nil {
			// user scope: global mcpServers
			for name, srv := range cfg.McpServers {
				srv.Source = claudePath
				srv.Scope = "user"
				merged[name] = srv
			}
			// local scope: per-project mcpServers keyed by abs cwd
			if cwd != "" {
				if proj, ok := cfg.Projects[cwd]; ok {
					for name, srv := range proj.McpServers {
						srv.Source = claudePath
						srv.Scope = "local"
						merged[name] = srv
					}
				}
			}
		}
	}

	// 3. project scope: walk all parent dirs from root down to cwd,
	// merging .mcp.json files (closer to cwd wins, matching TS behaviour).
	if cwd != "" {
		dirs := ancestorDirs(cwd)
		for _, dir := range dirs {
			mcpJSON := filepath.Join(dir, ".mcp.json")
			if cfg, err := loadMcpFile(mcpJSON); err == nil {
				for name, srv := range cfg.McpServers {
					srv.Source = mcpJSON
					srv.Scope = "project"
					merged[name] = srv
				}
			}
		}
	}

	// 4. Plugin-provided MCP servers: read installed_plugins.json, check each
	// enabled plugin's install dir for a .mcp.json file.
	// These show as scope "plugin" in the manager.
	loadPluginMCPServers(merged)

	return merged, nil
}

// loadPluginMCPServers reads each enabled, installed plugin's .mcp.json and
// adds its servers to merged with scope="plugin".
func loadPluginMCPServers(merged map[string]ServerConfig) {
	home, _ := os.UserHomeDir()
	claudeHome := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	pluginsDir := os.Getenv("CLAUDE_CODE_PLUGIN_CACHE_DIR")
	if pluginsDir == "" {
		pluginsDir = filepath.Join(claudeHome, "plugins")
	}

	// Read which plugins are enabled from settings.json.
	settingsPath := filepath.Join(claudeHome, "settings.json")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		return
	}
	var settings pluginsSettingsJSON
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		return
	}

	// Read installed plugin paths from installed_plugins.json.
	installedPath := filepath.Join(pluginsDir, "installed_plugins.json")
	installedData, err := os.ReadFile(installedPath)
	if err != nil {
		return
	}
	var installed pluginsInstalledV2
	if err := json.Unmarshal(installedData, &installed); err != nil {
		return
	}

	for pluginID, entries := range installed.Plugins {
		// Check if this plugin is enabled (value must be truthy).
		enabled := false
		if v, ok := settings.EnabledPlugins[pluginID]; ok {
			switch val := v.(type) {
			case bool:
				enabled = val
			case []interface{}:
				enabled = len(val) > 0
			}
		}
		if !enabled {
			continue
		}

		// Use the first install entry (user scope preferred).
		if len(entries) == 0 {
			continue
		}
		installPath := entries[0].InstallPath
		if installPath == "" {
			continue
		}

		// Parse the plugin name from "name@marketplace".
		pluginName := pluginID
		if at := strings.LastIndex(pluginID, "@"); at >= 0 {
			pluginName = pluginID[:at]
		}

		// Load .mcp.json from the install dir.
		mcpJSONPath := filepath.Join(installPath, ".mcp.json")
		cfg, err := loadPluginMcpFile(mcpJSONPath)
		if err != nil {
			continue
		}
		for name, srv := range cfg {
			// Prefix with "plugin:<pluginName>:" to match Claude Code naming.
			qualName := "plugin:" + pluginName + ":" + name
			srv.Source = "plugin:" + pluginName
			srv.Scope = "plugin"
			srv.PluginName = pluginName
			merged[qualName] = srv
		}
	}
}

// ancestorDirs returns the directory chain from filesystem root down to (and
// including) dir, so that merging in order gives closer-to-cwd entries higher
// priority (last write wins in the caller's loop).
func ancestorDirs(dir string) []string {
	dir = filepath.Clean(dir)
	root := filepath.VolumeName(dir) + string(filepath.Separator)
	var chain []string
	for {
		chain = append([]string{dir}, chain...)
		if dir == root {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return chain
}

func loadMcpFile(path string) (*McpJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg McpJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// loadPluginMcpFile reads a plugin's .mcp.json which may be either:
//   - {"mcpServers": {"name": {...}}}  — standard format
//   - {"name": {...}}                  — flat format used by many plugins
func loadPluginMcpFile(path string) (map[string]ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Try standard format first.
	var wrapped McpJSON
	if json.Unmarshal(data, &wrapped) == nil && len(wrapped.McpServers) > 0 {
		return wrapped.McpServers, nil
	}
	// Try flat format: top-level keys are server names.
	var flat map[string]ServerConfig
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, err
	}
	return flat, nil
}

// expandEnv replaces ${VAR} and $VAR in the string with environment values.
func expandEnv(s string) string {
	return os.Expand(s, func(key string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		return ""
	})
}

// IsDisabled returns true if the named server is in disabledMcpServers for cwd.
// Mirrors isMcpServerDisabled() in src/services/mcp/config.ts.
func IsDisabled(name, cwd string) bool {
	data, err := os.ReadFile(globalClaudeFile())
	if err != nil {
		return false
	}
	var cfg claudeJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	proj := cfg.Projects[cwd]
	for _, d := range proj.DisabledMcpServers {
		if d == name {
			return true
		}
	}
	return false
}

// SetDisabled adds or removes name from disabledMcpServers in ~/.claude.json → projects[cwd].
// Mirrors setMcpServerEnabled() in src/services/mcp/config.ts.
func SetDisabled(name, cwd string, disabled bool) error {
	path := globalClaudeFile()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var cfg map[string]json.RawMessage
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
	} else {
		cfg = make(map[string]json.RawMessage)
	}

	// Read existing projects map.
	var projects map[string]json.RawMessage
	if raw, ok := cfg["projects"]; ok {
		_ = json.Unmarshal(raw, &projects)
	}
	if projects == nil {
		projects = make(map[string]json.RawMessage)
	}

	// Read existing project config.
	var proj claudeJSONProject
	if raw, ok := projects[cwd]; ok {
		_ = json.Unmarshal(raw, &proj)
	}

	// Toggle membership in DisabledMcpServers.
	proj.DisabledMcpServers = toggleList(proj.DisabledMcpServers, name, disabled)

	// Write back.
	projRaw, err := json.Marshal(proj)
	if err != nil {
		return err
	}
	projects[cwd] = projRaw
	projectsRaw, err := json.Marshal(projects)
	if err != nil {
		return err
	}
	cfg["projects"] = projectsRaw
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func toggleList(list []string, name string, add bool) []string {
	has := false
	for _, v := range list {
		if v == name {
			has = true
			break
		}
	}
	if add && !has {
		return append(list, name)
	}
	if !add && has {
		out := list[:0]
		for _, v := range list {
			if v != name {
				out = append(out, v)
			}
		}
		return out
	}
	return list
}

// NormalizeServerName converts an MCP server name to a safe tool-name prefix.
// "my-server" → "my_server__" (double underscore separator matches TS convention)
func NormalizeServerName(name string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, name)
	return safe + "__"
}
