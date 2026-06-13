package bashtool

import "github.com/icehunter/conduit/internal/shellsafe"

// hasShellMetachars reports whether cmd contains unquoted metacharacters that
// make it unsafe to auto-approve as a single benign command. It is a thin
// wrapper around shellsafe.HasUnsafeConstructs so bashtool has one place to
// import for shell-safety classification.
func hasShellMetachars(cmd string) bool {
	return shellsafe.HasUnsafeConstructs(cmd)
}
