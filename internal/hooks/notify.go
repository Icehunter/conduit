package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Notify sends a desktop notification on macOS (osascript / terminal-notifier)
// and Linux (notify-send). Non-fatal — silently ignored if unavailable.
// Uses conduit.png next to the binary as the icon when present.
// Mirrors the desktop notification behavior from src/utils/hooks/ (notifs/).
func Notify(title, body string) {
	switch runtime.GOOS {
	case "darwin":
		notifyMacOS(title, body)
	case "linux":
		notifyLinux(title, body)
	}
}

func logoPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	p := filepath.Join(filepath.Dir(exe), "conduit.png")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func notifyMacOS(title, body string) {
	icon := logoPath()
	// terminal-notifier supports custom icons and is available via Homebrew.
	if tnPath, err := exec.LookPath("terminal-notifier"); err == nil {
		args := []string{"-title", title, "-message", body}
		if icon != "" {
			args = append(args, "-contentImage", icon)
		}
		_ = exec.Command(tnPath, args...).Run()
		return
	}
	// Fallback: built-in AppleScript notification (no custom icon support).
	script := `display notification "` + escapeAppleScript(body) + `" with title "` + escapeAppleScript(title) + `"`
	_ = exec.Command("osascript", "-e", script).Run()
}

func notifyLinux(title, body string) {
	icon := logoPath()
	args := []string{title, body}
	if icon != "" {
		args = append([]string{"--icon", icon}, args...)
	}
	_ = exec.Command("notify-send", args...).Run()
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
