//go:build !windows

package workflow

import (
	"errors"
	"syscall"
)

// pidAlive reports whether a process with this pid exists.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
