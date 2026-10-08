// Package hooks implements PreToolUse, PostToolUse, SessionStart, Stop, and
// UserPromptSubmit hook runners.
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
//	{"hookSpecificOutput": {                 — Claude Code PreToolUse form:
//	  "hookEventName": "PreToolUse",           permissionDecision allow|deny|ask
//	  "permissionDecision": "ask",             decides the prompt; updatedInput
//	  "updatedInput": {...}}}                  replaces the model's tool input
//	(anything else / no JSON)                — no effect, hook is advisory
package hooks

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

	"github.com/icehunter/conduit/internal/settings"
)

// HookEvent identifies when a hook fires.
type HookEvent string

const (
	EventPreToolUse    HookEvent = "PreToolUse"
	EventPostToolUse   HookEvent = "PostToolUse"
	EventSessionStart  HookEvent = "SessionStart"
	EventStop          HookEvent = "Stop"
	EventTeammateIdle  HookEvent = "TeammateIdle"
	EventTaskCreated   HookEvent = "TaskCreated"
	EventTaskCompleted HookEvent = "TaskCompleted"

	// EventUserPromptSubmit fires before a user prompt is sent to the model.
	// Unlike the advisory events it gates the prompt: it can block it, add
	// context, or rewrite it.
	EventUserPromptSubmit HookEvent = "UserPromptSubmit"
)

// HookInput is the JSON payload sent to hook stdin.
type HookInput struct {
	SessionID string `json:"session_id"`
	// Event names the hook event; it is also the hook_event_name field of the
	// payload, as in Claude Code.
	Event          HookEvent      `json:"hook_event_name,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	Cwd            string         `json:"cwd,omitempty"`
	TranscriptPath string         `json:"transcript_path,omitempty"`
	PermissionMode string         `json:"permission_mode,omitempty"`
	ToolName       string         `json:"tool_name,omitempty"`
	ToolInput      map[string]any `json:"tool_input,omitempty"`
	Output         string         `json:"tool_response,omitempty"`
	TeammateName   string         `json:"teammate_name,omitempty"`
	TaskID         string         `json:"task_id,omitempty"`
}

// HookOutput is the optional JSON a hook writes to stdout.
type HookOutput struct {
	Decision string `json:"decision"` // "approve" | "block" | ""
	Reason   string `json:"reason"`
	// Continue=false stops the turn without a model request (UserPromptSubmit);
	// StopReason is shown to the user. SystemMessage is a notice for the user.
	Continue      *bool  `json:"continue"`
	StopReason    string `json:"stopReason"`
	SystemMessage string `json:"systemMessage"`
	// additionalContext is the top-level field used by Copilot CLI and SDK.
	AdditionalContext string `json:"additionalContext"`
	// hookSpecificOutput carries the Claude Code SessionStart context payload.
	HookSpecificOutput struct {
		AdditionalContext string `json:"additionalContext"`
		// PermissionDecision ("allow" | "deny" | "ask") and its reason are the
		// Claude Code PreToolUse form; they take precedence over the older
		// top-level decision.
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		// UpdatedInput replaces the model's tool input (PreToolUse only).
		UpdatedInput map[string]any `json:"updatedInput"`
		// SessionTitle renames the session (UserPromptSubmit).
		SessionTitle string `json:"sessionTitle"`
		// UpdatedPrompt replaces the user's prompt text (UserPromptSubmit).
		// conduit extension; Claude Code has no equivalent.
		UpdatedPrompt *string `json:"updatedPrompt"`
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
	// Ask is true if a hook returned permissionDecision=ask: the user is
	// prompted even where the permission rules would allow the call.
	Ask bool
	// UpdatedInput, when non-nil, replaces the model's tool input before the
	// permission check and execution (PreToolUse only). With several hooks the
	// last one to return it wins.
	UpdatedInput map[string]any
	// AdditionalContext is text returned by a SessionStart hook to be injected
	// into the conversation as a system-reminder on the first turn.
	AdditionalContext string
	// Stopped is true if a hook returned continue=false: the turn ends without
	// a model request and StopReason is shown to the user (UserPromptSubmit).
	Stopped    bool
	StopReason string
	// SystemMessage is a notice for the user.
	SystemMessage string
	// Warnings are hooks that could not run to a verdict and were let through
	// (UserPromptSubmit fails open); the caller shows them to the user.
	Warnings []string
	// SessionTitle, when non-empty, renames the session (UserPromptSubmit).
	SessionTitle string
	// UpdatedPrompt, when non-nil, replaces the user's prompt text
	// (UserPromptSubmit). With several hooks the last one to return it wins.
	UpdatedPrompt *string
}

// RunPreToolUse runs all PreToolUse hooks matching toolName.
// Returns a Result; if Result.Blocked, the tool call should be aborted.
func RunPreToolUse(ctx context.Context, hooks []settings.HookMatcher, sessionID, toolName string, toolInput map[string]any) Result {
	input := HookInput{
		SessionID: sessionID,
		Event:     EventPreToolUse,
		ToolName:  toolName,
		ToolInput: toolInput,
	}
	return runMatching(ctx, hooks, toolName, input)
}

// RunPostToolUse runs all PostToolUse hooks matching toolName.
func RunPostToolUse(ctx context.Context, hooks []settings.HookMatcher, sessionID, toolName, output string) Result {
	input := HookInput{
		SessionID: sessionID,
		Event:     EventPostToolUse,
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
	input := HookInput{SessionID: sessionID, Event: EventSessionStart}
	r := runMatching(ctx, hooks, "startup", input)
	return r.AdditionalContext
}

// RunStop runs all Stop hooks. Results are advisory — never blocks.
func RunStop(ctx context.Context, hooks []settings.HookMatcher, sessionID string) {
	input := HookInput{SessionID: sessionID, Event: EventStop}
	runMatching(ctx, hooks, "", input)
}

// RunTeammateIdle fires all TeammateIdle hooks. Advisory — never blocks.
func RunTeammateIdle(ctx context.Context, hooks []settings.HookMatcher, sessionID, teammateName string) {
	input := HookInput{SessionID: sessionID, Event: EventTeammateIdle, TeammateName: teammateName}
	runMatching(ctx, hooks, "", input)
}

// RunTaskCreated fires all TaskCreated hooks. Advisory — never blocks.
func RunTaskCreated(ctx context.Context, hooks []settings.HookMatcher, sessionID, taskID string) {
	input := HookInput{SessionID: sessionID, Event: EventTaskCreated, TaskID: taskID}
	runMatching(ctx, hooks, "", input)
}

// RunTaskCompleted fires all TaskCompleted hooks. Advisory — never blocks.
func RunTaskCompleted(ctx context.Context, hooks []settings.HookMatcher, sessionID, taskID string) {
	input := HookInput{SessionID: sessionID, Event: EventTaskCompleted, TaskID: taskID}
	runMatching(ctx, hooks, "", input)
}

// RunUserPromptSubmit runs every UserPromptSubmit hook for input and merges
// their outcomes. Matchers are ignored (the event has no tool) and so is the
// async flag: the hooks gate the prompt, so they run in order and to
// completion. A block or stop short-circuits the rest. A hook that times out,
// crashes or cannot be reached fails open: the prompt goes through.
func RunUserPromptSubmit(ctx context.Context, matchers []settings.HookMatcher, input HookInput) Result {
	input.Event = EventUserPromptSubmit
	var result Result
	join := func(dst *string, add string) {
		if add == "" {
			return
		}
		if *dst != "" {
			*dst += "\n"
		}
		*dst += add
	}
	for _, m := range matchers {
		for _, hook := range m.Hooks {
			r := dispatchHook(ctx, hook, input, m.PluginRoot)
			join(&result.AdditionalContext, r.AdditionalContext)
			join(&result.SystemMessage, r.SystemMessage)
			result.Warnings = append(result.Warnings, r.Warnings...)
			if r.SessionTitle != "" {
				result.SessionTitle = r.SessionTitle
			}
			if r.UpdatedPrompt != nil {
				result.UpdatedPrompt = r.UpdatedPrompt
			}
			if r.Blocked || r.Stopped {
				// The verdict ends the run, but what earlier hooks and this
				// one already said (notices, title, warnings) is kept.
				result.Blocked, result.Reason = r.Blocked, r.Reason
				result.Stopped, result.StopReason = r.Stopped, r.StopReason
				return result
			}
		}
	}
	return result
}

// failed is the Result for a hook that could not run to a verdict. Every event
// but UserPromptSubmit treats that as a block; a prompt is never held hostage
// by a broken hook, so there it lets the prompt through and returns the reason
// as a warning for the caller to show.
func failed(input HookInput, reason string) Result {
	if input.Event == EventUserPromptSubmit {
		return Result{Warnings: []string{reason}}
	}
	return Result{Blocked: true, Reason: reason}
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
			if r.Ask {
				result.Ask = true
			}
			if r.UpdatedInput != nil {
				result.UpdatedInput = r.UpdatedInput
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
	r := dispatchHookRaw(ctx, hook, input, pluginRoot)
	if r.Stopped && input.Event != EventUserPromptSubmit {
		// continue:false is a stop only where the event can stop a turn. Every
		// other event gates something (a tool call), and a stop there denies it.
		r.Blocked = true
		if r.Reason == "" {
			r.Reason = r.StopReason
		}
		if r.Reason == "" {
			r.Reason = "stopped by hook"
		}
	}
	return r
}

func dispatchHookRaw(ctx context.Context, hook settings.Hook, input HookInput, pluginRoot string) Result {
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
	for part := range strings.SplitSeq(matcher, "|") {
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
		reason := strings.TrimSpace(stderr.String())
		if input.Event == EventUserPromptSubmit {
			// Claude Code semantics: only exit 2 blocks the prompt. Any other
			// failure, a timeout included, is a warning and the prompt goes on.
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 && hctx.Err() == nil {
				if reason == "" {
					reason = "hook exited with status 2"
				}
				return Result{Blocked: true, Reason: reason}
			}
			return failed(input, fmt.Sprintf("%v: %s", err, reason))
		}
		// Non-zero exit: treat as block with the stderr as reason.
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
		if input.Event == EventUserPromptSubmit {
			return Result{AdditionalContext: out} // plain stdout is context for the model
		}
		return Result{} // non-JSON stdout is advisory only
	}
	return directive.result("blocked by hook")
}

// result converts a parsed hook directive into a Result. blockedDefault is the
// reason used when a block carries none. The Claude Code
// hookSpecificOutput.permissionDecision form wins over the older top-level
// decision; "ask" and updatedInput are only expressible in the former.
func (d HookOutput) result(blockedDefault string) Result {
	r := d.decision(blockedDefault)
	// continue:false is layered on the decision, never in place of it: a
	// deny or block alongside it must still deny.
	if d.Continue != nil && !*d.Continue {
		r.Stopped = true
		r.StopReason = d.StopReason
	}
	r.SystemMessage = d.SystemMessage
	return r
}

func (d HookOutput) decision(blockedDefault string) Result {
	hso := d.HookSpecificOutput
	var r Result
	switch strings.ToLower(hso.PermissionDecision) {
	case "deny":
		reason := hso.PermissionDecisionReason
		if reason == "" {
			reason = blockedDefault
		}
		return Result{Blocked: true, Reason: reason}
	case "allow":
		r.Approved = true
	case "ask":
		r.Ask = true
	default:
		switch strings.ToLower(d.Decision) {
		case "block":
			reason := d.Reason
			if reason == "" {
				reason = blockedDefault
			}
			return Result{Blocked: true, Reason: reason}
		case "approve":
			r.Approved = true
		}
	}
	r.UpdatedInput = hso.UpdatedInput
	r.UpdatedPrompt = hso.UpdatedPrompt
	r.SessionTitle = hso.SessionTitle
	// additionalContext comes from the nested hookSpecificOutput field (Claude
	// Code SessionStart format) or the top-level one (Copilot/SDK).
	r.AdditionalContext = hso.AdditionalContext
	if r.AdditionalContext == "" {
		r.AdditionalContext = d.AdditionalContext
	}
	return r
}
