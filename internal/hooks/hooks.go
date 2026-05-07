// Package hooks implements PreToolUse, PostToolUse, SessionStart, and Stop
// hook runners.
//
// Mirrors src/utils/hooks/ from the TS source. Hooks are shell commands
// defined in settings.json under the `hooks` key. They run synchronously
// before/after tool calls and can block execution by returning non-zero.
//
// Hook input is written as JSON to the hook's stdin.
// stdout is parsed for optional JSON directives:
//
//	{"decision": "block", "reason": "..."}  — blocks the tool call
//	{"decision": "approve"}                  — approves without further prompting
//	(anything else / no JSON)                — no effect, hook is advisory
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// HookEvent identifies when a hook fires.
type HookEvent string

const (
	EventPreToolUse   HookEvent = "PreToolUse"
	EventPostToolUse  HookEvent = "PostToolUse"
	EventSessionStart HookEvent = "SessionStart"
	EventStop         HookEvent = "Stop"
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
	// additionalContext is the top-level field used by Copilot CLI and SDK.
	AdditionalContext string `json:"additionalContext"`
	// hookSpecificOutput carries the Claude Code SessionStart context payload.
	HookSpecificOutput struct {
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
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
	// AdditionalContext is text returned by a SessionStart hook to be injected
	// into the conversation as a system-reminder on the first turn.
	AdditionalContext string
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
// Returns any additionalContext text collected from hook output, to be injected
// into the first conversation turn as a <system-reminder>.
// The event type "startup" is passed as the matcher token so that hooks with
// matchers like "startup|clear|compact" fire correctly.
func RunSessionStart(ctx context.Context, hooks []settings.HookMatcher, sessionID string) string {
	input := HookInput{SessionID: sessionID}
	r := runMatching(ctx, hooks, "startup", input)
	return r.AdditionalContext
}

// RunStop runs all Stop hooks. Results are advisory — never blocks.
func RunStop(ctx context.Context, hooks []settings.HookMatcher, sessionID string) {
	input := HookInput{SessionID: sessionID}
	runMatching(ctx, hooks, "", input)
}

func runMatching(ctx context.Context, matchers []settings.HookMatcher, toolName string, input HookInput) Result {
	var result Result
	for _, m := range matchers {
		if !matchesTool(m.Matcher, toolName) {
			continue
		}
		pluginRoot := m.PluginRoot
		for _, hook := range m.Hooks {
			if hook.Async {
				// Fire-and-forget: run in background, never block the caller.
				// When DefaultAsyncGroup is set (normal session path), the
				// goroutine is tracked and cancellable at shutdown. When nil
				// (tests or non-session callers), fall back to an untracked
				// goroutine tied to context.Background() — original behaviour.
				h := hook
				in := input
				pr := pluginRoot
				if DefaultAsyncGroup != nil {
					DefaultAsyncGroup.Go(func(ctx context.Context) {
						_ = dispatchHook(ctx, h, in, pr)
					})
				} else {
					go func() {
						_ = dispatchHook(context.Background(), h, in, pr)
					}()
				}
				continue
			}
			r := dispatchHook(ctx, hook, input, pluginRoot)
			if r.Blocked {
				// Block short-circuits all remaining hooks immediately.
				return r
			}
			if r.Approved {
				// Approved is sticky but does not stop later matchers — a more
				// specific matcher further down the list may still block.
				result.Approved = true
			}
			// Accumulate additional context from SessionStart hooks.
			if r.AdditionalContext != "" {
				if result.AdditionalContext != "" {
					result.AdditionalContext += "\n"
				}
				result.AdditionalContext += r.AdditionalContext
			}
		}
	}
	return result
}

// dispatchHook routes a hook to the appropriate runner based on its type.
// pluginRoot is the plugin's install directory; non-empty for plugin-sourced hooks.
func dispatchHook(ctx context.Context, hook settings.Hook, input HookInput, pluginRoot string) Result {
	switch hook.Type {
	case "command", "":
		if hook.Command == "" {
			return Result{}
		}
		return runHook(ctx, hook.Command, hookTimeout(hook.TimeoutSecs), input, pluginRoot)
	case "http":
		return runHTTPHook(ctx, hook, input)
	case "prompt":
		return runPromptHook(ctx, hook, input)
	case "agent":
		return runAgentHook(ctx, hook, input)
	default:
		return Result{} // unknown type is advisory
	}
}

// matchesTool returns true if the matcher applies to toolName.
// Empty matcher matches all tools. Multiple alternatives separated by "|"
// are evaluated as OR (e.g. "startup|clear|compact" for SessionStart event
// types). Each alternative may be an exact name or a glob ("Bash*").
func matchesTool(matcher, toolName string) bool {
	if matcher == "" {
		return true
	}
	for _, part := range strings.Split(matcher, "|") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "*") {
			prefix := strings.TrimSuffix(part, "*")
			if strings.HasPrefix(strings.ToLower(toolName), strings.ToLower(prefix)) {
				return true
			}
		} else if strings.EqualFold(part, toolName) {
			return true
		}
	}
	return false
}

// maxHookTimeout is the upper bound for any hook execution.
const maxHookTimeout = 60 * time.Second

// minHookTimeout is the lower bound to prevent zero/negative values from
// disabling the timeout guard.
const minHookTimeout = time.Second

// hookTimeout converts a per-hook TimeoutSecs value to a clamped duration.
// 0 → default (60s). Values outside [1s, 60s] are clamped.
func hookTimeout(secs int) time.Duration {
	if secs <= 0 {
		return maxHookTimeout
	}
	d := time.Duration(secs) * time.Second
	if d < minHookTimeout {
		return minHookTimeout
	}
	if d > maxHookTimeout {
		return maxHookTimeout
	}
	return d
}

// runHook executes a single hook command with JSON input on stdin.
// pluginRoot is injected as CLAUDE_PLUGIN_ROOT in the subprocess environment
// when non-empty, enabling plugin hooks to reference their own install path.
func runHook(ctx context.Context, command string, timeout time.Duration, input HookInput, pluginRoot string) Result {
	payload, _ := json.Marshal(input)

	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(hctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if pluginRoot != "" {
		cmd.Env = append(os.Environ(), "CLAUDE_PLUGIN_ROOT="+pluginRoot)
	}

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
	// Collect additionalContext from either the top-level field (Copilot/SDK)
	// or the nested hookSpecificOutput field (Claude Code SessionStart format).
	addlCtx := directive.HookSpecificOutput.AdditionalContext
	if addlCtx == "" {
		addlCtx = directive.AdditionalContext
	}
	return Result{AdditionalContext: addlCtx}
}
