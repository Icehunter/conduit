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
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/rtk"
	"github.com/icehunter/conduit/internal/rtk/track"
	"github.com/icehunter/conduit/internal/tool"
)

// trackDB is the lazily-opened RTK history database (nil if unavailable).
var (
	trackOnce sync.Once
	trackDB   *track.DB
)

// SessionEnv is injected by the loop at startup from settings.Merged.Env.
// When non-nil, each subprocess inherits the base environment plus these vars.
var SessionEnv map[string]string

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

// IsReadOnly returns true for shell commands that are known to be read-only
// (ls, cat, echo, git log/status/diff/show, find, head, tail, wc, etc.).
// This prevents permission prompts for benign inspection commands, matching
// Claude Code's isReadOnlyBashCommand heuristic.
func (*Tool) IsReadOnly(raw json.RawMessage) bool {
	var inp Input
	if err := json.Unmarshal(raw, &inp); err != nil || inp.Command == "" {
		return false
	}
	return isReadOnlyCommand(inp.Command)
}

// readOnlyPrefixes are command prefixes whose base form is always safe.
// We match the first token (binary name) only, since flags don't change safety.
var readOnlyPrefixes = map[string]bool{
	"ls": true, "ll": true, "la": true, "dir": true,
	"cat": true, "bat": true, "less": true, "more": true, "head": true, "tail": true,
	"echo": true, "printf": true,
	"pwd": true, "which": true, "type": true, "whereis": true,
	"find": true, "fd": true, "locate": true,
	"wc": true, "du": true, "df": true, "stat": true, "file": true,
	"uname": true, "hostname": true, "whoami": true, "id": true, "date": true, "uptime": true,
	"ps": true, "top": true, "htop": true,
	"env": true, "printenv": true,
	"diff": true, "cmp": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"sort": true, "uniq": true, "cut": true, "awk": true, "sed": true,
	"jq": true, "yq": true, "xmllint": true,
	"python": true, "python3": true, "node": true, // read-only when just -c or --version
	"go":   true, // covered by subcommand check below
	"make": true, // covered by subcommand check below
}

// readOnlySubcommands maps binary → set of safe subcommands.
var readOnlySubcommands = map[string]map[string]bool{
	"git": {"log": true, "status": true, "diff": true, "show": true, "blame": true,
		"branch": true, "tag": true, "remote": true, "stash": true, "describe": true,
		"rev-parse": true, "ls-files": true, "shortlog": true, "reflog": true, "config": true},
	"go":    {"version": true, "env": true, "list": true, "doc": true, "vet": true},
	"cargo": {"check": true, "clippy": true, "doc": true, "test": true, "bench": true},
	"npm":   {"list": true, "ls": true, "outdated": true, "audit": true, "info": true, "view": true},
	"gh":    {"pr": true, "issue": true, "repo": true, "release": true, "run": true, "workflow": true},
	"make":  {"--dry-run": true, "-n": true, "--question": true, "-q": true},
}

func isReadOnlyCommand(cmd string) bool {
	// Strip one leading env-var assignment (FOO=bar cmd ...).
	if len(cmd) > 0 && cmd[0] != ' ' {
		eq := false
		for i, c := range cmd {
			if c == '=' {
				eq = true
				_ = i
			}
			if c == ' ' {
				if eq {
					cmd = cmd[i+1:]
				}
				break
			}
		}
	}

	// Get first token (the binary).
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	bin := fields[0]
	// Strip path prefix.
	if idx := strings.LastIndexByte(bin, '/'); idx >= 0 {
		bin = bin[idx+1:]
	}

	if readOnlyPrefixes[bin] {
		// For tools with subcommand lists, verify the subcommand is safe.
		if subs, hasSubs := readOnlySubcommands[bin]; hasSubs {
			if len(fields) < 2 {
				return false
			}
			return subs[fields[1]]
		}
		return true
	}

	// Check subcommand-based tools not in the prefix list.
	if subs, hasSubs := readOnlySubcommands[bin]; hasSubs {
		if len(fields) >= 2 {
			return subs[fields[1]]
		}
	}

	return false
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
	if len(SessionEnv) > 0 {
		base := os.Environ()
		for k, v := range SessionEnv {
			base = append(base, k+"="+v)
		}
		cmd.Env = base
	}
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
		fmt.Fprintf(&sb, "[truncated to first %d bytes]\n", MaxOutputBytes)
	}

	switch {
	case cctx.Err() == context.DeadlineExceeded:
		fmt.Fprintf(&sb, "Command timed out after %s.\n", timeout)
		return tool.ErrorResult(strings.TrimRight(sb.String(), "\n")), nil
	case ctx.Err() == context.Canceled:
		return tool.ErrorResult("Command cancelled."), nil
	case runErr != nil:
		// Non-zero exit: surface to the model as an in-band error so it
		// can correct course.
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		}
		fmt.Fprintf(&sb, "Exit code: %d\n", exitCode)
		return tool.ErrorResult(strings.TrimRight(sb.String(), "\n")), nil
	}

	if sb.Len() == 0 {
		return tool.TextResult("(no output)"), nil
	}

	output := strings.TrimRight(sb.String(), "\n")
	filtered := rtk.Filter(in.Command, output)
	if filtered.SavedBytes > 0 {
		if db := getTrackDB(); db != nil {
			_ = db.Record(track.Row{
				Command:       in.Command,
				OriginalBytes: len(filtered.Original),
				FilteredBytes: len(filtered.Filtered),
				SavedBytes:    filtered.SavedBytes,
				SavedPct:      filtered.SavingsPct,
				RecordedAt:    time.Now(),
			})
		}
	}
	return tool.TextResult(filtered.Filtered), nil
}
