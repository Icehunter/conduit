//go:build windows

package browser

import "os/exec"

func openBrowser(url string) error {
	return exec.Command("cmd", "/c", "start", url).Start()
}
