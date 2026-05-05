// Package globalconfig manages ~/.claude.json — the global per-installation
// config that stores per-project trust state and startup counters.
//
// Mirrors the shape used by Claude Code v2.1.126:
//   - decoded/2126.js (IsTrusted logic, ancestor walk)
//   - decoded/2127.js (per-project config defaults)
//   - decoded/0635.js (config file path: CLAUDE_CONFIG_DIR or ~/.claude.json)
//
// This file is distinct from ~/.claude/settings.json (permission rules,
// hooks, env vars). ~/.claude.json is the global config; settings.json
// files are per-layer (user/project/local) permission config.
package globalconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// ProjectConfig is the per-directory entry stored under projects[<cwd>].
// Mirrors the default object VIH in decoded/2127.js lines 31–41.
type ProjectConfig struct {
	HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted,omitempty"`
}

// GlobalConfig is the shape of ~/.claude.json.
type GlobalConfig struct {
	Projects    map[string]*ProjectConfig `json:"projects,omitempty"`
	NumStartups int                       `json:"numStartups,omitempty"`
}

var mu sync.Mutex

// configPath returns the path to ~/.claude.json.
// Resolution mirrors decoded/0635.js:
//   - If CLAUDE_CONFIG_DIR is set → $CLAUDE_CONFIG_DIR/.claude.json
//   - Otherwise → $HOME/.claude.json
func configPath() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Join(v, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// Load reads ~/.claude.json. A missing or corrupt file returns an empty config.
func Load() (*GlobalConfig, error) {
	mu.Lock()
	defer mu.Unlock()
	return load()
}

func load() (*GlobalConfig, error) {
	data, err := os.ReadFile(configPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &GlobalConfig{Projects: map[string]*ProjectConfig{}}, nil
		}
		return nil, err
	}
	var cfg GlobalConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		// Corrupt file — start fresh rather than blocking startup.
		return &GlobalConfig{Projects: map[string]*ProjectConfig{}}, nil
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]*ProjectConfig{}
	}
	return &cfg, nil
}

func save(cfg *GlobalConfig) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// IsTrusted reports whether cwd (or any ancestor) has been marked trusted.
// Also returns true when CLAUDE_CODE_SANDBOXED is set (CI / container bypass).
// Mirrors decoded/2126.js lines 90–107.
func IsTrusted(cwd string) (bool, error) {
	if os.Getenv("CLAUDE_CODE_SANDBOXED") != "" {
		return true, nil
	}

	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}

	mu.Lock()
	defer mu.Unlock()

	cfg, err := load()
	if err != nil {
		return false, err
	}

	// Walk from cwd up to root; any ancestor acceptance implies child trust.
	dir := abs
	for {
		if p, ok := cfg.Projects[dir]; ok && p.HasTrustDialogAccepted {
			return true, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return false, nil
}

// SetTrusted marks cwd as trusted in ~/.claude.json and persists immediately.
func SetTrusted(cwd string) error {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}

	mu.Lock()
	defer mu.Unlock()

	cfg, err := load()
	if err != nil {
		return err
	}
	if cfg.Projects[abs] == nil {
		cfg.Projects[abs] = &ProjectConfig{}
	}
	cfg.Projects[abs].HasTrustDialogAccepted = true
	return save(cfg)
}

// IncrementStartups bumps the startup counter in ~/.claude.json. Best-effort.
func IncrementStartups() {
	mu.Lock()
	defer mu.Unlock()
	cfg, err := load()
	if err != nil {
		return
	}
	cfg.NumStartups++
	_ = save(cfg)
}
