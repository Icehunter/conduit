package hooks

import (
	"context"
	"testing"

	"github.com/icehunter/claude-go/internal/settings"
)

func TestMatchesTool_Empty(t *testing.T) {
	if !matchesTool("", "Bash") {
		t.Error("empty matcher should match all tools")
	}
	if !matchesTool("", "") {
		t.Error("empty matcher should match empty tool")
	}
}

func TestMatchesTool_Exact(t *testing.T) {
	if !matchesTool("Bash", "Bash") {
		t.Error("exact match should work")
	}
	if matchesTool("Bash", "Edit") {
		t.Error("exact match should not match different tool")
	}
}

func TestMatchesTool_CaseInsensitive(t *testing.T) {
	if !matchesTool("bash", "Bash") {
		t.Error("matching should be case-insensitive")
	}
}

func TestMatchesTool_Glob(t *testing.T) {
	if !matchesTool("Bash*", "Bash") {
		t.Error("glob should match")
	}
	if !matchesTool("Bash*", "BashTool") {
		t.Error("glob should match prefix")
	}
}

func TestRunHook_ZeroExit(t *testing.T) {
	r := runHook(context.Background(), "true", HookInput{})
	if r.Blocked {
		t.Errorf("zero-exit hook should not block; reason: %s", r.Reason)
	}
}

func TestRunHook_NonZeroExit(t *testing.T) {
	r := runHook(context.Background(), "false", HookInput{})
	if !r.Blocked {
		t.Error("non-zero exit should block")
	}
}

func TestRunHook_BlockDirective(t *testing.T) {
	r := runHook(context.Background(), `echo '{"decision":"block","reason":"test"}'`, HookInput{})
	if !r.Blocked {
		t.Error("block directive should block")
	}
	if r.Reason != "test" {
		t.Errorf("reason = %q", r.Reason)
	}
}

func TestRunHook_ApproveDirective(t *testing.T) {
	r := runHook(context.Background(), `echo '{"decision":"approve"}'`, HookInput{})
	if r.Blocked {
		t.Error("approve directive should not block")
	}
}

func TestRunPreToolUse_NoMatchers(t *testing.T) {
	r := RunPreToolUse(context.Background(), nil, "sess", "Bash", nil)
	if r.Blocked {
		t.Error("no matchers should not block")
	}
}

func TestRunPreToolUse_MatchAndBlock(t *testing.T) {
	matchers := []settings.HookMatcher{{
		Matcher: "Bash",
		Hooks:   []settings.Hook{{Type: "command", Command: "false"}},
	}}
	r := RunPreToolUse(context.Background(), matchers, "sess", "Bash", nil)
	if !r.Blocked {
		t.Error("matching hook with non-zero exit should block")
	}
}

func TestRunPreToolUse_NonMatchingTool(t *testing.T) {
	matchers := []settings.HookMatcher{{
		Matcher: "Edit",
		Hooks:   []settings.Hook{{Type: "command", Command: "false"}},
	}}
	r := RunPreToolUse(context.Background(), matchers, "sess", "Bash", nil)
	if r.Blocked {
		t.Error("hook for different tool should not affect Bash")
	}
}
