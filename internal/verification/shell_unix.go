//go:build !windows

package verification

import (
	"context"
	"os/exec"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "bash", "-c", command)
}

func cleanupShellProcess(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.Cancel != nil {
		// The shell may exit before a child with redirected output. Cancel is
		// installed by procgroup.Set and kills that entire group; ESRCH means
		// it already exited. Do this even after an otherwise successful check.
		_ = cmd.Cancel()
	}
}
