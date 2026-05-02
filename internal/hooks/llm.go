package hooks

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
)

// subAgentRunner is a function that can run a sub-agent prompt.
// Injected by the caller (agent loop) when prompt/agent hooks are needed.
// If nil, prompt/agent hooks are skipped (treated as advisory no-ops).
var SubAgentRunner func(ctx context.Context, prompt string) (string, error)

// runPromptHook evaluates a prompt hook by sending the prompt (with $ARGUMENTS
// substituted) to the LLM. The response is parsed as a HookOutput directive.
// Mirrors src/utils/hooks/execPromptHook.ts.
func runPromptHook(ctx context.Context, hook settings.Hook, input HookInput) Result {
	if SubAgentRunner == nil || hook.Prompt == "" {
		return Result{} // no runner available — advisory no-op
	}

	// Substitute $ARGUMENTS with the JSON-encoded hook input.
	prompt := substituteArguments(hook.Prompt, input)

	result, err := SubAgentRunner(ctx, prompt)
	if err != nil {
		// Non-fatal: prompt hook failure is advisory.
		return Result{}
	}

	return parseHookResponse(result)
}

// runAgentHook runs an agent hook — same as prompt hook but with a verification
// framing. If the agent returns a block decision, the tool call is blocked.
// Mirrors src/utils/hooks/execAgentHook.ts.
func runAgentHook(ctx context.Context, hook settings.Hook, input HookInput) Result {
	if SubAgentRunner == nil || hook.Prompt == "" {
		return Result{} // no runner available — advisory no-op
	}

	prompt := substituteArguments(hook.Prompt, input)

	// Wrap in a verification framing so the model knows to return a decision.
	agentPrompt := "You are a verification agent. " + prompt +
		"\n\nRespond with JSON: {\"decision\":\"block\",\"reason\":\"...\"} to block, or {\"decision\":\"approve\"} to allow."

	result, err := SubAgentRunner(ctx, agentPrompt)
	if err != nil {
		return Result{}
	}

	return parseHookResponse(result)
}

// substituteArguments replaces $ARGUMENTS in the prompt with the JSON-encoded
// hook input. Mirrors the $ARGUMENTS substitution in execPromptHook.ts.
func substituteArguments(prompt string, input HookInput) string {
	if !strings.Contains(prompt, "$ARGUMENTS") {
		return prompt
	}
	// Simple substitution — encode key fields.
	args := input.ToolName
	if input.Output != "" {
		args = input.Output
	}
	return strings.ReplaceAll(prompt, "$ARGUMENTS", args)
}

// parseHookResponse extracts a HookOutput from an LLM response string.
// The model may prefix the JSON with natural language — we scan for it.
func parseHookResponse(text string) Result {
	// Look for a JSON object in the text.
	start := strings.Index(text, "{")
	if start < 0 {
		return Result{}
	}
	end := strings.LastIndex(text, "}")
	if end < start {
		return Result{}
	}
	candidate := text[start : end+1]
	var out HookOutput
	if err := json.Unmarshal([]byte(candidate), &out); err != nil {
		return Result{}
	}
	switch strings.ToLower(out.Decision) {
	case "block":
		reason := out.Reason
		if reason == "" {
			reason = "blocked by hook"
		}
		return Result{Blocked: true, Reason: reason}
	case "approve":
		return Result{Approved: true}
	}
	return Result{}
}
