package agent

import (
	"context"
	"os"
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
