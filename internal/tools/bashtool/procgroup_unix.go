//go:build !windows

package bashtool

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup runs cmd in its own process group and makes cancellation
// kill the whole group, so the children a command starts (a test runner's
// workers, a backgrounded sleep) die with it on timeout or cancel instead of
// outliving the tool call. Follows the setPgid pattern in internal/kernel.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		// Negative PID signals every process in the group.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
