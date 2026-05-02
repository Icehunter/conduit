// Package hooks implements PreToolUse, PostToolUse, SessionStart, and Stop
// hook runners.
//
// Mirrors src/utils/hooks/ from the TS source. Hooks are shell commands
// defined in settings.json under the `hooks` key. They run synchronously
// before/after tool calls and can block execution by returning non-zero.
//
// Hook input is written as JSON to the hook's stdin.
// stdout is parsed for optional JSON directives:
//   {"decision": "block", "reason": "..."}  — blocks the tool call
//   {"decision": "approve"}                  — approves without further prompting
//   (anything else / no JSON)                — no effect, hook is advisory
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// HookEvent identifies when a hook fires.
type HookEvent string

const (
	EventPreToolUse  HookEvent = "PreToolUse"
	EventPostToolUse HookEvent = "PostToolUse"
	EventSessionStart HookEvent = "SessionStart"
	EventStop        HookEvent = "Stop"
)

// HookInput is the JSON payload sent to hook stdin.
type HookInput struct {
	SessionID string         `json:"session_id"`
	ToolName  string         `json:"tool_name,omitempty"`
	ToolInput map[string]any `json:"tool_input,omitempty"`
	Output    string         `json:"tool_response,omitempty"`
}

// HookOutput is the optional JSON a hook writes to stdout.
type HookOutput struct {
	Decision string `json:"decision"` // "approve" | "block" | ""
	Reason   string `json:"reason"`
}

// Result is the outcome of running a hook set.
type Result struct {
	// Blocked is true if any hook returned decision=block.
	Blocked bool
	// Reason is the block reason if Blocked.
	Reason string
	// Approved is true if a hook returned decision=approve, bypassing further
	// permission prompts for this tool call.
	Approved bool
}

// RunPreToolUse runs all PreToolUse hooks matching toolName.
// Returns a Result; if Result.Blocked, the tool call should be aborted.
func RunPreToolUse(ctx context.Context, hooks []settings.HookMatcher, sessionID, toolName string, toolInput map[string]any) Result {
	input := HookInput{
		SessionID: sessionID,
		ToolName:  toolName,
		ToolInput: toolInput,
	}
	return runMatching(ctx, hooks, toolName, input)
}

// RunPostToolUse runs all PostToolUse hooks matching toolName.
func RunPostToolUse(ctx context.Context, hooks []settings.HookMatcher, sessionID, toolName, output string) Result {
	input := HookInput{
		SessionID: sessionID,
		ToolName:  toolName,
		Output:    output,
	}
	return runMatching(ctx, hooks, toolName, input)
}

// RunSessionStart runs all SessionStart hooks. Results are advisory — never blocks.
func RunSessionStart(ctx context.Context, hooks []settings.HookMatcher, sessionID string) {
	input := HookInput{SessionID: sessionID}
	runMatching(ctx, hooks, "", input)
}

// RunStop runs all Stop hooks. Results are advisory — never blocks.
func RunStop(ctx context.Context, hooks []settings.HookMatcher, sessionID string) {
	input := HookInput{SessionID: sessionID}
	runMatching(ctx, hooks, "", input)
}

func runMatching(ctx context.Context, matchers []settings.HookMatcher, toolName string, input HookInput) Result {
	for _, m := range matchers {
		if !matchesTool(m.Matcher, toolName) {
			continue
		}
		for _, hook := range m.Hooks {
			if hook.Type != "command" || hook.Command == "" {
				continue
			}
			r := runHook(ctx, hook.Command, input)
			if r.Blocked || r.Approved {
				return r
			}
		}
	}
	return Result{}
}

// matchesTool returns true if the matcher applies to toolName.
// Empty matcher matches all tools; otherwise it's a tool name or glob.
func matchesTool(matcher, toolName string) bool {
	if matcher == "" {
		return true
	}
	if strings.Contains(matcher, "*") {
		// Simple glob: "Bash*" matches "Bash", "BashTool", etc.
		prefix := strings.TrimSuffix(matcher, "*")
		return strings.HasPrefix(strings.ToLower(toolName), strings.ToLower(prefix))
	}
	return strings.EqualFold(matcher, toolName)
}

// runHook executes a single hook command with JSON input on stdin.
func runHook(ctx context.Context, command string, input HookInput) Result {
	payload, _ := json.Marshal(input)

	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(hctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Non-zero exit: treat as block with the stderr as reason.
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = fmt.Sprintf("hook exited with error: %v", err)
		}
		return Result{Blocked: true, Reason: reason}
	}

	// Parse optional JSON directive from stdout.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return Result{}
	}
	var directive HookOutput
	if err := json.Unmarshal([]byte(out), &directive); err != nil {
		return Result{} // non-JSON stdout is advisory only
	}
	switch strings.ToLower(directive.Decision) {
	case "block":
		reason := directive.Reason
		if reason == "" {
			reason = "blocked by hook"
		}
		return Result{Blocked: true, Reason: reason}
	case "approve":
		return Result{Approved: true}
	}
	return Result{}
}
