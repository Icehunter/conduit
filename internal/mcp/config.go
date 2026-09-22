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
//  1. user    — ~/.claude.json → mcpServers  (Claude global, compat)
//  2. local-claude — ~/.claude.json → projects[cwd].mcpServers  (Claude per-project, compat)
//  3. project — every .mcp.json from filesystem root down to cwd (closer wins)
//  4. plugin  — enabled plugin .mcp.json files (skipped when trusted=false)
//  5. local   — ~/.conduit/conduit.json → projects[cwd].mcpServers (conduit per-project)
//  6. user    — ~/.conduit/mcp.json (conduit global; highest priority)
//
// trusted mirrors the same workspace-trust flag used by FilterUntrustedHooks:
// when false (workspace not yet trusted), plugin MCP servers are not loaded.
// This prevents auto-executing plugin code from an untrusted checkout.
//
// Mirrors getMcpConfigsByScope() in src/services/mcp/config.ts; conduit adds
// scopes 5 and 6 so users can manage MCP servers without touching Claude files.
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

	// 5. Conduit local scope: ~/.conduit/conduit.json projects[cwd].mcpServers.
	// Wins over plugin/project entries (closest to the user's intent).
	if cwd != "" {
		if localRaw, localPath, err := settings.LoadConduitProjectMCPServersRaw(cwd); err == nil {
			for name, encoded := range localRaw {
				var srv ServerConfig
				if err := json.Unmarshal(encoded, &srv); err != nil {
					continue
				}
				srv.Source = localPath
				srv.Scope = "local"
				merged[name] = srv
			}
		}
	}

	// 6. Conduit user MCP config (~/.conduit/mcp.json). Highest priority — wins
	// over Claude/project/plugin/local sources when server names collide.
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

// expandEnv replaces ${VAR}, ${VAR:-default}, and $VAR with environment
// values, discarding the missing-variable report. See expandEnvVars.
func expandEnv(s string) string {
	expanded, _ := expandEnvVars(s)
	return expanded
}

// expandEnvVars replaces ${VAR}, ${VAR:-default}, and $VAR in the string with
// environment values and reports the names of references that resolved to
// nothing: unset, with no default supplied.
//
// A reference that resolves to nothing is left in the string verbatim rather
// than replaced with "". This follows CC's env expansion
// (src/services/mcp/envExpansion.ts), and it matters because the expanded
// string becomes an argv entry, a URL or an Authorization header: a typo'd
// ${GITHUB_TOKN} surfaces as a literal "${GITHUB_TOKN}" the user can see in
// /mcp, where "" would surface as an opaque exec or 401 failure.
//
// Two deliberate divergences from the TS:
//
//   - The TS splits on ":-" with JS's two-arg split, which truncates rather
//     than rejoining, so ${VAR:-a:-b} defaults to "a" there. Everything after
//     the first ":-" is the default here, matching POSIX.
//   - The TS only recognizes the ${...} form. Bare $VAR is also expanded here
//     because conduit has always accepted it.
//
// A variable that is set but empty wins over its default, as in the TS and in
// POSIX's ${VAR-default}; note that POSIX ${VAR:-default} would use the
// default in that case.
func expandEnvVars(s string) (string, []string) {
	var (
		b       strings.Builder
		missing []string
	)
	// resolve reports the replacement for a reference and whether it resolved.
	resolve := func(content string) (string, bool) {
		varName, defaultValue, hasDefault := content, "", false
		if idx := strings.Index(content, ":-"); idx >= 0 {
			varName, defaultValue, hasDefault = content[:idx], content[idx+2:], true
		}
		if v, ok := os.LookupEnv(varName); ok {
			return v, true
		}
		if hasDefault {
			return defaultValue, true
		}
		missing = append(missing, varName)
		return "", false
	}

	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 == len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}

		if s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				// Unterminated "${": no reference to expand.
				b.WriteString(s[i:])
				break
			}
			raw := s[i : i+3+end]
			if content := s[i+2 : i+2+end]; content == "" {
				b.WriteString(raw)
			} else if v, ok := resolve(content); ok {
				b.WriteString(v)
			} else {
				b.WriteString(raw)
			}
			i += 3 + end
			continue
		}

		name := leadingVarName(s[i+1:])
		if name == "" {
			// "$" followed by punctuation, or "$$": not a reference.
			b.WriteByte('$')
			i++
			continue
		}
		if v, ok := resolve(name); ok {
			b.WriteString(v)
		} else {
			b.WriteString(s[i : i+1+len(name)])
		}
		i += 1 + len(name)
	}

	return b.String(), missing
}

// leadingVarName returns the longest prefix of s that is a valid unbraced
// shell variable name, or "" if s does not start with one.
func leadingVarName(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlpha := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if isAlpha || (i > 0 && isDigit) {
			continue
		}
		return s[:i]
	}
	return s
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
