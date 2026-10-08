//go:build windows

// Package procgroup runs a command in its own process group so cancelling it
// takes the whole tree down.
package procgroup

import "os/exec"

// Set is a no-op on Windows: the Bash tool refuses to run there, and Setpgid
// is not available in syscall.SysProcAttr on that platform.
func Set(*exec.Cmd) {}
