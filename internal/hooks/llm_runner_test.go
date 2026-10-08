package hooks

import (
	"context"
	"testing"

	"github.com/icehunter/conduit/internal/settings"
)

// Agent hooks use AgentHookRunner when the loop provides one (CC classes them
// as sub-agent work); prompt hooks always use SubAgentRunner (auxiliary).
func TestLLMHooks_RunnerSelection(t *testing.T) {
	orig := DefaultAsyncGroup
	t.Cleanup(func() { DefaultAsyncGroup = orig })

	var used []string
	runner := func(name string) func(context.Context, string) (string, error) {
		return func(context.Context, string) (string, error) {
			used = append(used, name)
			return `{"decision":"approve"}`, nil
		}
	}
	hook := settings.Hook{Prompt: "check it"}

	tests := []struct {
		name      string
		agentHook bool
		withAgent bool
		want      string
	}{
		{"prompt hook uses sub-agent runner", false, true, "sub"},
		{"agent hook prefers agent-hook runner", true, true, "agent"},
		{"agent hook falls back without one", true, false, "sub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			used = nil
			DefaultAsyncGroup = &AsyncGroup{SubAgentRunner: runner("sub")}
			if tt.withAgent {
				DefaultAsyncGroup.AgentHookRunner = runner("agent")
			}
			var res Result
			if tt.agentHook {
				res = runAgentHook(context.Background(), hook, HookInput{})
			} else {
				res = runPromptHook(context.Background(), hook, HookInput{})
			}
			if !res.Approved || len(used) != 1 || used[0] != tt.want {
				t.Errorf("used = %v (approved=%v), want [%s]", used, res.Approved, tt.want)
			}
		})
	}
}
