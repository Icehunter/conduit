package tui

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"
)

// displayCwd is the working directory used to drop a redundant leading
// `cd <cwd> &&` from displayed commands. It is set from the cached location
// (never read from disk while rendering).
var displayCwd atomic.Pointer[string]

func setDisplayCwd(cwd string) { displayCwd.Store(&cwd) }

func currentDisplayCwd() string {
	if p := displayCwd.Load(); p != nil {
		return *p
	}
	return ""
}

func isBashTool(toolName string) bool {
	return strings.Contains(strings.ToLower(toolName), "bash")
}

// bashCommandFromInput extracts the full, untruncated command from a Bash
// tool's input. The input is either the JSON arguments (transcript rows) or
// the bare command (permission prompt).
func bashCommandFromInput(toolInput string) string {
	trimmed := strings.TrimSpace(toolInput)
	if strings.HasPrefix(trimmed, "{") {
		var args map[string]any
		if err := json.Unmarshal([]byte(trimmed), &args); err == nil {
			for _, key := range []string{"command", "cmd"} {
				if s, ok := args[key].(string); ok && s != "" {
					return s
				}
			}
			return ""
		}
	}
	return toolInput
}

var leadingCdRe = regexp.MustCompile(`(?s)^\s*cd\s+(?:"([^"]*)"|'([^']*)'|(\S+))\s*&&\s*(.+)$`)

// stripCwdPrefix removes a leading `cd <dir> &&` when dir is cwd, since the
// command already runs there and the prefix only adds noise.
func stripCwdPrefix(cmd, cwd string) string {
	if cwd == "" {
		return cmd
	}
	m := leadingCdRe.FindStringSubmatch(cmd)
	if m == nil {
		return cmd
	}
	dir := m[1] + m[2] + m[3]
	if dir == "" || strings.ContainsAny(dir, "$`") {
		return cmd
	}
	if filepath.Clean(dir) != filepath.Clean(cwd) {
		return cmd
	}
	return m[4]
}

// displayBashCommand returns the command to show for a Bash tool input.
func displayBashCommand(toolInput, cwd string) string {
	return strings.TrimSpace(stripCwdPrefix(bashCommandFromInput(toolInput), cwd))
}

// wrapBashCommand wraps a command to width columns, keeping its own newlines,
// and highlights each resulting line as shell.
func wrapBashCommand(cmd string, width int) []string {
	width = max(width, 10)
	var out []string
	for line := range strings.SplitSeq(cmd, "\n") {
		for wrapped := range strings.SplitSeq(ansi.Wrap(line, width, ""), "\n") {
			out = append(out, highlightLine(wrapped, "bash"))
		}
	}
	return out
}
