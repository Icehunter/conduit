package auth

import (
	"fmt"
	"os/exec"
	"runtime"
)

// SystemBrowser opens URLs in the OS default browser using the platform's
// standard "open this in the user's default app" command.
//
// macOS:   /usr/bin/open
// Linux:   xdg-open (provided by xdg-utils on most distros)
// Windows: rundll32 url.dll,FileProtocolHandler
//
// SystemBrowser returns a non-nil error if the launcher fails immediately;
// callers should treat the error as advisory (the manual paste flow is
// always available as a fallback).
type SystemBrowser struct{}

// Open implements BrowserOpener.
//
// We Start() the launcher and immediately Release() the OS process so we
// don't carry a tracked child or a goroutine waiting for it. The launchers
// (open / xdg-open / rundll32) reparent the actual browser to PID 1, so
// abandoning them here is safe: the user's browser stays open even if our
// process exits seconds later.
func (SystemBrowser) Open(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("/usr/bin/open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("auth: open browser: %w", err)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}
