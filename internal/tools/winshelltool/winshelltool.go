// Package winshelltool implements the Shell tool for Windows (PowerShell executor).
// On non-Windows platforms the tool is registered but returns an error if invoked.
package winshelltool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/tool"
)

// Tool implements the Shell tool — a PowerShell executor for Windows.
type Tool struct {
	env map[string]string
}

// New creates a new Shell tool with optional session environment variables.
func New(env map[string]string) *Tool { return &Tool{env: env} }

// Name implements tool.Tool.
func (*Tool) Name() string { return "Shell" }

// Description implements tool.Tool.
func (*Tool) Description() string {
	return "Executes a command via PowerShell on Windows. Returns combined stdout and stderr. " +
		"Use Read/Glob/Grep/Edit for file operations; reserve this for commands that genuinely need a shell. " +
		"Provide the command in the `command` argument. Optional `timeout` (ms, default 120000, max 600000). " +
		"Optional `description` (one-line active-voice summary)."
}

// InputSchema implements tool.Tool.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command":     {"type": "string", "description": "The PowerShell command to execute"},
			"timeout":     {"type": "number", "description": "Optional timeout in milliseconds (max 600000)"},
			"description": {"type": "string", "description": "One-line active-voice summary of what this does"}
		},
		"required": ["command"]
	}`)
}

// Input is the typed view of the JSON input.
type Input struct {
	Command     string `json:"command"`
	Timeout     int    `json:"timeout,omitempty"`
	Description string `json:"description,omitempty"`
}

// IsReadOnly returns true for PowerShell commands that are known to be read-only.
func (*Tool) IsReadOnly(raw json.RawMessage) bool {
	var inp Input
	if err := json.Unmarshal(raw, &inp); err != nil || inp.Command == "" {
		return false
	}
	return isReadOnlyPSCommand(inp.Command)
}

// IsConcurrencySafe implements tool.Tool.
func (*Tool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

// Execute runs the PowerShell command and returns its output.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if runtime.GOOS != "windows" {
		return tool.ErrorResult("Shell tool (PowerShell) is only available on Windows. " +
			"Use the Bash tool on Unix/macOS."), nil
	}

	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Command) == "" {
		return tool.ErrorResult("`command` is required and cannot be empty"), nil
	}

	timeout := 120_000
	if in.Timeout > 0 && in.Timeout <= 600_000 {
		timeout = in.Timeout
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command", in.Command)

	// Inject session environment.
	if len(t.env) > 0 {
		base := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", "exit 0")
		cmd.Env = base.Environ()
		for k, v := range t.env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	out, err := cmd.CombinedOutput()
	output := strings.TrimRight(string(out), "\r\n")

	if err != nil && len(output) == 0 {
		return tool.ErrorResult(fmt.Sprintf("shell error: %v", err)), nil
	}
	if len(output) > 100_000 {
		output = output[:100_000] + "\n... (truncated)"
	}
	return tool.TextResult(output), nil
}

// isReadOnlyPSCommand returns true for PowerShell commands that only read state.
func isReadOnlyPSCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	lower := strings.ToLower(cmd)
	// Known safe read-only cmdlet verbs and commands.
	safeVerbs := []string{
		"get-", "test-path", "select-", "where-object", "format-",
		"measure-", "compare-", "find-", "resolve-", "split-path",
		"join-path", "convert-from-", "convertfrom-",
	}
	for _, v := range safeVerbs {
		if strings.HasPrefix(lower, v) {
			return true
		}
	}
	// Classic shell commands that map to PS aliases.
	for _, c := range []string{
		"git log", "git status", "git diff", "git show",
		"git branch", "git tag", "dir ", "ls ", "cat ", "type ",
	} {
		if strings.HasPrefix(lower, c) {
			return true
		}
	}
	return false
}
