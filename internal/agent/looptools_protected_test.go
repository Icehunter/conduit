package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

// A PreToolUse hook approval must not skip the prompt for protected paths,
// and "always allow" must not be persisted for them.
func TestLoop_HookApprovalDoesNotSkipProtectedAsk(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantAsked int
	}{
		{"protected path still asks", ".git/config", 1},
		{"ordinary path honours hook approval", "src/x.go", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := tool.NewRegistry()
			reg.Register(&fakeTool{name: "Write", result: "ok"})
			dir := t.TempDir()
			gate := permissions.New(dir, nil, permissions.ModeDefault, nil, nil, nil)
			asked := 0
			hooksCfg := &settings.HooksSettings{PreToolUse: []settings.HookMatcher{{
				Matcher: "Write",
				Hooks:   []settings.Hook{{Type: "command", Command: `echo '{"decision":"approve"}'`}},
			}}}
			sse := singleToolUseSSEWithInput("Write", "toolu_p", `{"file_path":"`+tc.path+`"}`)
			lp, srv := newTestLoopWithConfig(t, []string{sse, textOnlySSE("done")}, reg, LoopConfig{
				Gate: gate, Hooks: hooksCfg, SessionID: "s",
				AskPermission: func(context.Context, string, string) (bool, bool) {
					asked++
					return false, false
				},
			})
			defer srv.Close()
			if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if asked != tc.wantAsked {
				t.Errorf("asked = %d, want %d", asked, tc.wantAsked)
			}
		})
	}
}

// "Always allow" on a protected path must not persist an allow rule.
func TestLoop_AlwaysAllowNotPersistedForProtectedPath(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantPersist bool
	}{
		{"protected path", ".git/config", false},
		{"ordinary path", "src/x.go", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := tool.NewRegistry()
			reg.Register(&fakeTool{name: "Write", result: "ok"})
			dir := t.TempDir()
			gate := permissions.New(dir, nil, permissions.ModeDefault, nil, nil, nil)
			sse := singleToolUseSSEWithInput("Write", "toolu_a", `{"file_path":"`+tc.path+`"}`)
			lp, srv := newTestLoopWithConfig(t, []string{sse, textOnlySSE("done")}, reg, LoopConfig{
				Gate: gate, Cwd: dir, SessionID: "s",
				AskPermission: func(context.Context, string, string) (bool, bool) { return true, true },
			})
			defer srv.Close()
			if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			_, err := os.Stat(settings.ProjectLocalSettingsPath(dir))
			if persisted := err == nil; persisted != tc.wantPersist {
				t.Errorf("persisted = %v, want %v (stat err: %v)", persisted, tc.wantPersist, err)
			}
		})
	}
}

// Plan mode denies non-read-only calls outright: the model gets a plan-mode
// message and the user is never prompted.
func TestLoop_PlanModeDeniesWithoutAsking(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "ok"})
	dir := t.TempDir()
	gate := permissions.New(dir, nil, permissions.ModePlan, nil, nil, nil)
	asked := 0
	sse := singleToolUseSSEWithInput("Bash", "toolu_plan", `{"command":"touch x"}`)
	lp, srv := newTestLoopWithConfig(t, []string{sse, textOnlySSE("done")}, reg, LoopConfig{
		Gate: gate, Cwd: dir, SessionID: "s",
		AskPermission: func(context.Context, string, string) (bool, bool) {
			asked++
			return true, false
		},
	})
	defer srv.Close()
	var got LoopEvent
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(ev LoopEvent) {
		if ev.Type == EventToolResult {
			got = ev
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.IsError || !strings.Contains(got.ResultText, "plan mode is read-only") {
		t.Errorf("result = %+v, want denied plan-mode error", got)
	}
	if asked != 0 {
		t.Errorf("asked = %d, want 0", asked)
	}
}

// A PreToolUse hook "ask" must not turn into a prompt in plan or council
// mode, even for an otherwise allowed read-only call.
func TestLoop_PlanModeHookAskNeverPrompts(t *testing.T) {
	for _, mode := range []permissions.Mode{permissions.ModePlan, permissions.ModeCouncil} {
		t.Run(string(mode), func(t *testing.T) {
			reg := tool.NewRegistry()
			reg.Register(&fakeTool{name: "Read", result: "ok", readOnly: true})
			dir := t.TempDir()
			gate := permissions.New(dir, nil, mode, nil, nil, nil)
			asked := 0
			hooksCfg := &settings.HooksSettings{PreToolUse: []settings.HookMatcher{{
				Matcher: "Read",
				Hooks:   []settings.Hook{{Type: "command", Command: `echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`}},
			}}}
			sse := singleToolUseSSEWithInput("Read", "toolu_ask", `{"file_path":"x.go"}`)
			lp, srv := newTestLoopWithConfig(t, []string{sse, textOnlySSE("done")}, reg, LoopConfig{
				Gate: gate, Hooks: hooksCfg, Cwd: dir, SessionID: "s",
				AskPermission: func(context.Context, string, string) (bool, bool) {
					asked++
					return true, false
				},
			})
			defer srv.Close()
			var got LoopEvent
			if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(ev LoopEvent) {
				if ev.Type == EventToolResult {
					got = ev
				}
			}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if asked != 0 {
				t.Errorf("asked = %d, want 0", asked)
			}
			if !got.IsError || !strings.Contains(got.ResultText, "plan mode is read-only") {
				t.Errorf("result = %+v, want a plan-mode denial", got)
			}
		})
	}
}
