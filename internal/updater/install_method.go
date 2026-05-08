package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// InstallMethod identifies how the running conduit binary was installed.
type InstallMethod int

const (
	InstallUnknown InstallMethod = iota
	InstallHomebrew
	InstallScoop
	InstallWinget
	InstallGoInstall
	InstallDirect
)

// detectInstallMethod inspects os.Executable() and returns a best-guess
// install source. It only looks at path conventions — no exec, no I/O
// beyond resolving the symlink — so it's safe to call repeatedly.
func detectInstallMethod() InstallMethod {
	exe, err := os.Executable()
	if err != nil {
		return InstallUnknown
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	resolved = filepath.ToSlash(resolved)
	lower := strings.ToLower(resolved)

	switch runtime.GOOS {
	case "darwin", "linux":
		if strings.Contains(resolved, "/Cellar/") ||
			strings.HasPrefix(resolved, "/opt/homebrew/") ||
			strings.HasPrefix(resolved, "/home/linuxbrew/") ||
			strings.HasPrefix(resolved, "/usr/local/Cellar/") {
			return InstallHomebrew
		}
		if strings.Contains(resolved, "/go/bin/") {
			return InstallGoInstall
		}
	case "windows":
		if strings.Contains(lower, "\\scoop\\") || strings.Contains(lower, "/scoop/") {
			return InstallScoop
		}
		if strings.Contains(lower, "winget") || strings.Contains(lower, "windowsapps") {
			return InstallWinget
		}
		if strings.Contains(lower, "\\go\\bin\\") || strings.Contains(lower, "/go/bin/") {
			return InstallGoInstall
		}
	}
	return InstallDirect
}

// upgradeCmd returns a one-line shell hint for the given install method.
func upgradeCmd(m InstallMethod) string {
	switch m {
	case InstallHomebrew:
		return "brew upgrade conduit"
	case InstallScoop:
		return "scoop update conduit"
	case InstallWinget:
		return "winget upgrade Icehunter.conduit"
	case InstallGoInstall:
		return "go install github.com/icehunter/conduit/cmd/conduit@latest"
	case InstallDirect, InstallUnknown:
		fallthrough
	default:
		return "Download from https://github.com/Icehunter/conduit/releases/latest"
	}
}
