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
	RegisterModelCommand(r, func() string { return "claude-sonnet-4-6" }, func(string) {}, nil, nil, nil)

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
	var got pickerPayload
	if err := json.Unmarshal([]byte(result.Text), &got); err != nil {
		t.Fatalf("unmarshal picker: %v", err)
	}
	if len(got.Items) < 6 {
		t.Fatalf("picker items len = %d, want grouped Claude and MCP items", len(got.Items))
	}
	if !got.Items[0].Section || got.Items[0].Label != "Claude Subscription" {
		t.Fatalf("first picker item = %#v, want Claude Subscription section", got.Items[0])
	}
	foundMCPSection := false
	for _, item := range got.Items {
		if item.Section && item.Label == "MCP local-router" {
			foundMCPSection = true
			break
		}
	}
	if !foundMCPSection {
		t.Fatalf("picker items missing MCP local-router section: %#v", got.Items)
	}
}

func TestRegisterModelCommand_LocalSelectionSwitchesProvider(t *testing.T) {
	r := New()
	RegisterModelCommand(r, func() string { return "claude-sonnet-4-6" }, func(string) {}, nil, nil, nil)

	result, ok := r.Dispatch("/model local")
	if !ok {
		t.Fatal("expected /model local to dispatch")
	}
	if result.Type != "provider-switch" || result.Provider == nil {
		t.Fatalf("/model local = %#v", result)
	}
	if result.Provider.Kind != "mcp" || result.Provider.Server != "local-router" || result.Provider.DirectTool != "local_direct" {
		t.Fatalf("provider = %#v, want local-router MCP provider", result.Provider)
	}
}

func TestRegisterModelCommand_ConfiguredLocalTargets(t *testing.T) {
	r := New()
	providers := map[string]settings.ActiveProviderSettings{
		"mcp.fast-router": {
			Kind:          "mcp",
			Server:        "fast-router",
			Model:         "llama-fast",
			DirectTool:    "direct",
			ImplementTool: "implement",
		},
	}
	RegisterModelCommand(r, func() string { return "claude-sonnet-4-6" }, func(string) {}, nil, nil, providers)

	result, ok := r.Dispatch("/models")
	if !ok {
		t.Fatal("expected /models to dispatch")
	}
	var got pickerPayload
	if err := json.Unmarshal([]byte(result.Text), &got); err != nil {
		t.Fatalf("unmarshal picker: %v", err)
	}
	found := false
	for _, item := range got.Items {
		if item.Value == "local:fast-router" && item.Label == "llama-fast" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("picker items missing configured fast-router: %#v", got.Items)
	}

	result, ok = r.Dispatch("/model local:fast-router")
	if !ok {
		t.Fatal("expected /model local:fast-router to dispatch")
	}
	if result.Provider == nil || result.Provider.Server != "fast-router" || result.Provider.DirectTool != "direct" || result.Provider.ImplementTool != "implement" {
		t.Fatalf("provider = %#v, want configured fast-router provider", result.Provider)
	}
}

func TestRegisterModelCommand_ClaudeSelectionSwitchesProvider(t *testing.T) {
	r := New()
	RegisterModelCommand(r, func() string { return "claude-sonnet-4-6" }, func(string) {}, nil, nil, nil)

	result, ok := r.Dispatch("/model opus")
	if !ok {
		t.Fatal("expected /model opus to dispatch")
	}
	if result.Type != "provider-switch" || result.Provider == nil {
		t.Fatalf("/model opus = %#v", result)
	}
	if result.Provider.Kind != "claude-subscription" || result.Provider.Model != "claude-opus-4-7" {
		t.Fatalf("provider = %#v, want Claude Opus provider", result.Provider)
	}
}

func TestRegisterModelCommand_AnthropicSelectionSwitchesProvider(t *testing.T) {
	r := New()
	RegisterModelCommand(
		r,
		func() string { return "claude-sonnet-4-6" },
		func(string) {},
		func() string { return "anthropic-api" },
		nil,
		nil,
	)

	result, ok := r.Dispatch("/model opus")
	if !ok {
		t.Fatal("expected /model opus to dispatch")
	}
	if result.Type != "provider-switch" || result.Provider == nil {
		t.Fatalf("/model opus = %#v", result)
	}
	if result.Provider.Kind != "anthropic-api" || result.Provider.Model != "claude-opus-4-7" {
		t.Fatalf("provider = %#v, want Anthropic API Opus provider", result.Provider)
	}

	picker, ok := r.Dispatch("/models")
	if !ok {
		t.Fatal("expected /models to dispatch")
	}
	var got pickerPayload
	if err := json.Unmarshal([]byte(picker.Text), &got); err != nil {
		t.Fatalf("unmarshal picker: %v", err)
	}
	if !got.Items[0].Section || got.Items[0].Label != "Anthropic API" {
		t.Fatalf("first picker item = %#v, want Anthropic API section", got.Items[0])
	}
	if got.Items[1].Value != "anthropic-api:claude-opus-4-7" {
		t.Fatalf("first model value = %q, want anthropic-api prefix", got.Items[1].Value)
	}
}

func TestRegisterModelCommand_RoleSelection(t *testing.T) {
	r := New()
	called := false
	RegisterModelCommand(r, func() string { return "claude-sonnet-4-6" }, func(string) { called = true }, nil, nil, nil)

	result, ok := r.Dispatch("/model --role planning opus")
	if !ok {
		t.Fatal("expected /model --role planning opus to dispatch")
	}
	if result.Role != settings.RolePlanning {
		t.Fatalf("role = %q, want planning", result.Role)
	}
	if result.Provider == nil || result.Provider.Model != "claude-opus-4-7" {
		t.Fatalf("provider = %#v, want opus provider", result.Provider)
	}
	if called {
		t.Fatal("non-main role selection should not switch the live model")
	}
}

func TestRegisterLocalCommands_DefaultDirect(t *testing.T) {
	r := New()
	RegisterLocalCommands(r, nil, nil, nil)

	result, ok := r.Dispatch("/local explain this")
	if !ok {
		t.Fatal("expected /local to dispatch")
	}
	if result.Type != "local-call" {
		t.Fatalf("result type = %q, want local-call", result.Type)
	}
	var call LocalCall
	if err := json.Unmarshal([]byte(result.Text), &call); err != nil {
		t.Fatalf("unmarshal local call: %v", err)
	}
	if call.Server != "local-router" {
		t.Errorf("server = %q, want local-router", call.Server)
	}
	if call.Tool != "mcp__local_router__local_direct" {
		t.Errorf("tool = %q, want direct local router tool", call.Tool)
	}
	if call.Arguments["prompt"] != "explain this" {
		t.Errorf("prompt = %#v, want explain this", call.Arguments["prompt"])
	}
	if call.Arguments["mode"] != "direct" || call.Arguments["include_review_reminder"] != false {
		t.Errorf("direct args = %#v", call.Arguments)
	}
}

func TestRegisterLocalCommands_DefaultImplementDiff(t *testing.T) {
	r := New()
	RegisterLocalCommands(r, nil, nil, nil)

	result, ok := r.Dispatch("/local-implement fix parser")
	if !ok {
		t.Fatal("expected /local-implement to dispatch")
	}
	if result.Type != "local-call" {
		t.Fatalf("result type = %q, want local-call", result.Type)
	}
	var call LocalCall
	if err := json.Unmarshal([]byte(result.Text), &call); err != nil {
		t.Fatalf("unmarshal local call: %v", err)
	}
	if call.Tool != "mcp__local_router__local_implement" {
		t.Errorf("tool = %q, want implement local router tool", call.Tool)
	}
	if call.Arguments["prompt"] != "fix parser" {
		t.Errorf("prompt = %#v, want fix parser", call.Arguments["prompt"])
	}
	if call.Arguments["output_format"] != "diff" || call.Arguments["include_review_reminder"] != false {
		t.Errorf("implement args = %#v", call.Arguments)
	}
}

func TestRegisterLocalCommands_LocalMode(t *testing.T) {
	r := New()
	RegisterLocalCommands(r, nil, nil, nil)

	result, ok := r.Dispatch("/local-mode on")
	if !ok {
		t.Fatal("expected /local-mode to dispatch")
	}
	if result.Type != "local-mode" || result.Text != "on\tlocal-router" {
		t.Fatalf("local-mode on = %#v", result)
	}

	result, ok = r.Dispatch("/local-mode off")
	if !ok {
		t.Fatal("expected /local-mode off to dispatch")
	}
	if result.Type != "local-mode" || result.Text != "off\t" {
		t.Fatalf("local-mode off = %#v", result)
	}
}

func TestRegisterLocalCommands_HiddenFromDiscovery(t *testing.T) {
	r := New()
	RegisterLocalCommands(r, nil, nil, nil)

	for _, cmd := range r.All() {
		if strings.HasPrefix(cmd.Name, "local") {
			t.Fatalf("local debug command should be hidden from discovery: %#v", cmd)
		}
	}
	if _, ok := r.Dispatch("/local still works"); !ok {
		t.Fatal("hidden /local should still dispatch")
	}
}

func TestRegisterLocalCommands_UsesActiveMCPProvider(t *testing.T) {
	r := New()
	provider := &settings.ActiveProviderSettings{
		Kind:          "mcp",
		Server:        "gpu-router",
		DirectTool:    "chat",
		ImplementTool: "diff",
		Model:         "deepseek-coder",
	}
	RegisterLocalCommands(r, nil, provider, nil)

	result, ok := r.Dispatch("/local explain this")
	if !ok {
		t.Fatal("expected /local to dispatch")
	}
	var call LocalCall
	if err := json.Unmarshal([]byte(result.Text), &call); err != nil {
		t.Fatalf("unmarshal local call: %v", err)
	}
	if call.Server != "gpu-router" || call.Tool != "mcp__gpu_router__chat" {
		t.Fatalf("direct call = %#v", call)
	}

	result, ok = r.Dispatch("/local-implement fix parser")
	if !ok {
		t.Fatal("expected /local-implement to dispatch")
	}
	if err := json.Unmarshal([]byte(result.Text), &call); err != nil {
		t.Fatalf("unmarshal implement call: %v", err)
	}
	if call.Server != "gpu-router" || call.Tool != "mcp__gpu_router__diff" {
		t.Fatalf("implement call = %#v", call)
	}

	result, ok = r.Dispatch("/local-mode on")
	if !ok {
		t.Fatal("expected /local-mode to dispatch")
	}
	if result.Type != "local-mode" || result.Text != "on\tgpu-router" {
		t.Fatalf("local-mode on = %#v", result)
	}
}

func TestRegisterLocalCommands_ConfiguredTargets(t *testing.T) {
	r := New()
	providers := map[string]settings.ActiveProviderSettings{
		"mcp.fast-router": {
			Kind:          "mcp",
			Server:        "fast-router",
			Model:         "llama-fast",
			DirectTool:    "direct",
			ImplementTool: "implement",
		},
	}
	RegisterLocalCommands(r, nil, nil, providers)

	result, ok := r.Dispatch("/local list")
	if !ok {
		t.Fatal("expected /local list to dispatch")
	}
	if result.Type != "text" || !strings.Contains(result.Text, "fast-router") || !strings.Contains(result.Text, "configured") {
		t.Fatalf("/local list = %#v, want configured fast-router", result)
	}

	result, ok = r.Dispatch("/local fast-router explain this")
	if !ok {
		t.Fatal("expected /local fast-router to dispatch")
	}
	var call LocalCall
	if err := json.Unmarshal([]byte(result.Text), &call); err != nil {
		t.Fatalf("unmarshal direct call: %v", err)
	}
	if call.Server != "fast-router" || call.Tool != "mcp__fast_router__direct" || call.Arguments["prompt"] != "explain this" {
		t.Fatalf("direct call = %#v", call)
	}
}

func TestRegisterAccountCommand_AccountsAlias(t *testing.T) {
	r := New()
	RegisterAccountCommand(r)

	result, ok := r.Dispatch("/accounts")
	if !ok {
		t.Fatal("expected /accounts to dispatch")
	}
	if result.Type != "settings-panel" || result.Text != "accounts" {
		t.Fatalf("result = %#v, want accounts settings panel", result)
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
