// Package globalconfig manages Conduit's global per-installation state:
// per-project trust state and startup counters in ~/.conduit/conduit.json.
//
// Claude's ~/.claude.json is read only as an import fallback for existing
// trust decisions; Conduit never writes to it.
package globalconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/icehunter/conduit/internal/settings"
)

// ProjectConfig is the per-directory entry stored under projects[<cwd>].
// Mirrors the default object VIH in decoded/2127.js lines 31–41.
type ProjectConfig struct {
	HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted,omitempty"`
}

// GlobalConfig is the global-state subset of ~/.conduit/conduit.json.
type GlobalConfig struct {
	Projects    map[string]*ProjectConfig `json:"projects,omitempty"`
	NumStartups int                       `json:"numStartups,omitempty"`
}

var mu sync.Mutex

// configPath returns the path to Conduit's state/config file.
func configPath() string {
	return settings.ConduitSettingsPath()
}

func legacyConfigPath() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Join(v, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// Load reads Conduit's global state. A missing or corrupt file returns an empty config.
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

func loadLegacy() (*GlobalConfig, error) {
	data, err := os.ReadFile(legacyConfigPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &GlobalConfig{Projects: map[string]*ProjectConfig{}}, nil
		}
		return nil, err
	}
	var cfg GlobalConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &GlobalConfig{Projects: map[string]*ProjectConfig{}}, nil
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]*ProjectConfig{}
	}
	return &cfg, nil
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

	legacy, err := loadLegacy()
	if err == nil {
		dir := abs
		for {
			if p, ok := legacy.Projects[dir]; ok && p.HasTrustDialogAccepted {
				_ = settings.SetConduitProjectTrusted(cwd)
				return true, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return false, nil
}

// SetTrusted marks cwd as trusted in ~/.conduit/conduit.json and persists immediately.
func SetTrusted(cwd string) error {
	mu.Lock()
	defer mu.Unlock()
	return settings.SetConduitProjectTrusted(cwd)
}

// IncrementStartups bumps the startup counter in ~/.conduit/conduit.json. Best-effort.
func IncrementStartups() {
	mu.Lock()
	defer mu.Unlock()
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(configPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return
		}
	}
	var count int
	if current, ok := raw["numStartups"]; ok {
		if err := json.Unmarshal(current, &count); err != nil {
			return
		}
	}
	countRaw, err := json.Marshal(count + 1)
	if err != nil {
		return
	}
	raw["numStartups"] = countRaw
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(configPath(), append(out, '\n'), 0o600)
}
