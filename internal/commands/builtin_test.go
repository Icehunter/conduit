package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

func TestPickerResult_RoundTrip(t *testing.T) {
	r := pickerResult("theme", "Pick a theme", "dark",
		[]string{"dark", "light"}, []string{"Dark", "Light mode"})
	if r.Type != "picker" {
		t.Fatalf("Type = %q, want picker", r.Type)
	}
	if r.Model != "theme" {
		t.Fatalf("Model = %q, want theme", r.Model)
	}
	var got pickerPayload
	if err := json.Unmarshal([]byte(r.Text), &got); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if got.Title != "Pick a theme" || got.Current != "dark" {
		t.Errorf("title/current mismatch: %+v", got)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(got.Items))
	}
	if got.Items[0].Value != "dark" || got.Items[0].Label != "Dark" {
		t.Errorf("item[0] = %+v", got.Items[0])
	}
	if got.Items[1].Label != "Light mode" {
		t.Errorf("item[1] label = %q, want 'Light mode'", got.Items[1].Label)
	}
}

func TestPickerResult_LabelsFallbackToValues(t *testing.T) {
	r := pickerResult("model", "Pick", "", []string{"a", "b", "c"})
	var got pickerPayload
	_ = json.Unmarshal([]byte(r.Text), &got)
	for i, it := range got.Items {
		if it.Label != it.Value {
			t.Errorf("item[%d] label %q should fall back to value %q", i, it.Label, it.Value)
		}
	}
}

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

func TestRegisterBuiltins_CommandPickerCommand(t *testing.T) {
	r := New()
	RegisterBuiltins(r)

	result, ok := r.Dispatch("/commands")
	if !ok {
		t.Fatal("expected /commands to be registered")
	}
	if result.Type != "commands" {
		t.Errorf("result type = %q, want 'commands'", result.Type)
	}
}

func TestRegisterModelCommand_ModelsAliasOpensPicker(t *testing.T) {
	r := New()
	RegisterModelCommand(r, func() string { return "claude-sonnet-4-6" }, func(string) {})

	result, ok := r.Dispatch("/models")
	if !ok {
		t.Fatal("expected /models to be registered")
	}
	if result.Type != "picker" {
		t.Errorf("result type = %q, want 'picker'", result.Type)
	}
	if result.Model != "model" {
		t.Errorf("result model = %q, want 'model'", result.Model)
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

func TestRewindCommand_NoCallback(t *testing.T) {
	r := New()
	state := &SessionState{}
	RegisterSessionCommands(r, state)

	result, ok := r.Dispatch("/rewind")
	if !ok {
		t.Fatal("expected /rewind to be registered")
	}
	if result.Type != "text" {
		t.Errorf("result type = %q, want 'text'", result.Type)
	}
}

func TestRewindCommand_WithCallback_DefaultN(t *testing.T) {
	removed := 0
	r := New()
	state := &SessionState{
		Rewind: func(n int) int {
			removed = n
			return n
		},
	}
	RegisterSessionCommands(r, state)

	result, _ := r.Dispatch("/rewind")
	if result.Type != "rewind" {
		t.Errorf("expected type 'rewind'; got %q", result.Type)
	}
	if removed != 1 {
		t.Errorf("expected Rewind(1) called; got Rewind(%d)", removed)
	}
}

func TestRewindCommand_WithCallback_ExplicitN(t *testing.T) {
	removed := 0
	r := New()
	state := &SessionState{
		Rewind: func(n int) int {
			removed = n
			return n
		},
	}
	RegisterSessionCommands(r, state)

	result, _ := r.Dispatch("/rewind 3")
	if result.Type != "rewind" {
		t.Errorf("expected type 'rewind'; got %q", result.Type)
	}
	if removed != 3 {
		t.Errorf("expected Rewind(3) called; got Rewind(%d)", removed)
	}
}
