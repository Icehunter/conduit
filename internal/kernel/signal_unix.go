//go:build !windows

package kernel

import (
	"os"
	"os/exec"
	"syscall"
)

// sendInterrupt sends SIGINT to the process group of proc so that long-running
// code inside the interpreter is interrupted without terminating the interpreter
// itself (which uses a separate PGID).
func sendInterrupt(proc *os.Process) {
	if proc == nil {
		return
	}
	// Negative PID sends to the entire process group.
	_ = syscall.Kill(-proc.Pid, syscall.SIGINT)
}

// setPgid sets Setpgid on cmd so the spawned interpreter gets its own process
// group. This is required so sendInterrupt (SIGINT to -pgid) doesn't propagate
// to the conduit parent process.
func setPgid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
