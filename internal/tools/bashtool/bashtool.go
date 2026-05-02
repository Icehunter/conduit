// Package bashtool implements the M2 reference port of Claude Code's
// Bash tool. It executes a shell command via `bash -c` and returns the
// combined stdout/stderr.
//
// This is a deliberately minimal port. The real BashTool ships ~10k LOC
// of pathValidation, bashSecurity, bashPermissions, sandbox routing, and
// run_in_background plumbing; that surface lands in M5 alongside the
// permission system. M2's BashTool is enough to round-trip a tool call
// through the agent loop with real subprocess execution.
//
// Reference: src/tools/BashTool/BashTool.tsx (~157 KB), src/tools/BashTool/toolName.ts.
package bashtool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/icehunter/claude-go/internal/tool"
)

// DefaultTimeout matches the leaked TS reference (BashTool.tsx ~882 line
// `timeout: timeoutMs`) when no timeout argument is supplied: 2 minutes.
const DefaultTimeout = 2 * time.Minute

// MaxTimeout caps `timeout` so a runaway tool call can't hold the agent
// loop hostage. Real BashTool exposes 10 min.
const MaxTimeout = 10 * time.Minute

// MaxOutputBytes truncates combined stdout+stderr to keep tool_result
// blocks under typical context budgets. Matches the real BashTool's
// `maxResultSizeChars` of ~30000.
const MaxOutputBytes = 30000

// Tool implements the Bash tool.
type Tool struct{}

// New returns a fresh Bash tool. Stateless; one instance is fine.
func New() *Tool { return &Tool{} }

// Name implements tool.Tool.
func (*Tool) Name() string { return "Bash" }

// Description is the prompt text the model sees.
func (*Tool) Description() string {
	return "Executes a given bash command in a fresh shell and returns the combined stdout and stderr. " +
		"Provide the command in the `command` argument. Optional `timeout` (ms, default 120000, max 600000). " +
		"Optional `description` (one-line active-voice summary)."
}

// InputSchema is the JSON Schema sent to the model.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command":     {"type": "string", "description": "The command to execute"},
			"timeout":     {"type": "number", "description": "Optional timeout in milliseconds (max 600000)"},
			"description": {"type": "string", "description": "One-line active-voice summary of what this does"}
		},
		"required": ["command"]
	}`)
}

// IsReadOnly: Bash is never read-only.
func (*Tool) IsReadOnly(json.RawMessage) bool { return false }

// IsConcurrencySafe: Bash is never concurrency-safe (commands may write
// to shared state).
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Input is the typed view of the JSON input.
type Input struct {
	Command     string `json:"command"`
	Timeout     int    `json:"timeout,omitempty"`     // milliseconds
	Description string `json:"description,omitempty"`
}

// Execute runs the command and returns its output.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Command) == "" {
		return tool.ErrorResult("`command` is required and cannot be empty"), nil
	}

	timeout := DefaultTimeout
	if in.Timeout > 0 {
		timeout = time.Duration(in.Timeout) * time.Millisecond
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Use exec.CommandContext so cancel-after-timeout sends SIGKILL.
	// `bash -c` matches the real tool's shell-out behavior.
	cmd := exec.CommandContext(cctx, "bash", "-c", in.Command)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	runErr := cmd.Run()

	out := combined.Bytes()
	truncated := false
	if len(out) > MaxOutputBytes {
		out = out[:MaxOutputBytes]
		truncated = true
	}

	var sb strings.Builder
	if len(out) > 0 {
		sb.Write(out)
		if !bytes.HasSuffix(out, []byte{'\n'}) {
			sb.WriteByte('\n')
		}
	}
	if truncated {
		sb.WriteString(fmt.Sprintf("[truncated to first %d bytes]\n", MaxOutputBytes))
	}

	switch {
	case cctx.Err() == context.DeadlineExceeded:
		sb.WriteString(fmt.Sprintf("Command timed out after %s.\n", timeout))
		return tool.ErrorResult(strings.TrimRight(sb.String(), "\n")), nil
	case ctx.Err() == context.Canceled:
		return tool.ErrorResult("Command cancelled."), nil
	case runErr != nil:
		// Non-zero exit: surface to the model as an in-band error so it
		// can correct course.
		exitCode := -1
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
		sb.WriteString(fmt.Sprintf("Exit code: %d\n", exitCode))
		return tool.ErrorResult(strings.TrimRight(sb.String(), "\n")), nil
	}

	if sb.Len() == 0 {
		// Empty output is still success.
		sb.WriteString("(no output)")
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}
