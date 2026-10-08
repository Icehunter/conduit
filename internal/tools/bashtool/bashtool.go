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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/rtk"
	"github.com/icehunter/conduit/internal/rtk/track"
	"github.com/icehunter/conduit/internal/sessionstats"
	"github.com/icehunter/conduit/internal/shellsafe"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/truncate"
)

// trackDB is the lazily-opened RTK history database (nil if unavailable).
var (
	trackOnce sync.Once
	trackDB   *track.DB
)

func getTrackDB() *track.DB {
	trackOnce.Do(func() {
		db, err := track.Open()
		if err == nil {
			trackDB = db
		}
	})
	return trackDB
}

// DefaultTimeout matches the leaked TS reference (BashTool.tsx ~882 line
// `timeout: timeoutMs`) when no timeout argument is supplied: 2 minutes.
const DefaultTimeout = 2 * time.Minute

// MaxTimeout caps `timeout` so a runaway tool call can't hold the agent
// loop hostage. Real BashTool exposes 10 min.
const MaxTimeout = 10 * time.Minute

// MaxOutputBytes bounds the combined stdout+stderr the model sees, split
// between the head and the tail of longer output, to keep tool_result
// blocks under typical context budgets. Matches the real BashTool's
// `maxResultSizeChars` of ~30000. Output past it is saved in full to a spill
// file the result names.
const MaxOutputBytes = 30000

// Tool implements the Bash tool.
type Tool struct {
	tool.NotDeferrable
	// env holds session-level environment variables (from settings.Merged.Env)
	// that each subprocess should inherit in addition to the process environment.
	env map[string]string
}

// New returns a Bash tool that injects env into every subprocess it spawns.
// Pass nil (or an empty map) when no extra variables are needed.
func New(env map[string]string) *Tool { return &Tool{env: env} }

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

// IsReadOnly returns true for shell commands that are known to be read-only
// (ls, cat, echo, git log/status/diff/show, find, head, tail, wc, etc.).
// This prevents permission prompts for benign inspection commands, matching
// Claude Code's isReadOnlyBashCommand heuristic.
func (*Tool) IsReadOnly(raw json.RawMessage) bool {
	var inp Input
	if err := json.Unmarshal(raw, &inp); err != nil || inp.Command == "" {
		return false
	}
	return shellsafe.IsReadOnly(inp.Command)
}

func isReadOnlyCommand(cmd string) bool {
	return shellsafe.IsReadOnly(cmd)
}

// IsConcurrencySafe: Bash is never concurrency-safe (commands may write
// to shared state).
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Input is the typed view of the JSON input.
type Input struct {
	Command     string `json:"command"`
	Timeout     int    `json:"timeout,omitempty"` // milliseconds
	Description string `json:"description,omitempty"`
}

// Execute runs the command and returns its output.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if runtime.GOOS == "windows" {
		return tool.ErrorResult(
			"Bash is not available on Windows. " +
				"Install WSL (Windows Subsystem for Linux) or Git Bash and ensure `bash` is on your PATH.",
		), nil
	}
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

	// Use exec.CommandContext so cancel-after-timeout sends SIGKILL, to the
	// whole process group so the command's children die with it.
	// `bash -c` matches the real tool's shell-out behavior.
	cmd := exec.CommandContext(cctx, "bash", "-c", in.Command)
	setProcessGroup(cmd)
	// A backgrounded child (`server &`) inherits the output pipe and keeps it
	// open after bash exits; without a WaitDelay, Run waits for that child
	// and neither the timeout nor a cancel can end the call.
	cmd.WaitDelay = waitDelay
	cmd.Dir = tool.CwdFromContext(ctx)
	// Inherit process env + any session-level env injected from settings.
	if len(t.env) > 0 {
		base := os.Environ()
		for k, v := range t.env {
			base = append(base, k+"="+v)
		}
		cmd.Env = base
	}
	// The capture keeps all output: small output in memory, larger output in
	// a spill file. It only cancels the command (killing it) past the disk
	// cap, so long builds and test runs are never cut short for being loud.
	capture := newOutputCapture(cancel)
	cmd.Stdout = capture
	cmd.Stderr = capture

	runErr := cmd.Run()
	capture.finish()
	if errors.Is(runErr, exec.ErrWaitDelay) {
		// bash exited cleanly; only a background child still held the pipe.
		runErr = nil
	}

	maxBytes, maxLines := outputBudget()
	switch {
	case ctx.Err() == context.Canceled:
		capture.discard()
		return tool.ErrorResult("Command cancelled."), nil
	case capture.wasKilled():
		return tool.ErrorResult(killedReport(capture, maxBytes, maxLines)), nil
	case cctx.Err() == context.DeadlineExceeded:
		// Report whatever was captured, bounded the same way as a finished
		// command's output, but without RTK: a filter that summarizes a
		// complete run can misread a partial one.
		var sb strings.Builder
		if out := boundedOutput(capture, maxBytes, maxLines); out != "" {
			sb.WriteString(out)
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "Command timed out after %s.", timeout)
		return tool.ErrorResult(sb.String()), nil
	}

	var out, handle string
	if raw, ok := capture.inMemory(); ok {
		// RTK filters the whole output before it is bounded, so the bound
		// never discards content RTK would have kept.
		filtered := rtk.Filter(in.Command, strings.TrimRight(raw, "\n"))
		if filtered.SavedBytes > 0 {
			// Record RTK savings metrics. Use the pre-footer filtered size so the
			// recovery hint bytes do not distort /rtk gain analytics.
			sessionstats.SessionMetrics.RecordRTK(filtered.SavedBytes)
			if db := getTrackDB(); db != nil {
				_ = db.Record(track.Row{
					Command:       in.Command,
					OriginalBytes: len(filtered.Original),
					FilteredBytes: len(filtered.Filtered), // pre-footer size
					SavedBytes:    filtered.SavedBytes,
					SavedPct:      filtered.SavingsPct,
					RecordedAt:    time.Now(),
				})
			}
		}
		out = fitOutput(filtered.Filtered, maxBytes, maxLines)
		handle = filtered.Handle
	} else {
		// Too large to hold, so too large for RTK: show both ends of the raw
		// stream and point at the spill file for the rest.
		out = capture.excerpt(maxBytes, maxLines, true)
	}

	var sb strings.Builder
	if out != "" {
		sb.WriteString(out)
		sb.WriteByte('\n')
	}
	// The CCR recovery footer goes after the bounded output so the handle
	// hint is never cut.
	if handle != "" {
		fmt.Fprintf(&sb, "[full output compressed; recover with CCRRetrieve handle=%q]\n", handle)
	}

	if runErr != nil {
		// Non-zero exit: surface to the model as an in-band error so it
		// can correct course.
		exitCode := -1
		if ee, ok := errors.AsType[*exec.ExitError](runErr); ok {
			exitCode = ee.ExitCode()
		}
		fmt.Fprintf(&sb, "Exit code: %d\n", exitCode)
		return tool.ErrorResult(strings.TrimRight(sb.String(), "\n")), nil
	}

	if sb.Len() == 0 {
		return tool.TextResult("(no output)"), nil
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// outputBudget returns how many bytes and lines of output the model sees:
// MaxOutputBytes, tightened by the user's tool-output limits.
func outputBudget() (maxBytes, maxLines int) {
	lines, size := truncate.Limits()
	return min(MaxOutputBytes, size), lines
}

// boundedOutput renders the captured output within the budget, saving the
// full output to a spill file when it does not fit.
func boundedOutput(c *outputCapture, maxBytes, maxLines int) string {
	if raw, ok := c.inMemory(); ok {
		return fitOutput(strings.TrimRight(raw, "\n"), maxBytes, maxLines)
	}
	return c.excerpt(maxBytes, maxLines, true)
}

// killedReport describes a command killed for passing the disk cap: both
// ends of what it wrote first, and where all of that was saved.
func killedReport(c *outputCapture, maxBytes, maxLines int) string {
	path, err := c.spillPath()
	partial := "partial output: " + path
	if path == "" {
		partial = fmt.Sprintf("partial output unavailable: %v", err)
	}
	return fmt.Sprintf("%s\n[killed: output exceeded %s; %s]",
		c.excerpt(maxBytes, maxLines, false), formatSize(c.diskCap), partial)
}

// formatSize renders a byte count in whole megabytes when it is one.
func formatSize(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}
