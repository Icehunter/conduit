package settings

import (
	"os"
	"path/filepath"
	"runtime"
)

// ApplyEnv sets all key=value pairs from env into the process environment.
// Returns a cleanup function that restores the previous values.
// The caller must call cleanup when the tool invocation is complete.
// Mirrors the env injection behavior from src/utils/settings/settings.ts.
func ApplyEnv(env map[string]string) func() {
	if len(env) == 0 {
		return func() {}
	}
	// Save previous values.
	prev := make(map[string]string, len(env))
	prevSet := make(map[string]bool, len(env))
	for k := range env {
		v, ok := os.LookupEnv(k)
		prev[k] = v
		prevSet[k] = ok
	}
	// Apply new values.
	for k, v := range env {
		_ = os.Setenv(k, v)
	}
	return func() {
		for k := range env {
			if prevSet[k] {
				_ = os.Setenv(k, prev[k])
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}
}

// claudeDir returns the directory where conduit stores its configuration.
// Resolution order (mirrors real Claude Code):
//   - CLAUDE_CONFIG_DIR env (explicit override for testing)
//   - Linux: $XDG_CONFIG_HOME/claude (if XDG_CONFIG_HOME is set)
//   - Windows: %APPDATA%\claude
//   - Otherwise: ~/.claude
func claudeDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	if runtime.GOOS == "linux" {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "claude")
		}
	}
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "claude")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// ClaudeDir returns the resolved configuration directory. Exported for use
// by other packages (memdir, session, etc.) that need the home path.
func ClaudeDir() string { return claudeDir() }
