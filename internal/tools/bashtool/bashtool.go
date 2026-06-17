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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/iox"
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

// MaxOutputBytes truncates combined stdout+stderr to keep tool_result
// blocks under typical context budgets. Matches the real BashTool's
// `maxResultSizeChars` of ~30000.
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

	// Use exec.CommandContext so cancel-after-timeout sends SIGKILL.
	// `bash -c` matches the real tool's shell-out behavior.
	cmd := exec.CommandContext(cctx, "bash", "-c", in.Command)
	// Inherit process env + any session-level env injected from settings.
	if len(t.env) > 0 {
		base := os.Environ()
		for k, v := range t.env {
			base = append(base, k+"="+v)
		}
		cmd.Env = base
	}
	// LimitWriter caps the output buffer before cmd.Run() returns, preventing
	// a subprocess that spews gigabytes from filling RAM. The hard cap is 4×
	// MaxOutputBytes; on overflow cancel() is called immediately, which sends
	// SIGKILL to the subprocess via exec.CommandContext.
	var combined bytes.Buffer
	lw := &iox.AtomicLimitWriter{
		W:          &combined,
		Limit:      int64(MaxOutputBytes) * 4,
		OnOverflow: cancel,
	}
	cmd.Stdout = lw
	cmd.Stderr = lw

	runErr := cmd.Run()

	rawOut := combined.Bytes()
	overflowed := lw.Overflow.Load()

	switch {
	case cctx.Err() == context.DeadlineExceeded:
		// Build timeout response from whatever we captured.
		var sb strings.Builder
		if len(rawOut) > 0 {
			out := rawOut
			if len(out) > MaxOutputBytes {
				out = out[:MaxOutputBytes]
			}
			sb.Write(out)
			if !bytes.HasSuffix(out, []byte{'\n'}) {
				sb.WriteByte('\n')
			}
		}
		fmt.Fprintf(&sb, "Command timed out after %s.\n", timeout)
		return tool.ErrorResult(strings.TrimRight(sb.String(), "\n")), nil
	case ctx.Err() == context.Canceled:
		return tool.ErrorResult("Command cancelled."), nil
	}

	// Step 1: RTK filter on the raw (possibly very large) output so the filter
	// can see the full stream before we hard-cap it. RTK classifiers often
	// achieve 10x reduction, so filtering first means the tail-drop cap never
	// discards content that RTK would have kept.
	// If overflowed, the tail was silently dropped before RTK; no stderr print
	// here because writing to os.Stderr while the TUI is active corrupts the
	// alt-screen display. The RTK footer appended below is visible in the model.
	rawOutput := strings.TrimRight(string(rawOut), "\n")
	filtered := rtk.Filter(in.Command, rawOutput)
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

	// Step 2: hard-cap at MaxOutputBytes AFTER RTK has had a chance to reduce.
	rtkOut := filtered.Filtered
	hardCapped := overflowed
	if len(rtkOut) > MaxOutputBytes {
		rtkOut = rtkOut[:MaxOutputBytes]
		hardCapped = true
	}

	var sb strings.Builder
	if len(rtkOut) > 0 {
		sb.WriteString(rtkOut)
		if !strings.HasSuffix(rtkOut, "\n") {
			sb.WriteByte('\n')
		}
	}
	if hardCapped {
		fmt.Fprintf(&sb, "[truncated to first %d bytes]\n", MaxOutputBytes)
	}
	// Append the CCR recovery footer AFTER the hard-cap so the handle hint is
	// never silently truncated mid-string.
	if filtered.Handle != "" {
		fmt.Fprintf(&sb, "[full output compressed; recover with CCRRetrieve handle=%q]\n", filtered.Handle)
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

	// Step 3: truncate-to-disk for large outputs. HasTask=true so the hint
	// tells the model it can delegate large file inspection to a Task sub-agent.
	finalOutput := strings.TrimRight(sb.String(), "\n")
	maxLines, maxBytes := truncate.Limits()
	tr, _ := truncate.Apply(finalOutput, truncate.Options{
		MaxLines:  maxLines,
		MaxBytes:  maxBytes,
		Direction: "tail", // bash output: most recent (tail) is usually most relevant
		HasTask:   true,
	})
	return tool.TextResult(tr.Content), nil
}
