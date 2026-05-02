package hooks

import (
	"os/exec"
	"runtime"
)

// Notify sends a desktop notification on macOS (osascript) and Linux (notify-send).
// Non-fatal — silently ignored if the notification system is unavailable.
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
	script := `display notification "` + escapeAppleScript(body) + `" with title "` + escapeAppleScript(title) + `"`
	_ = exec.Command("osascript", "-e", script).Run()
}

func notifyLinux(title, body string) {
	_ = exec.Command("notify-send", title, body).Run()
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
