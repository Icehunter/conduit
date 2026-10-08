//go:build windows

package bashtool

import "os/exec"

// setProcessGroup is a no-op on Windows: Execute refuses to run there, and
// Setpgid is not available in syscall.SysProcAttr on that platform.
func setProcessGroup(*exec.Cmd) {}
