package commands

import (
	"strings"
	"testing"

	"github.com/icehunter/claude-go/internal/permissions"
	"github.com/icehunter/claude-go/internal/settings"
)

func TestRegisterPermissionsCommand_NilGate(t *testing.T) {
	r := New()
	RegisterPermissionsCommand(r, nil)

	result, ok := r.Dispatch("/permissions")
	if !ok {
		t.Fatal("expected /permissions to be registered")
	}
	if result.Type != "text" {
		t.Errorf("result type = %q, want 'text'", result.Type)
	}
	if !strings.Contains(result.Text, "no gate") {
		t.Errorf("nil gate message should mention 'no gate', got: %q", result.Text)
	}
}

func TestRegisterPermissionsCommand_WithGate(t *testing.T) {
	r := New()
	gate := permissions.New(
		permissions.ModeDefault,
		[]string{"Bash(git log *)", "Edit"},
		[]string{"Bash(rm -rf *)"},
		[]string{"Bash(npm *)"},
	)
	RegisterPermissionsCommand(r, gate)

	result, ok := r.Dispatch("/permissions")
	if !ok {
		t.Fatal("expected /permissions to be registered")
	}
	if result.Type != "text" {
		t.Errorf("result type = %q, want 'text'", result.Type)
	}
	if !strings.Contains(result.Text, "default") {
		t.Errorf("should show mode 'default', got: %q", result.Text)
	}
	if !strings.Contains(result.Text, "Bash(git log *)") {
		t.Errorf("should show allow rule, got: %q", result.Text)
	}
	if !strings.Contains(result.Text, "Bash(rm -rf *)") {
		t.Errorf("should show deny rule, got: %q", result.Text)
	}
	if !strings.Contains(result.Text, "Bash(npm *)") {
		t.Errorf("should show ask rule, got: %q", result.Text)
	}
}

func TestRegisterPermissionsCommand_EmptyLists(t *testing.T) {
	r := New()
	gate := permissions.New(permissions.ModeBypassPermissions, nil, nil, nil)
	RegisterPermissionsCommand(r, gate)

	result, _ := r.Dispatch("/permissions")
	if !strings.Contains(result.Text, "bypassPermissions") {
		t.Errorf("should show mode, got: %q", result.Text)
	}
	if !strings.Contains(result.Text, "empty") {
		t.Errorf("empty lists should say 'empty', got: %q", result.Text)
	}
}

func TestRegisterHooksCommand_NilHooks(t *testing.T) {
	r := New()
	RegisterHooksCommand(r, nil)

	result, ok := r.Dispatch("/hooks")
	if !ok {
		t.Fatal("expected /hooks to be registered")
	}
	if !strings.Contains(result.Text, "no hooks") {
		t.Errorf("nil hooks message should mention 'no hooks', got: %q", result.Text)
	}
}

func TestRegisterHooksCommand_WithHooks(t *testing.T) {
	r := New()
	hooksConfig := &settings.HooksSettings{
		PreToolUse: []settings.HookMatcher{
			{
				Matcher: "Bash",
				Hooks:   []settings.Hook{{Type: "command", Command: "echo pre-bash"}},
			},
		},
		PostToolUse: []settings.HookMatcher{
			{
				Matcher: "",
				Hooks:   []settings.Hook{{Type: "command", Command: "logger post"}},
			},
		},
	}
	RegisterHooksCommand(r, hooksConfig)

	result, ok := r.Dispatch("/hooks")
	if !ok {
		t.Fatal("expected /hooks to be registered")
	}
	if result.Type != "text" {
		t.Errorf("result type = %q, want 'text'", result.Type)
	}
	if !strings.Contains(result.Text, "PreToolUse") {
		t.Errorf("should show PreToolUse section, got: %q", result.Text)
	}
	if !strings.Contains(result.Text, "echo pre-bash") {
		t.Errorf("should show hook command, got: %q", result.Text)
	}
	if !strings.Contains(result.Text, "PostToolUse") {
		t.Errorf("should show PostToolUse section, got: %q", result.Text)
	}
	if !strings.Contains(result.Text, "(all tools)") {
		t.Errorf("empty matcher should show '(all tools)', got: %q", result.Text)
	}
}

func TestRegisterHooksCommand_EmptyHooks(t *testing.T) {
	r := New()
	hooksConfig := &settings.HooksSettings{}
	RegisterHooksCommand(r, hooksConfig)

	result, _ := r.Dispatch("/hooks")
	if !strings.Contains(result.Text, "none") {
		t.Errorf("empty hooks should show '(none)', got: %q", result.Text)
	}
}
