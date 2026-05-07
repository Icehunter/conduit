package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/settings"
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
	r := runHook(context.Background(), "true", maxHookTimeout, HookInput{}, "")
	if r.Blocked {
		t.Errorf("zero-exit hook should not block; reason: %s", r.Reason)
	}
}

func TestRunHook_NonZeroExit(t *testing.T) {
	r := runHook(context.Background(), "false", maxHookTimeout, HookInput{}, "")
	if !r.Blocked {
		t.Error("non-zero exit should block")
	}
}

func TestRunHook_BlockDirective(t *testing.T) {
	r := runHook(context.Background(), `echo '{"decision":"block","reason":"test"}'`, maxHookTimeout, HookInput{}, "")
	if !r.Blocked {
		t.Error("block directive should block")
	}
	if r.Reason != "test" {
		t.Errorf("reason = %q", r.Reason)
	}
}

func TestRunHook_ApproveDirective(t *testing.T) {
	r := runHook(context.Background(), `echo '{"decision":"approve"}'`, maxHookTimeout, HookInput{}, "")
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

func TestRunHook_ApproveDirectiveSetsApproved(t *testing.T) {
	r := runHook(context.Background(), `echo '{"decision":"approve"}'`, maxHookTimeout, HookInput{}, "")
	if r.Blocked {
		t.Error("approve should not block")
	}
	if !r.Approved {
		t.Error("approve directive should set Approved=true")
	}
}

func TestRunHook_NonJSONStdoutIsAdvisory(t *testing.T) {
	r := runHook(context.Background(), `echo "some output"`, maxHookTimeout, HookInput{}, "")
	if r.Blocked || r.Approved {
		t.Error("non-JSON stdout should be advisory only")
	}
}

func TestRunHook_StdinReceivesJSON(t *testing.T) {
	// Hook reads stdin and exits non-zero if it doesn't contain session_id.
	r := runHook(context.Background(), `grep -q '"session_id"' || exit 1`, maxHookTimeout, HookInput{SessionID: "test-session"}, "")
	if r.Blocked {
		t.Errorf("hook should have found session_id in stdin: %s", r.Reason)
	}
}

func TestRunSessionStart_NoHooks(t *testing.T) {
	// Must not panic with nil/empty matchers.
	if ctx := RunSessionStart(context.Background(), nil, "sess"); ctx != "" {
		t.Errorf("empty hook list should return empty context, got %q", ctx)
	}
	RunSessionStart(context.Background(), []settings.HookMatcher{}, "sess")
}

func TestRunSessionStart_Fires(t *testing.T) {
	// SessionStart hooks are advisory — result is ignored even if they exit non-zero.
	matchers := []settings.HookMatcher{{
		Matcher: "",
		Hooks:   []settings.Hook{{Type: "command", Command: "false"}},
	}}
	// Must not panic and must not return an error to caller.
	RunSessionStart(context.Background(), matchers, "sess")
}

func TestRunSessionStart_MatchesStartupToken(t *testing.T) {
	// Superpowers uses "startup|clear|compact" as the SessionStart matcher.
	// RunSessionStart must pass "startup" so it fires.
	out := filepath.Join(t.TempDir(), "fired.txt")
	matchers := []settings.HookMatcher{{
		Matcher: "startup|clear|compact",
		Hooks: []settings.Hook{{
			Type:    "command",
			Command: "echo fired > '" + out + "'",
		}},
	}}
	RunSessionStart(context.Background(), matchers, "sess")
	if _, err := os.Stat(out); err != nil {
		t.Error("SessionStart hook with matcher 'startup|clear|compact' did not fire")
	}
}

func TestRunSessionStart_ReturnsAdditionalContext(t *testing.T) {
	matchers := []settings.HookMatcher{{
		Matcher: "",
		Hooks: []settings.Hook{{
			Type:    "command",
			Command: `echo '{"hookSpecificOutput":{"additionalContext":"hello from hook"}}'`,
		}},
	}}
	got := RunSessionStart(context.Background(), matchers, "sess")
	if got != "hello from hook" {
		t.Errorf("AdditionalContext = %q; want %q", got, "hello from hook")
	}
}

func TestMatchesTool_PipeAlternation(t *testing.T) {
	tests := []struct {
		matcher string
		tool    string
		want    bool
	}{
		{"startup|clear|compact", "startup", true},
		{"startup|clear|compact", "clear", true},
		{"startup|clear|compact", "compact", true},
		{"startup|clear|compact", "stop", false},
		{"startup|clear|compact", "", false},
		{"Bash|Edit", "Bash", true},
		{"Bash|Edit", "Edit", true},
		{"Bash|Edit", "Grep", false},
	}
	for _, tt := range tests {
		got := matchesTool(tt.matcher, tt.tool)
		if got != tt.want {
			t.Errorf("matchesTool(%q, %q) = %v; want %v", tt.matcher, tt.tool, got, tt.want)
		}
	}
}

func TestRunStop_NoHooks(t *testing.T) {
	RunStop(context.Background(), nil, "sess")
}

func TestRunStop_Fires(t *testing.T) {
	matchers := []settings.HookMatcher{{
		Matcher: "",
		Hooks:   []settings.Hook{{Type: "command", Command: "true"}},
	}}
	RunStop(context.Background(), matchers, "sess")
}

func TestRunPreToolUse_ApproveResult(t *testing.T) {
	matchers := []settings.HookMatcher{{
		Matcher: "Bash",
		Hooks:   []settings.Hook{{Type: "command", Command: `echo '{"decision":"approve"}'`}},
	}}
	r := RunPreToolUse(context.Background(), matchers, "sess", "Bash", nil)
	if r.Blocked {
		t.Error("approve should not block")
	}
	if !r.Approved {
		t.Error("approve directive should propagate Approved=true")
	}
}

// --- Conformance tests (M12 hardening) ---

// TestRunPostToolUse_ReceivesOutput verifies that PostToolUse hooks receive the
// tool output in the `tool_response` field (mirrors HookInput.Output JSON key).
func TestRunPostToolUse_ReceivesOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hook_output.json")
	matchers := []settings.HookMatcher{{
		Matcher: "Bash",
		Hooks: []settings.Hook{{
			Type:    "command",
			Command: "cat > '" + filepath.ToSlash(out) + "'",
		}},
	}}

	RunPostToolUse(context.Background(), matchers, "sess-1", "Bash", "echo hi output")

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("hook did not write output file: %v", err)
	}
	var inp HookInput
	if err := json.Unmarshal(data, &inp); err != nil {
		t.Fatalf("parse hook stdin JSON: %v", err)
	}
	if inp.ToolName != "Bash" {
		t.Errorf("tool_name = %q; want Bash", inp.ToolName)
	}
	if inp.Output != "echo hi output" {
		t.Errorf("tool_response = %q; want echo hi output", inp.Output)
	}
	if inp.SessionID != "sess-1" {
		t.Errorf("session_id = %q; want sess-1", inp.SessionID)
	}
}

// TestRunPostToolUse_NonMatchingToolSkipped verifies that PostToolUse hooks
// with a non-matching matcher do not fire.
func TestRunPostToolUse_NonMatchingToolSkipped(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hook_output.json")
	matchers := []settings.HookMatcher{{
		Matcher: "Edit", // only fires for Edit, not Bash
		Hooks: []settings.Hook{{
			Type:    "command",
			Command: "cat > " + out,
		}},
	}}

	RunPostToolUse(context.Background(), matchers, "sess", "Bash", "output")

	if _, err := os.Stat(out); err == nil {
		t.Error("hook should not have fired for non-matching tool")
	}
}

// TestRunHook_AsyncReturnsImmediately verifies that async hooks do not block
// the caller. The hook sleeps 300 ms; the caller should return in <100 ms.
func TestRunHook_AsyncReturnsImmediately(t *testing.T) {
	matchers := []settings.HookMatcher{{
		Matcher: "",
		Hooks: []settings.Hook{{
			Type:    "command",
			Command: "sleep 0.3",
			Async:   true,
		}},
	}}

	start := time.Now()
	RunPreToolUse(context.Background(), matchers, "sess", "Bash", nil)
	elapsed := time.Since(start)

	if elapsed > 150*time.Millisecond {
		t.Errorf("async hook blocked caller for %v; want <150ms", elapsed)
	}
}

func TestRunHook_InjectsPluginRoot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "plugin_root.txt")
	// The hook writes $CLAUDE_PLUGIN_ROOT to a file so we can read it back.
	cmd := `echo "$CLAUDE_PLUGIN_ROOT" > '` + out + `'`
	r := runHook(context.Background(), cmd, maxHookTimeout, HookInput{}, "/plugin/install/dir")
	if r.Blocked {
		t.Fatalf("hook blocked: %s", r.Reason)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("hook did not write output: %v", err)
	}
	got := strings.TrimSpace(string(data))
	if got != "/plugin/install/dir" {
		t.Errorf("CLAUDE_PLUGIN_ROOT = %q; want /plugin/install/dir", got)
	}
}
