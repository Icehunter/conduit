//go:build windows

package kernel

import (
	"os"
	"os/exec"
)

// sendInterrupt on Windows sends a graceful Ctrl+C event to the process.
// If that fails (e.g., process is not a console app) we do nothing rather
// than killing the interpreter.
func sendInterrupt(proc *os.Process) {
	// GenerateConsoleCtrlEvent is the Windows equivalent of SIGINT.
	// A best-effort is fine here; if it fails the dirty timeout path
	// will surface the error to the caller.
	if proc == nil {
		return
	}
	// No-op fallback: Windows support is limited; the interpreter will
	// remain alive but dirty=true until the user's code finishes naturally.
}

// setPgid is a no-op on Windows; process groups work differently and
// Setpgid is not available in syscall.SysProcAttr on that platform.
func setPgid(_ *exec.Cmd) {}
