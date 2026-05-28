package memdir

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

const userProfileName = "user.md"

// UserProfilePath returns the path to the global USER.md.
// Lives at ~/.conduit/user.md — follows the user across all projects.
func UserProfilePath() string {
	return filepath.Join(settings.ConduitDir(), userProfileName)
}

// ProjectUserProfilePath returns the per-project USER.md path.
// Lives at <projectDir>/memory/USER.md inside the conduit projects tree.
func ProjectUserProfilePath(projectDir string) string {
	return filepath.Join(projectDir, "memory", "USER.md")
}

// userProfileCache memoizes LoadUserProfile results keyed on the combined
// mtime of global + project USER.md. Same pattern as promptCache in memdir.go.
var userProfileCache struct {
	mu         sync.Mutex
	projectDir string
	globalMT   time.Time
	projectMT  time.Time
	result     string
}

// LoadUserProfile reads and merges the global (~/.conduit/user.md) and
// per-project (<projectDir>/memory/USER.md) USER.md files.
//
// Resolution order (global first, project appended):
//  1. ~/.conduit/user.md
//  2. <projectDir>/memory/USER.md  (only when projectDir != "")
//
// Returns "" if neither file exists. Results are mtime-cached to avoid
// redundant disk reads on every turn.
func LoadUserProfile(projectDir string) string {
	globalPath := UserProfilePath()
	projectPath := ""
	if projectDir != "" {
		projectPath = ProjectUserProfilePath(projectDir)
	}

	var globalMT, projectMT time.Time
	if info, err := os.Stat(globalPath); err == nil {
		globalMT = info.ModTime()
	}
	if projectPath != "" {
		if info, err := os.Stat(projectPath); err == nil {
			projectMT = info.ModTime()
		}
	}

	userProfileCache.mu.Lock()
	if userProfileCache.projectDir == projectDir &&
		userProfileCache.globalMT.Equal(globalMT) &&
		userProfileCache.projectMT.Equal(projectMT) &&
		// Require at least one mtime to be non-zero so a "neither exists" state
		// doesn't incorrectly cache across runs where a file appears later.
		(!globalMT.IsZero() || !projectMT.IsZero()) {
		cached := userProfileCache.result
		userProfileCache.mu.Unlock()
		return cached
	}
	userProfileCache.mu.Unlock()

	var parts []string

	if data, err := os.ReadFile(globalPath); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			parts = append(parts, s)
		}
	}

	if projectPath != "" {
		if data, err := os.ReadFile(projectPath); err == nil {
			if s := strings.TrimSpace(string(data)); s != "" {
				// Only append if it's genuinely different content from the global
				// (same file content deduplicated — e.g. a symlink edge case).
				if len(parts) == 0 || parts[0] != s {
					parts = append(parts, s)
				}
			}
		}
	}

	result := strings.Join(parts, "\n\n")

	userProfileCache.mu.Lock()
	userProfileCache.projectDir = projectDir
	userProfileCache.globalMT = globalMT
	userProfileCache.projectMT = projectMT
	userProfileCache.result = result
	userProfileCache.mu.Unlock()

	return result
}
