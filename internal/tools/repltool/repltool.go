// Package repltool implements the REPL tool — an interactive code execution
// environment. Mirrors src/tools/REPLTool/.
//
// The TS original runs code in a Bun VM sandbox. Our Go port falls back to
// running the code via the system interpreter (node/python3/bash), which is
// good enough for the agent's typical use (quick calculations, data transforms,
// script snippets).
//
// When a KernelManager is provided (via NewWithKernelManager), Python and Node
// executions use a persistent kernel process per (sessionID, lang) pair so
// that variables set in one call are visible in subsequent calls.
// Bash and unsupported languages always use the subprocess-per-call path.
package repltool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/kernel"
	"github.com/icehunter/conduit/internal/tool"
)

const maxOutput = 10000

// kernelLangs is the set of language tokens that use the persistent kernel
// path when a Manager is configured. All other languages fall through to the
// subprocess-per-call path.
var kernelLangs = map[string]string{
	"python":     "python",
	"python3":    "python",
	"py":         "python",
	"javascript": "node",
	"js":         "node",
	"node":       "node",
}

// Tool implements the REPL tool.
type Tool struct {
	mgr       *kernel.Manager
	sessionID string
}

// New returns a Tool that uses subprocess-per-call for all languages.
func New() *Tool { return &Tool{} }

// NewWithKernelManager returns a Tool that uses a persistent kernel for
// Python and Node.js, and falls back to subprocess-per-call for everything
// else (bash, sh, etc.).
func NewWithKernelManager(mgr *kernel.Manager, sessionID string) *Tool {
	return &Tool{mgr: mgr, sessionID: sessionID}
}

func (*Tool) Name() string { return "REPL" }
func (*Tool) Description() string {
	return `Execute code in a REPL environment. Supports JavaScript (node), Python (python3), and Bash.

Use for:
- Quick calculations or data transformations
- Testing snippets before writing to files
- Evaluating expressions

Specify the language in the "language" field. Python and JavaScript use a persistent
kernel per session so variables set in one call are visible in subsequent calls.
Bash always runs in a fresh subprocess.`
}
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"code":     {"type": "string", "description": "Code to execute"},
			"language": {"type": "string", "description": "Language: javascript, python, bash", "default": "javascript"}
		},
		"required": ["code"]
	}`)
}
func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in struct {
		Code     string `json:"code"`
		Language string `json:"language,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult("invalid input"), nil
	}
	if strings.TrimSpace(in.Code) == "" {
		return tool.ErrorResult("code is required"), nil
	}
	lang := strings.ToLower(strings.TrimSpace(in.Language))
	if lang == "" {
		lang = "javascript"
	}

	// Persistent kernel path: Python and Node when a manager is configured.
	if t.mgr != nil {
		if kernelLang, ok := kernelLangs[lang]; ok {
			return t.executeKernel(ctx, kernelLang, in.Code)
		}
	}

	// Subprocess-per-call fallback (bash, sh, and any unrecognised language,
	// or when no manager is configured).
	return t.executeSubprocess(ctx, lang, in.Code)
}

// executeKernel runs code via the persistent kernel for the session.
func (t *Tool) executeKernel(ctx context.Context, kernelLang, code string) (tool.Result, error) {
	k, err := t.mgr.Get(t.sessionID, kernelLang)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("kernel unavailable: %s", err.Error())), nil
	}
	out, err := k.Execute(ctx, code)
	if err != nil {
		if out != "" {
			return tool.ErrorResult(fmt.Sprintf("%s\n%s", strings.TrimRight(out, "\n"), err.Error())), nil
		}
		return tool.ErrorResult(err.Error()), nil
	}
	if out == "" {
		out = "(no output)"
	}
	if len(out) > maxOutput {
		out = out[:maxOutput] + "\n[output truncated]"
	}
	return tool.TextResult(strings.TrimRight(out, "\n")), nil
}

// executeSubprocess runs code in a fresh subprocess (original per-call path).
func (t *Tool) executeSubprocess(ctx context.Context, lang, code string) (tool.Result, error) {
	ext := map[string]string{
		"javascript": ".js", "js": ".js", "node": ".js",
		"python": ".py", "python3": ".py", "py": ".py",
		"bash": ".sh", "sh": ".sh", "shell": ".sh",
	}[lang]
	if ext == "" {
		return tool.ErrorResult(fmt.Sprintf("unsupported language %q — use javascript, python, or bash", lang)), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Write code to a temp file to avoid any shell injection via -e/-c args.
	// exec.CommandContext passes the file path as a direct argv element —
	// no shell expansion happens, so the code content cannot break out.
	tmp, err := os.CreateTemp("", "claude-repl-*"+ext)
	if err != nil {
		return tool.ErrorResult("could not create temp file: " + err.Error()), nil
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(code); err != nil {
		tmp.Close()
		return tool.ErrorResult("could not write code: " + err.Error()), nil
	}
	tmp.Close()

	var cmd *exec.Cmd
	switch lang {
	case "javascript", "js", "node":
		if _, err := exec.LookPath("node"); err != nil {
			return tool.ErrorResult("node not found — install Node.js to run JavaScript"), nil
		}
		cmd = exec.CommandContext(ctx, "node", tmp.Name())
	case "python", "python3", "py":
		interp := "python3"
		if _, err := exec.LookPath("python3"); err != nil {
			interp = "python"
		}
		cmd = exec.CommandContext(ctx, interp, tmp.Name())
	case "bash", "sh", "shell":
		cmd = exec.CommandContext(ctx, "bash", tmp.Name())
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()

	result := out.String()
	if len(result) > maxOutput {
		result = result[:maxOutput] + "\n[output truncated]"
	}
	if runErr != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		}
		if result == "" {
			result = runErr.Error()
		}
		return tool.ErrorResult(fmt.Sprintf("exit %d\n%s", exitCode, strings.TrimRight(result, "\n"))), nil
	}
	if result == "" {
		result = "(no output)"
	}
	return tool.TextResult(strings.TrimRight(result, "\n")), nil
}
