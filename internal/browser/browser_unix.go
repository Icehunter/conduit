//go:build !windows

package browser

import (
	"os/exec"
	"runtime"
)

func openBrowser(url string) error {
	var cmd string
	if runtime.GOOS == "darwin" {
		cmd = "open"
	} else {
		cmd = "xdg-open"
	}
	return exec.Command(cmd, url).Start() //nolint:noctx // fire-and-forget browser open; no context cancellation needed
}
