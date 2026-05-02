// Package repltool implements the REPL tool — an interactive code execution
// environment. Mirrors src/tools/REPLTool/.
//
// The TS original runs code in a Bun VM sandbox. Our Go port falls back to
// running the code via the system interpreter (node/python3/bash), which is
// good enough for the agent's typical use (quick calculations, data transforms,
// script snippets).
package repltool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/tool"
)

const maxOutput = 10000

// Tool implements the REPL tool.
type Tool struct{}

func New() *Tool { return &Tool{} }
func (*Tool) Name() string { return "REPL" }
func (*Tool) Description() string {
	return `Execute code in a REPL environment. Supports JavaScript (node) and Python (python3).

Use for:
- Quick calculations or data transformations
- Testing snippets before writing to files
- Evaluating expressions

Specify the language in the "language" field. The code runs in a subprocess; there is no persistent state between calls.`
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
func (*Tool) IsReadOnly(json.RawMessage) bool       { return false }
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

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Write code to a temp file to avoid any shell injection via -e/-c args.
	// exec.CommandContext passes the file path as a direct argv element —
	// no shell expansion happens, so the code content cannot break out.
	ext := map[string]string{
		"javascript": ".js", "js": ".js", "node": ".js",
		"python": ".py", "python3": ".py", "py": ".py",
		"bash": ".sh", "sh": ".sh", "shell": ".sh",
	}[lang]
	if ext == "" {
		return tool.ErrorResult(fmt.Sprintf("unsupported language %q — use javascript, python, or bash", lang)), nil
	}

	tmp, err := os.CreateTemp("", "claude-repl-*"+ext)
	if err != nil {
		return tool.ErrorResult("could not create temp file: " + err.Error()), nil
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(in.Code); err != nil {
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
		if ee, ok := runErr.(*exec.ExitError); ok {
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
