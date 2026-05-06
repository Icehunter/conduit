package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/planusage"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/settings"
)

// plainText strips ANSI escapes so substring assertions don't fail when
// lipgloss v2 emits per-rune styling that interleaves with the text.
func plainText(s string) string { return ansi.Strip(s) }

func saveTestClaudeAccount(t *testing.T, email string) {
	t.Helper()
	if err := settings.SaveConduitRawKey("accounts", map[string]any{
		"active": "claude-ai:" + email,
		"accounts": map[string]any{
			"claude-ai:" + email: map[string]any{
				"email": email,
				"kind":  "claude-ai",
			},
		},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
}

func TestRenderWelcomeSectionDoesNotLeakANSI(t *testing.T) {
	out := renderWelcomeSection("Session", 40)
	plain := plainText(out)
	if strings.Contains(plain, "[38;") || strings.Contains(plain, "[m") {
		t.Fatalf("welcome section leaked raw ANSI: %q", plain)
	}
	if !strings.Contains(plain, "Session") || !strings.Contains(plain, "─") {
		t.Fatalf("welcome section missing label/rule: %q", plain)
	}
}

func TestWelcomeCardUsesLocalModeDisplay(t *testing.T) {
	m := Model{cfg: Config{Version: "test"}, modelName: "claude-sonnet-4-6", localMode: true, localModeServer: "local-router"}
	msg := m.welcomeCard()
	if !strings.Contains(msg.Content, "MCP · qwen3-coder · local-router") {
		t.Fatalf("welcome card content = %q, want local model display", msg.Content)
	}
}

func TestWelcomeCardUsesActiveMCPProviderDisplay(t *testing.T) {
	m := Model{
		cfg:       Config{Version: "test"},
		modelName: "claude-sonnet-4-6",
		activeProvider: &settings.ActiveProviderSettings{
			Kind:       "mcp",
			Server:     "local-router",
			Model:      "qwen3-coder",
			DirectTool: "local_direct",
		},
	}
	msg := m.welcomeCard()
	if !strings.Contains(msg.Content, "MCP · qwen3-coder · local-router") {
		t.Fatalf("welcome card content = %q, want active MCP provider display", msg.Content)
	}
}

func TestPlanModeUsesPlanningProvider(t *testing.T) {
	m := Model{
		modelName:          "claude-sonnet-4-6",
		permissionMode:     permissions.ModePlan,
		activeProvider:     &settings.ActiveProviderSettings{Kind: "claude-subscription", Model: "claude-sonnet-4-6"},
		providers:          map[string]settings.ActiveProviderSettings{"mcp.plan-router": {Kind: "mcp", Server: "plan-router", Model: "planner"}},
		roles:              map[string]string{settings.RolePlanning: "mcp.plan-router"},
		localDirectTool:    "local_direct",
		localImplementTool: "local_implement",
	}
	provider, ok := m.activeMCPProvider()
	if !ok {
		t.Fatal("activeMCPProvider should use planning role in plan mode")
	}
	if provider.Server != "plan-router" {
		t.Fatalf("server = %q, want plan-router", provider.Server)
	}
	if got := m.activeModelDisplayName(); !strings.Contains(got, "MCP · planner · plan-router") {
		t.Fatalf("display = %q, want planning provider display", got)
	}
}

func TestPermissionModeChangeUpdatesLoopModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	saveTestClaudeAccount(t, "test@example.com")
	loop := agent.NewLoop(nil, nil, agent.LoopConfig{Model: "claude-sonnet-4-6"})
	m := Model{
		cfg:            Config{Loop: loop},
		modelName:      "claude-sonnet-4-6",
		activeProvider: &settings.ActiveProviderSettings{Kind: "claude-subscription", Model: "claude-sonnet-4-6", Account: "test@example.com"},
		providers: map[string]settings.ActiveProviderSettings{
			"claude-subscription.claude-sonnet-4-6": {Kind: "claude-subscription", Model: "claude-sonnet-4-6", Account: "test@example.com"},
			"claude-subscription.claude-opus-4-7":   {Kind: "claude-subscription", Model: "claude-opus-4-7", Account: "test@example.com"},
		},
		roles: map[string]string{
			settings.RoleMain:     "claude-subscription.claude-sonnet-4-6",
			settings.RolePlanning: "claude-subscription.claude-opus-4-7",
		},
		permissionMode: permissions.ModeDefault,
	}

	m.applyPermissionMode(permissions.ModePlan)
	if loop.Model() != "claude-opus-4-7" {
		t.Fatalf("loop model = %q, want planning model", loop.Model())
	}
	if got := m.activeModelDisplayName(); got != "Claude Subscription · claude-opus-4-7 · test@example.com" {
		t.Fatalf("display model = %q, want planning provider display", got)
	}

	m.applyPermissionMode(permissions.ModeBypassPermissions)
	if loop.Model() != "claude-sonnet-4-6" {
		t.Fatalf("loop model = %q, want main model after auto mode", loop.Model())
	}
}

func TestModeRoleRoutingDefaultVsMain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	saveTestClaudeAccount(t, "test@example.com")
	m := Model{
		modelName:      "claude-sonnet-4-6",
		activeProvider: &settings.ActiveProviderSettings{Kind: "mcp", Server: "local-router", Model: "qwen3-coder"},
		providers: map[string]settings.ActiveProviderSettings{
			"mcp.local-router":                      {Kind: "mcp", Server: "local-router", Model: "qwen3-coder"},
			"claude-subscription.claude-sonnet-4-6": {Kind: "claude-subscription", Model: "claude-sonnet-4-6", Account: "test@example.com"},
		},
		roles: map[string]string{
			settings.RoleDefault: "mcp.local-router",
			settings.RoleMain:    "claude-subscription.claude-sonnet-4-6",
		},
	}

	m.permissionMode = permissions.ModeDefault
	if got := m.activeModelDisplayName(); got != "MCP · qwen3-coder · local-router" {
		t.Fatalf("default mode display = %q, want local default provider", got)
	}

	m.permissionMode = permissions.ModeBypassPermissions
	if got := m.activeModelDisplayName(); got != "Claude Subscription · claude-sonnet-4-6 · test@example.com" {
		t.Fatalf("auto mode display = %q, want main provider", got)
	}
}

func TestStartupWelcomeUsesSavedPermissionModeProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	saveTestClaudeAccount(t, "syndicated.life@gmail.com")
	loop := agent.NewLoop(nil, nil, agent.LoopConfig{Model: "stale-startup-model"})
	gate := permissions.New(permissions.ModeBypassPermissions, nil, nil, nil)
	m := New(Config{
		Version:   "test",
		ModelName: "stale-startup-model",
		Loop:      loop,
		Gate:      gate,
		InitialActiveProvider: &settings.ActiveProviderSettings{
			Kind:       "mcp",
			Server:     "local-router",
			Model:      "qwen3-coder",
			DirectTool: "local_direct",
		},
		InitialProviders: map[string]settings.ActiveProviderSettings{
			"mcp.local-router":                      {Kind: "mcp", Server: "local-router", Model: "qwen3-coder", DirectTool: "local_direct"},
			"claude-subscription.claude-sonnet-4-6": {Kind: "claude-subscription", Model: "claude-sonnet-4-6", Account: "syndicated.life@gmail.com"},
		},
		InitialRoles: map[string]string{
			settings.RoleDefault: "mcp.local-router",
			settings.RoleMain:    "claude-subscription.claude-sonnet-4-6",
		},
	})
	if loop.Model() != "claude-sonnet-4-6" {
		t.Fatalf("startup loop model = %q, want main role model", loop.Model())
	}
	if len(m.messages) == 0 {
		t.Fatal("startup should render welcome card")
	}
	content := plainText(m.messages[0].Content)
	if !strings.Contains(content, "Claude Subscription · claude-sonnet-4-6 · syndicated.life@gmail.com") {
		t.Fatalf("welcome card = %q, want main role provider", content)
	}
	if strings.Contains(content, "MCP · qwen3-coder · local-router") {
		t.Fatalf("welcome card used default MCP provider in auto mode: %q", content)
	}
}

func TestPermissionModeChangeRefreshesWelcomeViewport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	saveTestClaudeAccount(t, "test@example.com")
	loop := agent.NewLoop(nil, nil, agent.LoopConfig{Model: "claude-sonnet-4-6"})
	m := New(Config{
		Version:   "test",
		ModelName: "claude-sonnet-4-6",
		Loop:      loop,
		InitialActiveProvider: &settings.ActiveProviderSettings{
			Kind:    "claude-subscription",
			Model:   "claude-sonnet-4-6",
			Account: "test@example.com",
		},
		InitialProviders: map[string]settings.ActiveProviderSettings{
			"claude-subscription.claude-sonnet-4-6": {Kind: "claude-subscription", Model: "claude-sonnet-4-6", Account: "test@example.com"},
			"claude-subscription.claude-opus-4-7":   {Kind: "claude-subscription", Model: "claude-opus-4-7", Account: "test@example.com"},
		},
		InitialRoles: map[string]string{
			settings.RoleMain:     "claude-subscription.claude-sonnet-4-6",
			settings.RolePlanning: "claude-subscription.claude-opus-4-7",
		},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	m.applyPermissionMode(permissions.ModePlan)
	if out := plainText(m.vp.View()); !strings.Contains(out, "claude-opus-4-7") {
		t.Fatalf("welcome viewport did not refresh to planning model: %q", out)
	}

	m.applyPermissionMode(permissions.ModeBypassPermissions)
	if out := plainText(m.vp.View()); !strings.Contains(out, "claude-sonnet-4-6") {
		t.Fatalf("welcome viewport did not refresh back to main model: %q", out)
	}
}

func TestModelPickerTabCyclesProviderRoles(t *testing.T) {
	m := Model{
		providers: map[string]settings.ActiveProviderSettings{
			"mcp.local-router": {Kind: "mcp", Server: "local-router", Model: "qwen3-coder"},
		},
		roles: map[string]string{settings.RoleBackground: "mcp.local-router"},
		picker: &pickerState{
			kind:     "model",
			title:    "Pick a model",
			role:     settings.RoleDefault,
			current:  "claude-sonnet-4-6",
			selected: 0,
			items: []pickerItem{
				{Value: "claude-sonnet-4-6", Label: "Sonnet"},
				{Value: "local:local-router", Label: "qwen3-coder"},
			},
		},
	}
	updated, _ := m.handlePickerKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if updated.picker.role != settings.RoleMain {
		t.Fatalf("role = %q, want main", updated.picker.role)
	}
	updated, _ = updated.handlePickerKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if updated.picker.role != settings.RoleBackground {
		t.Fatalf("role after second tab = %q, want background", updated.picker.role)
	}
	if updated.picker.current != "local:local-router" {
		t.Fatalf("current = %q, want local role provider", updated.picker.current)
	}
}

func TestModelPickerFiltersDeletedAccountProviders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	if err := settings.SaveConduitRawKey("accounts", map[string]any{
		"active":   "",
		"accounts": map[string]any{},
	}); err != nil {
		t.Fatalf("save empty accounts: %v", err)
	}
	m := Model{}
	items := m.filterModelPickerItems([]pickerItem{
		{Label: "Claude Subscription", Section: true},
		{Value: "claude-subscription:claude-sonnet-4-6", Label: "Sonnet"},
		{Label: "MCP local-router", Section: true},
		{Value: "local:local-router", Label: "qwen3-coder"},
	})
	if len(items) != 2 {
		t.Fatalf("items = %#v, want only MCP section and row", items)
	}
	if !items[0].Section || items[0].Label != "MCP local-router" || items[1].Value != "local:local-router" {
		t.Fatalf("items = %#v, want stale account section filtered", items)
	}
}

func TestPlanUsageFetchUsesCurrentRoleProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	saveTestClaudeAccount(t, "work@example.com")
	if err := settings.SaveConduitRawKey("accounts", map[string]any{
		"active": "claude-ai:work@example.com",
		"accounts": map[string]any{
			"claude-ai:work@example.com": map[string]any{
				"email": "work@example.com",
				"kind":  "claude-ai",
			},
			"claude-ai:personal@example.com": map[string]any{
				"email": "personal@example.com",
				"kind":  "claude-ai",
			},
		},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	var got settings.ActiveProviderSettings
	m := Model{
		usageStatusEnabled: true,
		permissionMode:     permissions.ModePlan,
		cfg: Config{
			FetchPlanUsage: func(_ context.Context, provider settings.ActiveProviderSettings) (planusage.Info, error) {
				got = provider
				return planusage.Info{}, nil
			},
		},
		providers: map[string]settings.ActiveProviderSettings{
			"claude-subscription.work@example.com.claude-sonnet-4-6":     {Kind: "claude-subscription", Account: "work@example.com", Model: "claude-sonnet-4-6"},
			"claude-subscription.personal@example.com.claude-sonnet-4-6": {Kind: "claude-subscription", Account: "personal@example.com", Model: "claude-sonnet-4-6"},
		},
		roles: map[string]string{
			settings.RoleMain:     "claude-subscription.work@example.com.claude-sonnet-4-6",
			settings.RolePlanning: "claude-subscription.personal@example.com.claude-sonnet-4-6",
		},
	}
	_, cmd := m.startPlanUsageFetch()
	if cmd == nil {
		t.Fatal("startPlanUsageFetch returned nil command")
	}
	_ = cmd()
	if got.Account != "personal@example.com" || got.Model != "claude-sonnet-4-6" {
		t.Fatalf("usage provider = %#v, want planning personal provider", got)
	}
}

func TestWelcomeCardUsesRoleAccountMetadata(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	if err := settings.SaveConduitRawKey("accounts", map[string]any{
		"active": "claude-ai:work@example.com",
		"accounts": map[string]any{
			"claude-ai:work@example.com": map[string]any{
				"email":             "work@example.com",
				"kind":              "claude-ai",
				"subscription_type": "Claude Team",
			},
			"claude-ai:personal@example.com": map[string]any{
				"email":             "personal@example.com",
				"kind":              "claude-ai",
				"subscription_type": "Claude Max",
			},
		},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	m := Model{
		cfg:            Config{Version: "test", Profile: profile.Info{Email: "personal@example.com", SubscriptionType: "Claude Max"}},
		permissionMode: permissions.ModeDefault,
		providers: map[string]settings.ActiveProviderSettings{
			"claude-subscription.work@example.com.claude-sonnet-4-6": {Kind: "claude-subscription", Account: "work@example.com", Model: "claude-sonnet-4-6"},
		},
		roles: map[string]string{settings.RoleDefault: "claude-subscription.work@example.com.claude-sonnet-4-6"},
	}
	msg := m.welcomeCard()
	if !strings.Contains(msg.Content, "Claude Team") {
		t.Fatalf("welcome card = %q, want work account Claude Team metadata", msg.Content)
	}
	if strings.Contains(msg.Content, "Claude Max") {
		t.Fatalf("welcome card leaked active profile metadata: %q", msg.Content)
	}
}

func TestWelcomeCardDoesNotBorrowProfileForDifferentRoleAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	if err := settings.SaveConduitRawKey("accounts", map[string]any{
		"active": "claude-ai:personal@example.com",
		"accounts": map[string]any{
			"claude-ai:work@example.com": map[string]any{
				"email": "work@example.com",
				"kind":  "claude-ai",
			},
			"claude-ai:personal@example.com": map[string]any{
				"email":             "personal@example.com",
				"kind":              "claude-ai",
				"subscription_type": "Claude Max",
			},
		},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	m := Model{
		cfg:            Config{Version: "test", Profile: profile.Info{Email: "personal@example.com", SubscriptionType: "Claude Max"}},
		permissionMode: permissions.ModePlan,
		providers: map[string]settings.ActiveProviderSettings{
			"claude-subscription.work@example.com.claude-opus-4-7": {Kind: "claude-subscription", Account: "work@example.com", Model: "claude-opus-4-7"},
		},
		roles: map[string]string{settings.RolePlanning: "claude-subscription.work@example.com.claude-opus-4-7"},
	}
	msg := m.welcomeCard()
	if !strings.Contains(msg.Content, "work@example.com") {
		t.Fatalf("welcome card = %q, want work account email", msg.Content)
	}
	if strings.Contains(msg.Content, "Claude Max") {
		t.Fatalf("welcome card borrowed personal profile metadata: %q", msg.Content)
	}
}

func TestRenderUsageFooterLocalModeOmitsClaudeResets(t *testing.T) {
	m := Model{usageStatusEnabled: true, localMode: true, localModeServer: "local-router"}
	out := plainText(m.renderUsageFooter(80))
	if strings.Contains(out, "unknown") || strings.Contains(out, "tomorrow") {
		t.Fatalf("local usage footer should not show Claude reset text: %q", out)
	}
	if !strings.Contains(out, "Provider") || !strings.Contains(out, "MCP · qwen3-coder") {
		t.Fatalf("local usage footer missing provider row: %q", out)
	}
}

func TestLocalPromptFromContentIncludesAtMentionContent(t *testing.T) {
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.WriteFile(filepath.Join(dir, "model.go"), []byte("package tui\n\nfunc RealFunction() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := Model{}
	content := m.userTextContent("what functions are in @./model.go?")
	prompt := localPromptFromContent(content)
	if !strings.Contains(prompt, `<file_content path="model.go">`) || !strings.Contains(prompt, "func RealFunction()") {
		t.Fatalf("local prompt = %q, want expanded @file content", prompt)
	}
	if !strings.Contains(prompt, "what functions are in @./model.go?") {
		t.Fatalf("local prompt = %q, want original prompt", prompt)
	}
}

func TestAcceptAtMatchDirectoryKeepsPickerOpenForNestedPath(t *testing.T) {
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.MkdirAll(filepath.Join(dir, "internal", "tui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "tui", "model.go"), []byte("package tui\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := New(Config{})
	m.input.SetValue("@internal")
	m.atMatches = []string{"internal"}
	m = m.acceptAtMatch()
	if got, want := m.input.Value(), "@internal/"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if len(m.atMatches) == 0 {
		t.Fatal("directory completion should keep nested @ picker matches open")
	}
}

func TestLocalProviderChatResultAppendsAssistantHistory(t *testing.T) {
	m := New(Config{})
	m.turnID = 7
	m.running = true
	m.history = []api.Message{{
		Role:    "user",
		Content: []api.ContentBlock{{Type: "text", Text: "hello"}},
	}}

	updated, _ := m.Update(localCallDoneMsg{
		turnID: 7,
		chat:   true,
		call:   commands.LocalCall{Server: "local-router"},
		text:   "hi from local",
	})
	got := updated.(Model)
	if len(got.history) != 2 || got.history[1].Role != "assistant" || got.history[1].Content[0].Text != "hi from local" {
		t.Fatalf("history = %#v, want local assistant response appended", got.history)
	}
	if got.history[1].ProviderKind != "mcp" || got.history[1].Provider != "local-router" {
		t.Fatalf("provider metadata = %q/%q, want mcp/local-router", got.history[1].ProviderKind, got.history[1].Provider)
	}
	if len(got.messages) == 0 || got.messages[len(got.messages)-1].Role != RoleLocal {
		t.Fatalf("messages = %#v, want rendered local message", got.messages)
	}
}

func TestHistoryToDisplayMessagesRestoresLocalProvider(t *testing.T) {
	msgs := historyToDisplayMessages([]api.Message{{
		Role:         "assistant",
		Content:      []api.ContentBlock{{Type: "text", Text: "package main\n\nfunc main() {}"}},
		ProviderKind: "mcp",
		Provider:     "local-router",
	}})
	if len(msgs) != 1 || msgs[0].Role != RoleLocal || msgs[0].ToolName != "local-router" {
		t.Fatalf("messages = %#v, want restored local provider display", msgs)
	}
}

func TestPersistClaudeActiveProviderUpdatesAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)

	if suffix := persistClaudeActiveProvider("claude-sonnet-4-6", "first@example.com"); suffix != "" {
		t.Fatalf("persist first returned suffix %q", suffix)
	}
	if suffix := persistClaudeActiveProvider("claude-sonnet-4-6", "second@example.com"); suffix != "" {
		t.Fatalf("persist second returned suffix %q", suffix)
	}

	data, err := os.ReadFile(settings.ConduitSettingsPath())
	if err != nil {
		t.Fatalf("read conduit settings: %v", err)
	}
	var raw struct {
		ActiveProvider settings.ActiveProviderSettings `json:"activeProvider"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw.ActiveProvider.Account != "second@example.com" {
		t.Fatalf("account = %q, want second@example.com", raw.ActiveProvider.Account)
	}
}

func TestRenderMarkdown_Heading1(t *testing.T) {
	out := plainText(renderMarkdown("# Hello World", 80))
	if !strings.Contains(out, "Hello World") {
		t.Errorf("h1 text missing: %q", out)
	}
}

func TestRenderMarkdown_Heading2(t *testing.T) {
	out := plainText(renderMarkdown("## Section", 80))
	if !strings.Contains(out, "Section") {
		t.Errorf("h2 text missing: %q", out)
	}
}

func TestRenderMarkdown_Strikethrough(t *testing.T) {
	out := plainText(renderMarkdown("~~deleted text~~", 80))
	// Should contain the text (strikethrough styling is cosmetic)
	if !strings.Contains(out, "deleted text") {
		t.Errorf("strikethrough text missing: %q", out)
	}
}

func TestRenderMarkdown_Italic(t *testing.T) {
	out := plainText(renderMarkdown("*italic* and _also italic_", 80))
	if !strings.Contains(out, "italic") {
		t.Errorf("italic text missing: %q", out)
	}
}

func TestRenderMarkdown_TaskList_Unchecked(t *testing.T) {
	out := renderMarkdown("- [ ] todo item", 80)
	if !strings.Contains(out, "todo item") {
		t.Errorf("task list text missing: %q", out)
	}
	if !strings.Contains(out, "☐") {
		t.Errorf("unchecked box missing: %q", out)
	}
}

func TestRenderMarkdown_TaskList_Checked(t *testing.T) {
	out := renderMarkdown("- [x] done item", 80)
	if !strings.Contains(out, "done item") {
		t.Errorf("task list text missing: %q", out)
	}
	if !strings.Contains(out, "☑") {
		t.Errorf("checked box missing: %q", out)
	}
}

func TestRenderMarkdown_Table(t *testing.T) {
	table := "| Name | Value |\n|------|-------|\n| foo  | bar   |"
	out := renderMarkdown(table, 80)
	if !strings.Contains(out, "Name") {
		t.Errorf("table header missing: %q", out)
	}
	if !strings.Contains(out, "foo") {
		t.Errorf("table row missing: %q", out)
	}
	if !strings.Contains(out, "bar") {
		t.Errorf("table cell missing: %q", out)
	}
}

func TestRenderMarkdown_Table_Separator(t *testing.T) {
	// Separator rows (|---|---| lines) should not appear verbatim.
	table := "| A | B |\n|---|---|\n| 1 | 2 |"
	out := renderMarkdown(table, 80)
	if strings.Contains(out, "---") {
		t.Errorf("separator row should be removed: %q", out)
	}
}

func TestRenderMarkdown_BulletList(t *testing.T) {
	out := renderMarkdown("- item one\n- item two", 80)
	if !strings.Contains(out, "item one") {
		t.Errorf("bullet item missing: %q", out)
	}
}

func TestRenderMarkdown_CodeBlock_Preserved(t *testing.T) {
	out := renderMarkdown("```go\nfmt.Println(\"hi\")\n```", 80)
	if !strings.Contains(out, "Println") {
		t.Errorf("code content missing: %q", out)
	}
}

func TestRenderMarkdown_Bold(t *testing.T) {
	out := renderMarkdown("**important**", 80)
	if !strings.Contains(out, "important") {
		t.Errorf("bold text missing: %q", out)
	}
}

func TestRenderMarkdown_InlineCode(t *testing.T) {
	out := renderMarkdown("run `make build`", 80)
	if !strings.Contains(out, "make build") {
		t.Errorf("inline code missing: %q", out)
	}
}

func TestRenderMarkdown_HorizontalRule(t *testing.T) {
	out := renderMarkdown("---", 80)
	// Should render as a separator line, not literal "---"
	if strings.TrimSpace(out) == "---" {
		t.Errorf("horizontal rule should be rendered, not literal: %q", out)
	}
}

func TestRenderMarkdown_OrderedList(t *testing.T) {
	out := renderMarkdown("1. first\n2. second", 80)
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("ordered list items missing: %q", out)
	}
}

func TestRenderMarkdown_Blockquote(t *testing.T) {
	out := renderMarkdown("> This is a quote", 80)
	if !strings.Contains(out, "This is a quote") {
		t.Errorf("blockquote text missing: %q", out)
	}
}

func TestRenderMessage_AssistantInfo(t *testing.T) {
	out := plainText(renderMessage(Message{
		Role:              RoleAssistantInfo,
		AssistantModel:    "Sonnet 4.6",
		AssistantDuration: 12 * time.Second,
		AssistantCost:     0.03,
	}, 80, false))

	for _, want := range []string{"Sonnet 4.6", "12s", "$0.03"} {
		if !strings.Contains(out, want) {
			t.Fatalf("assistant info missing %q: %q", want, out)
		}
	}
}

func TestRenderMessage_LocalOutputUsesLocalLabelAndFormatsCode(t *testing.T) {
	out := plainText(renderMessage(Message{
		Role:     RoleLocal,
		ToolName: "local-router",
		Content:  "package main\n\nfunc main() {}\n",
	}, 80, false))

	for _, want := range []string{"Local local-router", "package main", "func main"} {
		if !strings.Contains(out, want) {
			t.Fatalf("local render missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "Claude") {
		t.Fatalf("local render should not use Claude label: %q", out)
	}
}

func TestFormatLocalOutput_Diff(t *testing.T) {
	out := formatLocalOutput("--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n")
	if !strings.HasPrefix(out, "```diff\n") {
		t.Fatalf("local diff output should be fenced as diff: %q", out)
	}
}

func TestRenderMessage_ToolSummary(t *testing.T) {
	out := plainText(renderMessage(Message{
		Role:         RoleTool,
		ToolName:     "BashTool",
		ToolInput:    `{"command":"make verify"}`,
		Content:      "All checks passed.",
		ToolDuration: 2 * time.Second,
	}, 80, false))

	for _, want := range []string{"Bash", "ran", "2s", "make verify"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool render missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "All checks passed.") {
		t.Fatalf("completed successful tool render should hide result preview: %q", out)
	}
	if got := strings.Count(out, "\n"); got != 0 {
		t.Fatalf("completed successful tool row should stay one line, got %d newlines: %q", got, out)
	}
}

func TestRenderMessage_RunningToolSummaryStaysOneLine(t *testing.T) {
	longPrompt := "Write a complete, production-quality Go webserver that serves cached data from an S3 bucket. Address all of the identified reliability, security, cache invalidation, observability, and deployment issues without omitting edge cases."
	out := renderMessage(Message{
		Role:      RoleTool,
		ToolName:  "qwen_router__qwen_implement",
		ToolInput: `{"prompt":"` + longPrompt + `"}`,
		Content:   "running…",
	}, 72, false)

	plain := plainText(out)
	if !strings.Contains(plain, "Write a complete") {
		t.Fatalf("running summary lost prompt content: %q", plain)
	}
	if got := strings.Count(out, "\n"); got != 0 {
		t.Fatalf("running successful tool row should stay one line, got %d newlines: %q", got, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(line); got > 72 {
			t.Fatalf("line width = %d, want <= 72: %q", got, line)
		}
	}
}

func TestRenderMessage_ToolErrorShowsDetails(t *testing.T) {
	out := plainText(renderMessage(Message{
		Role:      RoleTool,
		ToolName:  "BashTool",
		ToolInput: `{"command":"make verify"}`,
		Content:   "exit status 1: lint failed",
		ToolError: true,
	}, 80, false))

	for _, want := range []string{"Bash", "failed", "exit status 1: lint failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool error render missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "make verify") {
		t.Fatalf("completed error tool render should hide prompt summary: %q", out)
	}
}

func TestHistoryToDisplayMessage_ToolUsePreservesInput(t *testing.T) {
	msg := historyToDisplayMessage(api.Message{
		Role: "assistant",
		Content: []api.ContentBlock{{
			Type:  "tool_use",
			ID:    "toolu_1",
			Name:  "BashTool",
			Input: map[string]any{"command": "git status --short"},
		}},
	})

	out := plainText(renderMessage(msg, 80, false))
	for _, want := range []string{"Bash", "used", "git status --short"} {
		if !strings.Contains(out, want) {
			t.Fatalf("resumed tool render missing %q: %q", want, out)
		}
	}
}

func TestHistoryToDisplayMessages_PairsToolResultWithToolUse(t *testing.T) {
	msgs := historyToDisplayMessages([]api.Message{
		{
			Role: "assistant",
			Content: []api.ContentBlock{{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  "Bash",
				Input: map[string]any{"command": "git status --short"},
			}},
		},
		{
			Role: "user",
			Content: []api.ContentBlock{{
				Type:          "tool_result",
				ToolUseID:     "toolu_1",
				ResultContent: " M internal/tui/render.go",
			}},
		},
	})

	if len(msgs) != 1 {
		t.Fatalf("historyToDisplayMessages len = %d, want 1: %#v", len(msgs), msgs)
	}
	out := plainText(renderMessage(msgs[0], 80, false))
	for _, want := range []string{"Bash", "ran", "git status --short"} {
		if !strings.Contains(out, want) {
			t.Fatalf("paired resumed tool render missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "M internal/tui/render.go") {
		t.Fatalf("paired successful tool result should stay hidden: %q", out)
	}
}

func TestHistoryToDisplayMessages_PreservesTextAroundToolUse(t *testing.T) {
	msgs := historyToDisplayMessages([]api.Message{
		{
			Role: "assistant",
			Content: []api.ContentBlock{
				{Type: "text", Text: "I'll check status."},
				{
					Type:  "tool_use",
					ID:    "toolu_1",
					Name:  "Bash",
					Input: map[string]any{"command": "git status --short"},
				},
			},
		},
		{
			Role: "user",
			Content: []api.ContentBlock{{
				Type:          "tool_result",
				ToolUseID:     "toolu_1",
				ResultContent: "",
			}},
		},
	})

	if len(msgs) != 2 {
		t.Fatalf("historyToDisplayMessages len = %d, want 2: %#v", len(msgs), msgs)
	}
	if msgs[0].Role != RoleAssistant || !strings.Contains(msgs[0].Content, "I'll check status.") {
		t.Fatalf("assistant text not preserved: %#v", msgs[0])
	}
	out := plainText(renderMessage(msgs[1], 80, false))
	if !strings.Contains(out, "git status --short") {
		t.Fatalf("tool row missing command: %q", out)
	}
}

func TestRenderMessage_ToolResultFallbackSummary(t *testing.T) {
	out := plainText(renderMessage(Message{
		Role:     RoleTool,
		ToolName: "Bash",
		Content:  "first line\nsecond line\nthird line",
	}, 80, false))

	for _, want := range []string{"Bash", "ran", "first line +2 lines"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool fallback summary missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "second line") {
		t.Fatalf("tool fallback summary should not expand successful output: %q", out)
	}
}
