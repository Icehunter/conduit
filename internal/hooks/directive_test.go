package hooks

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/icehunter/conduit/internal/settings"
)

func TestHookOutputResult(t *testing.T) {
	tests := []struct {
		name string
		json string
		want Result
	}{
		{"empty", `{}`, Result{}},
		{"legacy block", `{"decision":"block","reason":"no"}`, Result{Blocked: true, Reason: "no"}},
		{"legacy block default reason", `{"decision":"block"}`, Result{Blocked: true, Reason: "blocked by hook"}},
		{"legacy approve", `{"decision":"approve"}`, Result{Approved: true}},
		{"cc deny", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"nope"}}`,
			Result{Blocked: true, Reason: "nope"}},
		{"cc deny default reason", `{"hookSpecificOutput":{"permissionDecision":"DENY"}}`,
			Result{Blocked: true, Reason: "blocked by hook"}},
		{"cc allow", `{"hookSpecificOutput":{"permissionDecision":"allow"}}`, Result{Approved: true}},
		{"cc ask", `{"hookSpecificOutput":{"permissionDecision":"ask"}}`, Result{Ask: true}},
		{"cc decision outranks legacy", `{"decision":"block","hookSpecificOutput":{"permissionDecision":"allow"}}`,
			Result{Approved: true}},
		{"updatedInput alone", `{"hookSpecificOutput":{"updatedInput":{"command":"ls"}}}`,
			Result{UpdatedInput: map[string]any{"command": "ls"}}},
		{"updatedInput with allow", `{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"a":1}}}`,
			Result{Approved: true, UpdatedInput: map[string]any{"a": float64(1)}}},
		{"updatedInput dropped on deny", `{"hookSpecificOutput":{"permissionDecision":"deny","updatedInput":{"a":1}}}`,
			Result{Blocked: true, Reason: "blocked by hook"}},
		{"additional context nested", `{"hookSpecificOutput":{"additionalContext":"n"}}`, Result{AdditionalContext: "n"}},
		{"additional context top level", `{"additionalContext":"t"}`, Result{AdditionalContext: "t"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out HookOutput
			if err := json.Unmarshal([]byte(tt.json), &out); err != nil {
				t.Fatal(err)
			}
			if got := out.result("blocked by hook"); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRunPreToolUse_Directives(t *testing.T) {
	cmd := func(script string) settings.HookMatcher {
		return settings.HookMatcher{Hooks: []settings.Hook{{Type: "command", Command: script}}}
	}
	tests := []struct {
		name     string
		matchers []settings.HookMatcher
		check    func(t *testing.T, r Result)
	}{
		{"updatedInput replaces", []settings.HookMatcher{
			cmd(`echo '{"hookSpecificOutput":{"updatedInput":{"command":"safe"}}}'`),
		}, func(t *testing.T, r Result) {
			if r.UpdatedInput["command"] != "safe" || r.Blocked || r.Approved {
				t.Errorf("got %+v", r)
			}
		}},
		{"last updatedInput wins", []settings.HookMatcher{
			cmd(`echo '{"hookSpecificOutput":{"updatedInput":{"v":1}}}'`),
			cmd(`echo '{"hookSpecificOutput":{"updatedInput":{"v":2}}}'`),
		}, func(t *testing.T, r Result) {
			if r.UpdatedInput["v"] != float64(2) {
				t.Errorf("got %+v", r)
			}
		}},
		{"ask is sticky across hooks", []settings.HookMatcher{
			cmd(`echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`),
			cmd(`echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`),
		}, func(t *testing.T, r Result) {
			if !r.Ask || !r.Approved {
				t.Errorf("got %+v", r)
			}
		}},
		{"deny short-circuits", []settings.HookMatcher{
			cmd(`echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"r"}}'`),
			cmd(`echo '{"hookSpecificOutput":{"updatedInput":{"v":2}}}'`),
		}, func(t *testing.T, r Result) {
			if !r.Blocked || r.Reason != "r" || r.UpdatedInput != nil {
				t.Errorf("got %+v", r)
			}
		}},
		{"non-json stdout is advisory", []settings.HookMatcher{cmd(`echo hello`)}, func(t *testing.T, r Result) {
			if !reflect.DeepEqual(r, Result{}) {
				t.Errorf("got %+v", r)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, RunPreToolUse(context.Background(), tt.matchers, "s", "Bash", map[string]any{"command": "rm"}))
		})
	}
}

func TestParseHookResponse_Directives(t *testing.T) {
	r := parseHookResponse(`Sure. {"hookSpecificOutput":{"permissionDecision":"ask","updatedInput":{"x":"y"}}} done`)
	if !r.Ask || r.UpdatedInput["x"] != "y" {
		t.Errorf("got %+v", r)
	}
}
