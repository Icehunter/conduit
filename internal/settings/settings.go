// Package settings loads and merges Claude Code settings files.
//
// Priority order (later overrides earlier):
//  1. ~/.claude/settings.json          (user global)
//  2. <project>/.claude/settings.json  (project shared)
//  3. <project>/.claude/settings.local.json (project local, gitignored)
//
// Mirrors src/utils/config.ts and src/utils/settings/settings.ts.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Permissions is the permissions section of a settings file.
type Permissions struct {
	Allow              []string `json:"allow"`
	Deny               []string `json:"deny"`
	Ask                []string `json:"ask"`
	DefaultMode        string   `json:"defaultMode"`
	AdditionalDirs     []string `json:"additionalDirectories"`
}

// Hook is one hook entry. Type determines which fields are used.
// Mirrors src/schemas/hooks.ts (BashCommandHookSchema, HttpHookSchema,
// PromptHookSchema, AgentHookSchema).
type Hook struct {
	// Common fields
	Type          string `json:"type"`                    // "command" | "http" | "prompt" | "agent"
	StatusMessage string `json:"statusMessage,omitempty"` // spinner text while running
	If            string `json:"if,omitempty"`            // permission rule to gate firing
	TimeoutSecs   int    `json:"timeout,omitempty"`       // per-hook timeout override (seconds)
	Once          bool   `json:"once,omitempty"`          // remove after first execution
	Async         bool   `json:"async,omitempty"`         // fire-and-forget (non-blocking)

	// type="command"
	Command string `json:"command,omitempty"` // shell command

	// type="http"
	URL            string            `json:"url,omitempty"`            // POST target
	Headers        map[string]string `json:"headers,omitempty"`        // extra headers
	AllowedEnvVars []string          `json:"allowedEnvVars,omitempty"` // vars to interpolate in headers

	// type="prompt" | "agent"
	Prompt string `json:"prompt,omitempty"` // LLM prompt (may contain $ARGUMENTS)
	Model  string `json:"model,omitempty"`  // model override
}

// HookMatcher is a matcher + hooks pair.
type HookMatcher struct {
	Matcher string `json:"matcher"` // tool name or glob, "" = all
	Hooks   []Hook `json:"hooks"`
}

// HooksSettings is the hooks section.
type HooksSettings struct {
	PreToolUse  []HookMatcher `json:"PreToolUse"`
	PostToolUse []HookMatcher `json:"PostToolUse"`
	SessionStart []HookMatcher `json:"SessionStart"`
	Stop        []HookMatcher `json:"Stop"`
}

// Settings is the parsed content of one settings file.
type Settings struct {
	Permissions Permissions   `json:"permissions"`
	Hooks       HooksSettings `json:"hooks"`
	// Env holds extra environment variables for tool execution.
	Env map[string]string `json:"env"`
	// EnabledPlugins mirrors the real Claude Code enabledPlugins field.
	// Key is "pluginName@marketplace", value is true/false.
	EnabledPlugins map[string]bool `json:"enabledPlugins,omitempty"`
	// Model is the preferred model name (e.g. "claude-opus-4-7").
	Model string `json:"model,omitempty"`
	// OutputStyle is the active output style name, persisted across sessions.
	OutputStyle string `json:"outputStyle,omitempty"`
	// Theme is the active palette name (dark|light|dark-daltonized|
	// light-daltonized|dark-ansi|light-ansi). Matches Claude Code's
	// THEME_NAMES so settings.json values are interchangeable.
	Theme string `json:"theme,omitempty"`
	// ThemeOverrides applies per-field color tweaks on top of the named
	// theme. Keys are lowercase Palette field names (e.g. "accent",
	// "success"); values are #RRGGBB hex or ANSI 0-15 codes.
	// conduit-only — Claude Code ignores this field.
	ThemeOverrides map[string]string `json:"themeOverrides,omitempty"`
}

// Merged is the result of loading and merging all settings layers.
type Merged struct {
	// Allow is the combined allow list (user + project + local).
	Allow []string
	// Deny is the combined deny list.
	Deny []string
	// Ask is the combined ask list.
	Ask []string
	// DefaultMode is the effective default permission mode.
	DefaultMode string
	// Hooks is the merged hooks configuration.
	Hooks HooksSettings
	// Env is the merged environment map.
	Env map[string]string
	// AdditionalDirs is the merged set of additional allowed dirs.
	AdditionalDirs []string
	// Model is the preferred model override from settings (last layer wins).
	Model string
	// OutputStyle is the active output style name (last layer wins).
	OutputStyle string
	// Theme is the active palette name (last layer wins).
	Theme string
	// ThemeOverrides is the per-field color override map (last layer wins).
	ThemeOverrides map[string]string
}

// Load reads and merges settings from all layers for the given cwd.
func Load(cwd string) (*Merged, error) {
	return loadPaths(settingsFiles(cwd))
}

// loadPaths merges settings from an explicit list of file paths (testable).
func loadPaths(paths []string) (*Merged, error) {
	merged := &Merged{
		DefaultMode: "default",
		Env:         make(map[string]string),
	}
	for _, path := range paths {
		s, err := readFile(path)
		if err != nil {
			continue // missing or invalid file is skipped
		}
		merged.Allow = append(merged.Allow, s.Permissions.Allow...)
		merged.Deny = append(merged.Deny, s.Permissions.Deny...)
		merged.Ask = append(merged.Ask, s.Permissions.Ask...)
		if s.Permissions.DefaultMode != "" {
			merged.DefaultMode = s.Permissions.DefaultMode
		}
		merged.AdditionalDirs = append(merged.AdditionalDirs, s.Permissions.AdditionalDirs...)
		mergeHooks(&merged.Hooks, &s.Hooks)
		for k, v := range s.Env {
			merged.Env[k] = v
		}
		if s.Model != "" {
			merged.Model = s.Model
		}
		if s.OutputStyle != "" {
			merged.OutputStyle = s.OutputStyle
		}
		if s.Theme != "" {
			merged.Theme = s.Theme
		}
		if len(s.ThemeOverrides) > 0 {
			if merged.ThemeOverrides == nil {
				merged.ThemeOverrides = map[string]string{}
			}
			for k, v := range s.ThemeOverrides {
				merged.ThemeOverrides[k] = v
			}
		}
	}
	return merged, nil
}

func settingsFiles(cwd string) []string {
	paths := []string{
		filepath.Join(claudeDir(), "settings.json"),
	}
	if cwd != "" {
		paths = append(paths,
			filepath.Join(cwd, ".claude", "settings.json"),
			filepath.Join(cwd, ".claude", "settings.local.json"),
		)
	}
	return paths
}

func readFile(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func mergeHooks(dst, src *HooksSettings) {
	dst.PreToolUse = append(dst.PreToolUse, src.PreToolUse...)
	dst.PostToolUse = append(dst.PostToolUse, src.PostToolUse...)
	dst.SessionStart = append(dst.SessionStart, src.SessionStart...)
	dst.Stop = append(dst.Stop, src.Stop...)
}

// UserSettingsPath returns the path to the user-global settings file.
func UserSettingsPath() string {
	return filepath.Join(claudeDir(), "settings.json")
}

// SaveOutputStyle persists the active output style name to the user settings file.
func SaveOutputStyle(name string) error {
	path := UserSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &raw)
	}
	encoded, err := json.Marshal(name)
	if err != nil {
		return err
	}
	if name == "" {
		delete(raw, "outputStyle")
	} else {
		raw["outputStyle"] = encoded
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// SetPluginEnabled sets enabledPlugins[pluginID] in the user settings file.
func SetPluginEnabled(pluginID string, enabled bool) error {
	return updateSettingsFile(UserSettingsPath(), func(s *Settings) {
		if s.EnabledPlugins == nil {
			s.EnabledPlugins = make(map[string]bool)
		}
		s.EnabledPlugins[pluginID] = enabled
	})
}

// RemovePlugin removes a plugin from enabledPlugins in the user settings file.
func RemovePlugin(pluginID string) error {
	return updateSettingsFile(UserSettingsPath(), func(s *Settings) {
		if s.EnabledPlugins != nil {
			delete(s.EnabledPlugins, pluginID)
		}
	})
}

// updateSettingsFile reads, mutates only enabledPlugins, and writes the settings file.
// It uses a raw JSON map so that unknown fields (and fields written by real Claude Code
// in formats we don't model, like null arrays or non-standard enum values) are preserved
// exactly — we never clobber them by round-tripping through our typed Settings struct.
func updateSettingsFile(path string, fn func(*Settings)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Read the raw JSON as an opaque map so unknown fields survive.
	raw := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &raw)
	}

	// Extract just the enabledPlugins section so fn can operate on it.
	var s Settings
	if ep, ok := raw["enabledPlugins"]; ok {
		_ = json.Unmarshal(ep, &s.EnabledPlugins)
	}

	fn(&s)

	// Write enabledPlugins back into the raw map.
	if s.EnabledPlugins == nil {
		delete(raw, "enabledPlugins")
	} else {
		ep, err := json.Marshal(s.EnabledPlugins)
		if err != nil {
			return err
		}
		raw["enabledPlugins"] = ep
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// SaveRawKey persists an arbitrary key/value to the user settings file using
// raw-map preservation so no other fields are disturbed.
func SaveRawKey(key string, value interface{}) error {
	path := UserSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &raw)
	}
	if value == nil {
		delete(raw, key)
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		raw[key] = encoded
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
