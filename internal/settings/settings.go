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

// Hook is one hook command entry.
type Hook struct {
	Type    string `json:"type"`    // "command"
	Command string `json:"command"` // shell command to run
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
	}
	return merged, nil
}

func settingsFiles(cwd string) []string {
	home, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(home, ".claude", "settings.json"),
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
