package hooks

import (
	"os/exec"
	"runtime"

	"github.com/icehunter/conduit/internal/assets"
)

// Notify sends a desktop notification on macOS (osascript) and Linux
// (notify-send). Non-fatal — silently ignored if unavailable.
// Mirrors the desktop notification behavior from src/utils/hooks/ (notifs/).
func Notify(title, body string) {
	switch runtime.GOOS {
	case "darwin":
		notifyMacOS(title, body)
	case "linux":
		notifyLinux(title, body)
	}
}

func notifyMacOS(title, body string) {
	// Native AppleScript notification — no extra dependencies, works on all
	// macOS versions. Custom app icon requires a .app bundle which we don't
	// have, so the icon is always the calling app's icon (Terminal/iTerm/etc).
	script := `display notification "` + escapeAppleScript(body) + `" with title "` + escapeAppleScript(title) + `"`
	_ = exec.Command("osascript", "-e", script).Run() //nolint:noctx
}

func notifyLinux(title, body string) {
	args := []string{title, body}
	if icon := assets.IconPath(); icon != "" {
		args = append([]string{"--icon", icon}, args...)
	}
	_ = exec.Command("notify-send", args...).Run() //nolint:noctx
}

// escapeAppleScript escapes double quotes and backslashes for AppleScript string literals.
func escapeAppleScript(s string) string {
	out := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
