package hooks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/settings"
)

func cmdMatcher(matcher, command string, async bool) []settings.HookMatcher {
	return []settings.HookMatcher{{Matcher: matcher, Hooks: []settings.Hook{{Type: "command", Command: command, Async: async}}}}
}

func strPtr(s string) *string { return &s }

func TestRunUserPromptSubmit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	tests := []struct {
		name    string
		matcher string
		command string
		async   bool
		timeout int
		want    Result
	}{
		{name: "silent hook", command: "true"},
		{name: "plain stdout is context", command: "echo remember this", want: Result{AdditionalContext: "remember this"}},
		{name: "exit 2 blocks with stderr", command: "echo denied >&2; exit 2", want: Result{Blocked: true, Reason: "denied"}},
		{name: "other exit codes fail open", command: "echo denied >&2; exit 1"},
		{name: "json block", command: `printf '{"decision":"block","reason":"nope"}'`, want: Result{Blocked: true, Reason: "nope"}},
		{name: "continue false stops", command: `printf '{"continue":false,"stopReason":"bye","systemMessage":"note"}'`, want: Result{Stopped: true, StopReason: "bye", SystemMessage: "note"}},
		{name: "continue true is a no-op", command: `printf '{"continue":true}'`},
		{name: "hookSpecificOutput additionalContext", command: `printf '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"ctx"}}'`, want: Result{AdditionalContext: "ctx"}},
		{name: "session title and system message", command: `printf '{"systemMessage":"hi","hookSpecificOutput":{"sessionTitle":"T"}}'`, want: Result{SystemMessage: "hi", SessionTitle: "T"}},
		{name: "updatedPrompt", command: `printf '{"hookSpecificOutput":{"updatedPrompt":"new"}}'`, want: Result{UpdatedPrompt: strPtr("new")}},
		{name: "matcher is ignored", matcher: "Bash", command: "echo ctx", want: Result{AdditionalContext: "ctx"}},
		{name: "async flag is ignored", async: true, command: "echo ctx", want: Result{AdditionalContext: "ctx"}},
		{name: "async hook can still block", async: true, command: "echo no >&2; exit 2", want: Result{Blocked: true, Reason: "no"}},
		{name: "timeout fails open", timeout: 1, command: "exec sleep 30"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := cmdMatcher(tt.matcher, tt.command, tt.async)
			ms[0].Hooks[0].TimeoutSecs = tt.timeout
			got := RunUserPromptSubmit(context.Background(), ms, HookInput{SessionID: "s", Prompt: "hi"})
			if got.Blocked != tt.want.Blocked || got.Reason != tt.want.Reason ||
				got.Stopped != tt.want.Stopped || got.StopReason != tt.want.StopReason ||
				got.AdditionalContext != tt.want.AdditionalContext ||
				got.SystemMessage != tt.want.SystemMessage || got.SessionTitle != tt.want.SessionTitle {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			if (got.UpdatedPrompt == nil) != (tt.want.UpdatedPrompt == nil) ||
				(got.UpdatedPrompt != nil && *got.UpdatedPrompt != *tt.want.UpdatedPrompt) {
				t.Errorf("UpdatedPrompt = %v, want %v", got.UpdatedPrompt, tt.want.UpdatedPrompt)
			}
		})
	}
}

func TestRunUserPromptSubmit_MergesHooksInOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	ms := []settings.HookMatcher{
		{Hooks: []settings.Hook{{Type: "command", Command: "echo one"}, {Type: "command", Command: `printf '{"hookSpecificOutput":{"updatedPrompt":"a"}}'`}}},
		{Hooks: []settings.Hook{{Type: "command", Command: "echo two"}, {Type: "command", Command: `printf '{"hookSpecificOutput":{"updatedPrompt":"b"}}'`}}},
	}
	got := RunUserPromptSubmit(context.Background(), ms, HookInput{})
	if got.AdditionalContext != "one\ntwo" {
		t.Errorf("context = %q, want %q", got.AdditionalContext, "one\ntwo")
	}
	if got.UpdatedPrompt == nil || *got.UpdatedPrompt != "b" {
		t.Errorf("UpdatedPrompt = %v, want b (last wins)", got.UpdatedPrompt)
	}
}

func TestRunUserPromptSubmit_BlockShortCircuits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	ms := []settings.HookMatcher{{Hooks: []settings.Hook{
		{Type: "command", Command: "echo no >&2; exit 2"},
		{Type: "command", Command: "touch " + marker},
	}}}
	if got := RunUserPromptSubmit(context.Background(), ms, HookInput{}); !got.Blocked {
		t.Fatalf("expected block, got %+v", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a hook after the blocking one still ran")
	}
}

func TestRunUserPromptSubmit_PayloadFields(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	out := filepath.Join(t.TempDir(), "payload")
	ms := cmdMatcher("", "cat > "+out, false)
	RunUserPromptSubmit(context.Background(), ms, HookInput{SessionID: "sid", Prompt: "do it", Cwd: "/w", PermissionMode: "default"})
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"hook_event_name":"UserPromptSubmit"`, `"prompt":"do it"`, `"session_id":"sid"`, `"cwd":"/w"`, `"permission_mode":"default"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("payload %s missing %s", b, want)
		}
	}
}

func TestOtherEventsKeepNonZeroBlocks(t *testing.T) {
	r := RunPreToolUse(context.Background(), cmdMatcher("", "echo bad >&2; exit 1", false), "s", "Bash", nil)
	if !r.Blocked || r.Reason != "bad" {
		t.Errorf("PreToolUse non-zero exit must still block, got %+v", r)
	}
	// Plain stdout stays advisory for the other events.
	r = RunPostToolUse(context.Background(), cmdMatcher("", "echo hello", false), "s", "Bash", "out")
	if r.AdditionalContext != "" {
		t.Errorf("PostToolUse plain stdout became context: %+v", r)
	}
}

// continue:false must never override a deny: a PreToolUse hook that stops and
// denies has to block the tool.
func TestRunPreToolUse_ContinueFalseDoesNotBypassDeny(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	tests := []struct {
		name       string
		output     string
		wantReason string
	}{
		{"decision block", `{"continue":false,"decision":"block","reason":"nope"}`, "nope"},
		{"permissionDecision deny", `{"continue":false,"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"denied"}}`, "denied"},
		{"continue false alone", `{"continue":false,"stopReason":"halt"}`, "halt"},
		{"continue false without reason", `{"continue":false}`, "stopped by hook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := cmdMatcher("", "printf '"+tt.output+"'", false)
			r := RunPreToolUse(context.Background(), ms, "s", "Bash", map[string]any{"command": "rm -rf /"})
			if !r.Blocked || r.Reason != tt.wantReason {
				t.Errorf("got %+v, want blocked with %q", r, tt.wantReason)
			}
		})
	}
}

func TestHookOutputResult_StoppedKeepsDecision(t *testing.T) {
	r := HookOutput{Decision: "block", Reason: "why", StopReason: "s", Continue: new(bool)}.result("d")
	if !r.Blocked || r.Reason != "why" || !r.Stopped || r.StopReason != "s" {
		t.Errorf("got %+v, want both blocked and stopped", r)
	}
}

func TestRunUserPromptSubmit_FailOpenReturnsWarnings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	tests := []struct {
		name string
		hook settings.Hook
	}{
		{"exit 1", settings.Hook{Type: "command", Command: "echo boom >&2; exit 1"}},
		{"http without url", settings.Hook{Type: "http"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RunUserPromptSubmit(context.Background(), []settings.HookMatcher{{Hooks: []settings.Hook{tt.hook}}}, HookInput{})
			if got.Blocked || got.Stopped || len(got.Warnings) != 1 {
				t.Errorf("got %+v, want not blocked and one warning", got)
			}
		})
	}
	// Other events keep treating an unusable http hook as a block.
	r := RunPreToolUse(context.Background(), []settings.HookMatcher{{Hooks: []settings.Hook{{Type: "http"}}}}, "s", "Bash", nil)
	if !r.Blocked {
		t.Error("PreToolUse http hook without url must still block")
	}
}

// A later hook's block keeps what earlier hooks already reported.
func TestRunUserPromptSubmit_BlockKeepsEarlierMessages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	ms := []settings.HookMatcher{{Hooks: []settings.Hook{
		{Type: "command", Command: `printf '{"systemMessage":"first","hookSpecificOutput":{"sessionTitle":"Title"}}'`},
		{Type: "command", Command: "echo bad >&2; exit 1"},
		{Type: "command", Command: "echo stop >&2; exit 2"},
	}}}
	got := RunUserPromptSubmit(context.Background(), ms, HookInput{})
	if !got.Blocked || got.Reason != "stop" || got.SystemMessage != "first" || got.SessionTitle != "Title" || len(got.Warnings) != 1 {
		t.Errorf("got %+v", got)
	}
}
