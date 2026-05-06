package settings

import (
	"os"
	"path/filepath"
)

// UserSettingsPath returns the path to the user-global settings file.
func UserSettingsPath() string {
	return filepath.Join(claudeDir(), "settings.json")
}

// ConduitSettingsPath returns conduit's private user settings overlay. Values
// here load after Claude-compatible settings and should only be written by
// conduit-specific features.
func ConduitSettingsPath() string {
	return filepath.Join(ConduitDir(), "conduit.json")
}

// ConduitDir returns conduit's private user configuration directory.
func ConduitDir() string {
	if dir := os.Getenv("CONDUIT_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".conduit"
	}
	return filepath.Join(home, ".conduit")
}
