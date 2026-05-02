// Package undercover implements undercover mode — safety instructions for
// commits/PRs to public repos, preventing AI attribution leaks.
//
// Mirrors src/utils/undercover.ts. In the TS source all logic is gated on
// USER_TYPE==='ant' (Anthropic internal builds only); external builds return
// trivial values. Here we expose the env-override path (CLAUDE_CODE_UNDERCOVER)
// so any user can opt in when working on public repos.
package undercover

import (
	"os"
	"strings"
)

// IsUndercover reports whether undercover mode is active.
// Activated by setting CLAUDE_CODE_UNDERCOVER=1 (or true/yes/on).
// Off by default in external builds (no auto-detection of internal repos).
func IsUndercover() bool {
	return isTruthy(os.Getenv("CLAUDE_CODE_UNDERCOVER"))
}

// GetUndercoverInstructions returns the system-prompt block injected when
// undercover mode is active. Empty string when mode is off.
func GetUndercoverInstructions() string {
	if !IsUndercover() {
		return ""
	}
	return `## UNDERCOVER MODE — CRITICAL

You are operating UNDERCOVER in a PUBLIC/OPEN-SOURCE repository. Your commit
messages, PR titles, and PR bodies MUST NOT contain ANY AI attribution or
internal information. Do not blow your cover.

NEVER include in commit messages or PR descriptions:
- Any mention that you are an AI or that AI was used
- The phrase "Claude Code" or any AI tool name
- "Co-Authored-By: Claude" or any AI attribution lines
- Model names or version numbers
- Internal codenames or project references

Write commit messages as a human developer would — describe only what the
code change does.

GOOD:
- "Fix race condition in file watcher initialization"
- "Add support for custom key bindings"
- "Refactor parser for better error messages"

BAD (never write these):
- "Generated with Claude Code"
- "Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
- "1-shotted by claude-sonnet-4-6"
`
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
