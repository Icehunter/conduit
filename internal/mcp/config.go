package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/settings"
)

// claudeJSON is the shape of ~/.claude.json used by real Claude Code.
// We only decode the fields we need for MCP server discovery.
type claudeJSON struct {
	McpServers map[string]ServerConfig      `json:"mcpServers"`
	Projects   map[string]claudeJSONProject `json:"projects"`
}

type claudeJSONProject struct {
	McpServers         map[string]ServerConfig `json:"mcpServers"`
	DisabledMcpServers []string                `json:"disabledMcpServers,omitempty"`
	EnabledMcpServers  []string                `json:"enabledMcpServers,omitempty"`
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
//  4. plugin — enabled plugin .mcp.json files (skipped when trusted=false)
//  5. conduit — ~/.conduit/mcp.json
//
// trusted mirrors the same workspace-trust flag used by FilterUntrustedHooks:
// when false (workspace not yet trusted), plugin MCP servers are not loaded.
// This prevents auto-executing plugin code from an untrusted checkout.
//
// Mirrors getMcpConfigsByScope() in src/services/mcp/config.ts.
func LoadConfigs(cwd string, trusted bool) (map[string]ServerConfig, error) {
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
	loadPluginMCPServers(merged, trusted)

	// 5. Conduit global MCP config. This is conduit's own overlay and wins
	// over Claude/project/plugin sources when server names collide.
	loadConduitMCPServers(merged)

	return merged, nil
}

func conduitMCPFile() string {
	dir := os.Getenv("CONDUIT_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return filepath.Join(".conduit", "mcp.json")
		}
		dir = filepath.Join(home, ".conduit")
	}
	return filepath.Join(dir, "mcp.json")
}

func loadConduitMCPServers(merged map[string]ServerConfig) {
	path := conduitMCPFile()
	cfg, err := loadMcpFile(path)
	if err != nil {
		return
	}
	for name, srv := range cfg.McpServers {
		srv.Source = path
		srv.Scope = "conduit"
		merged[name] = srv
	}
}

// loadPluginMCPServers reads each enabled, installed plugin's .mcp.json and
// adds its servers to merged with scope="plugin".
//
// When trusted is false the workspace has not been approved by the user, so
// plugin MCP servers are skipped entirely — matching the behavior of
// FilterUntrustedHooks which also gates project-local hook execution on trust.
func loadPluginMCPServers(merged map[string]ServerConfig, trusted bool) {
	if !trusted {
		return
	}
	enabledPlugins := loadEnabledPlugins()

	installed, err := plugins.LoadInstalledPlugins()
	if err != nil {
		return
	}

	for pluginID, entries := range installed.Plugins {
		if !pluginEnabledFromMap(pluginID, enabledPlugins) {
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

func pluginEnabledFromMap(pluginID string, enabledPlugins map[string]interface{}) bool {
	v, ok := enabledPlugins[pluginID]
	if !ok {
		return true
	}
	switch val := v.(type) {
	case bool:
		return val
	case []interface{}:
		return len(val) > 0
	default:
		return true
	}
}

func loadEnabledPlugins() map[string]interface{} {
	if cfg, err := settings.LoadConduitConfig(); err == nil && len(cfg.EnabledPlugins) > 0 {
		out := make(map[string]interface{}, len(cfg.EnabledPlugins))
		for k, v := range cfg.EnabledPlugins {
			out[k] = v
		}
		return out
	}
	home, _ := os.UserHomeDir()
	claudeHome := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	settingsPath := filepath.Join(claudeHome, "settings.json")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil
	}
	var raw struct {
		EnabledPlugins map[string]interface{} `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(settingsData, &raw); err != nil {
		return nil
	}
	return raw.EnabledPlugins
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

// isMcpjsonApproved checks the project-scope MCP approval state from the
// user/project settings.json hierarchy. Returns true if the server should
// be allowed to connect: explicitly enabled, OR enableAllProjectMcpServers
// is set, AND not explicitly disabled. Mirrors CC's MCPServerApprovalDialog
// gate (see src/components/MCPServerApprovalDialog.tsx).
func isMcpjsonApproved(name, cwd string) bool {
	merged, err := settings.Load(cwd)
	if err != nil || merged == nil {
		return false
	}
	for _, n := range merged.DisabledMcpjsonServers {
		if n == name {
			return false
		}
	}
	if merged.EnableAllProjectMcpServers {
		return true
	}
	for _, n := range merged.EnabledMcpjsonServers {
		if n == name {
			return true
		}
	}
	return false
}

// IsDisabled returns true if the named server is in disabledMcpServers for cwd.
// Conduit project state wins; Claude global config is only a compatibility
// fallback until Conduit has an explicit disabledMcpServers field for cwd.
func IsDisabled(name, cwd string) bool {
	if state, ok, err := settings.LoadConduitProjectState(cwd); err == nil && ok && state.DisabledMcpServersPresent {
		for _, d := range state.DisabledMcpServers {
			if d == name {
				return true
			}
		}
		return false
	}
	data, err := os.ReadFile(globalClaudeFile())
	if err != nil {
		return false
	}
	var cfg claudeJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	proj, ok := cfg.Projects[cwd]
	if !ok {
		proj = cfg.Projects[filepath.ToSlash(cwd)]
	}
	for _, d := range proj.DisabledMcpServers {
		if d == name {
			return true
		}
	}
	return false
}

// SetDisabled adds or removes name from disabledMcpServers in
// ~/.conduit/conduit.json → projects[cwd].
func SetDisabled(name, cwd string, disabled bool) error {
	return settings.SetConduitProjectMCPDisabled(cwd, name, disabled)
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
