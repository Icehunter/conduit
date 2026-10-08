//go:build windows

package verification

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; "+command+"; if (-not $?) { exit 1 }; if ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }")
	// procgroup.Set is a no-op on Windows. taskkill /T includes the test
	// runner's children rather than terminating only PowerShell on timeout.
	cmd.Cancel = func() error {
		killCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := exec.CommandContext(killCtx, "taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return cmd
}

// Windows taskkill handles the tree on cancellation while its parent exists.
// Configured commands must remain foreground; detached process ownership needs
// a Windows Job Object, which this adapter does not establish.
func cleanupShellProcess(*exec.Cmd) {}
