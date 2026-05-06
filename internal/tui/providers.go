package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

func runLocalCall(ctx context.Context, manager *mcp.Manager, call commands.LocalCall, input []byte, turnID int, chat bool) tea.Msg {
	result, err := manager.CallTool(ctx, call.Tool, input)
	if err != nil {
		return localCallDoneMsg{turnID: turnID, call: call, chat: chat, err: err}
	}
	var parts []string
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if result.IsError {
		if text == "" {
			text = "local tool returned an error"
		}
		return localCallDoneMsg{turnID: turnID, call: call, chat: chat, err: errors.New(text)}
	}
	return localCallDoneMsg{turnID: turnID, call: call, chat: chat, text: text}
}

func persistClaudeActiveProvider(model, account string) string {
	return persistActiveProvider(accountBackedActiveProvider(model, account))
}

func accountBackedActiveProvider(model, account string, tokens ...auth.PersistedTokens) settings.ActiveProviderSettings {
	kind := "claude-subscription"
	if len(tokens) > 0 {
		kind = providerKindForAccountKind(auth.InferAccountKind(tokens[0]))
	} else if account != "" {
		if store, err := auth.ListAccounts(); err == nil {
			if entry, ok := store.Accounts[account]; ok && entry.Kind != "" {
				kind = providerKindForAccountKind(entry.Kind)
			} else {
				for id, entry := range store.Accounts {
					if entry.Email == account && id == store.Active && entry.Kind != "" {
						kind = providerKindForAccountKind(entry.Kind)
						break
					}
				}
			}
		}
	}
	return settings.ActiveProviderSettings{
		Kind:    kind,
		Model:   model,
		Account: account,
	}
}

func providerKindForAccountKind(kind string) string {
	switch kind {
	case auth.AccountKindAnthropicConsole:
		return "anthropic-api"
	default:
		return "claude-subscription"
	}
}

func persistActiveProvider(value settings.ActiveProviderSettings) string {
	if err := settings.SaveActiveProvider(value); err != nil {
		return fmt.Sprintf(" (failed to persist active provider: %v)", err)
	}
	return ""
}

func (m *Model) persistProviderForRole(role string, value settings.ActiveProviderSettings) string {
	if role == "" {
		role = settings.RoleDefault
	}
	key := settings.ProviderKey(value)
	if m.providers == nil {
		m.providers = map[string]settings.ActiveProviderSettings{}
	}
	if m.roles == nil {
		m.roles = map[string]string{}
	}
	m.providers[key] = value
	m.roles[role] = key
	if err := settings.SaveRoleProvider(role, value); err != nil {
		return fmt.Sprintf(" (failed to persist %s provider: %v)", role, err)
	}
	return ""
}

func (m *Model) setActiveProvider(value settings.ActiveProviderSettings) {
	provider := value
	m.activeProvider = &provider
	switch provider.Kind {
	case "mcp":
		m.localMode = true
		m.localModeServer = provider.Server
		m.localDirectTool = provider.DirectTool
		m.localImplementTool = provider.ImplementTool
		m.ensureDefaultLocalTools()
	default:
		m.localMode = false
		m.localModeServer = ""
	}
}

func (m *Model) applyPermissionMode(mode permissions.Mode) {
	m.permissionMode = mode
	if m.cfg.Gate != nil {
		m.cfg.Gate.SetMode(mode)
	}
	_ = settings.SaveConduitPermissionsField("defaultMode", string(mode))
	m.applyEffectiveProviderForMode()
	m.syncLive()
	m.refreshWelcomeCardMessage()
}

func (m *Model) applyEffectiveProviderForMode() {
	provider, ok := m.providerForCurrentMode()
	if !ok || provider.Kind == "mcp" || provider.Model == "" || m.cfg.Loop == nil {
		return
	}
	m.cfg.Loop.SetModel(provider.Model)
}

func (m Model) currentProviderRole() string {
	switch m.permissionMode {
	case permissions.ModePlan:
		return settings.RolePlanning
	case permissions.ModeAcceptEdits, permissions.ModeBypassPermissions:
		return settings.RoleMain
	default:
		return settings.RoleDefault
	}
}

func (m Model) backgroundModel() string {
	if m.cfg.BackgroundModel != nil {
		if model := strings.TrimSpace(m.cfg.BackgroundModel()); model != "" {
			return model
		}
	}
	if provider, ok := m.providerForRole(settings.RoleBackground); ok && provider.Kind != "mcp" && provider.Model != "" {
		return provider.Model
	}
	return compact.DefaultModel
}

func (m Model) providerForCurrentMode() (settings.ActiveProviderSettings, bool) {
	return m.providerForRole(m.currentProviderRole())
}

func (m Model) providerForRole(role string) (settings.ActiveProviderSettings, bool) {
	if role == "" {
		role = settings.RoleDefault
	}
	if ref := m.roles[role]; ref != "" {
		if provider, ok := m.providers[ref]; ok {
			if !m.providerAvailable(provider) {
				return settings.ActiveProviderSettings{}, false
			}
			return provider, true
		}
	}
	if role == settings.RoleDefault && m.activeProvider != nil {
		if !m.providerAvailable(*m.activeProvider) {
			return settings.ActiveProviderSettings{}, false
		}
		return *m.activeProvider, true
	}
	return settings.ActiveProviderSettings{}, false
}

func (m Model) providerAvailable(provider settings.ActiveProviderSettings) bool {
	switch provider.Kind {
	case "claude-subscription", "anthropic-api":
		return m.accountProviderAvailable(provider)
	default:
		return true
	}
}

func (m Model) accountProviderAvailable(provider settings.ActiveProviderSettings) bool {
	if provider.Account == "" {
		return false
	}
	store, err := auth.LoadAccountStore()
	if err != nil || len(store.Accounts) == 0 {
		return false
	}
	if entry, ok := store.Accounts[provider.Account]; ok {
		return tuiProviderKindMatchesAccount(provider.Kind, entry.Kind)
	}
	for _, entry := range store.Accounts {
		if entry.Email == provider.Account && tuiProviderKindMatchesAccount(provider.Kind, entry.Kind) {
			return true
		}
	}
	return false
}

func tuiProviderKindMatchesAccount(providerKind, accountKind string) bool {
	switch providerKind {
	case "anthropic-api":
		return accountKind == auth.AccountKindAnthropicConsole
	case "claude-subscription":
		return accountKind == "" || accountKind == auth.AccountKindClaudeAI
	default:
		return false
	}
}

func (m Model) filterModelPickerItems(items []pickerItem) []pickerItem {
	out := make([]pickerItem, 0, len(items))
	for i := 0; i < len(items); i++ {
		item := items[i]
		if !item.Section {
			if m.modelPickerItemAvailable(item) {
				out = append(out, item)
			}
			continue
		}
		start := len(out)
		out = append(out, item)
		for i+1 < len(items) && !items[i+1].Section {
			i++
			if m.modelPickerItemAvailable(items[i]) {
				out = append(out, items[i])
			}
		}
		if len(out) == start+1 {
			out = out[:start]
		}
	}
	return out
}

func (m Model) modelPickerItemAvailable(item pickerItem) bool {
	switch {
	case strings.HasPrefix(item.Value, "local:"):
		return true
	case strings.HasPrefix(item.Value, "provider:"):
		return true
	case strings.HasPrefix(item.Value, "anthropic-api:"):
		return m.anyAccountForProviderKind("anthropic-api")
	case strings.HasPrefix(item.Value, "claude-subscription:"):
		return m.anyAccountForProviderKind("claude-subscription")
	default:
		return true
	}
}

func (m Model) anyAccountForProviderKind(kind string) bool {
	store, err := auth.LoadAccountStore()
	if err != nil {
		return false
	}
	for _, entry := range store.Accounts {
		if tuiProviderKindMatchesAccount(kind, entry.Kind) {
			return true
		}
	}
	return false
}

func (m Model) mcpActiveProvider(server string) settings.ActiveProviderSettings {
	directTool := m.localDirectTool
	if directTool == "" {
		directTool = "local_direct"
	}
	implementTool := m.localImplementTool
	if implementTool == "" {
		implementTool = "local_implement"
	}
	return settings.ActiveProviderSettings{
		Kind:          "mcp",
		Server:        server,
		Model:         m.localModelName(server),
		DirectTool:    directTool,
		ImplementTool: implementTool,
	}
}

func (m Model) activeMCPProvider() (settings.ActiveProviderSettings, bool) {
	if provider, ok := m.providerForCurrentMode(); ok && provider.Kind == "mcp" {
		if provider.Server == "" {
			provider.Server = "local-router"
		}
		if provider.DirectTool == "" {
			provider.DirectTool = "local_direct"
		}
		if provider.ImplementTool == "" {
			provider.ImplementTool = "local_implement"
		}
		if provider.Model == "" {
			provider.Model = m.localModelName(provider.Server)
		}
		return provider, true
	}
	if m.currentProviderRole() == settings.RoleDefault && m.localMode {
		server := m.localModeServer
		if server == "" {
			server = "local-router"
		}
		directTool := m.localDirectTool
		if directTool == "" {
			directTool = "local_direct"
		}
		implementTool := m.localImplementTool
		if implementTool == "" {
			implementTool = "local_implement"
		}
		return settings.ActiveProviderSettings{
			Kind:          "mcp",
			Server:        server,
			Model:         m.localModelName(server),
			DirectTool:    directTool,
			ImplementTool: implementTool,
		}, true
	}
	return settings.ActiveProviderSettings{}, false
}

func (m Model) providerValueForRole(role string) string {
	if role == "" {
		role = settings.RoleDefault
	}
	if ref := m.roles[role]; ref != "" {
		if provider, ok := m.providers[ref]; ok {
			return providerPickerValue(provider)
		}
	}
	if role == settings.RoleDefault && m.activeProvider != nil {
		return providerPickerValue(*m.activeProvider)
	}
	return ""
}

func providerPickerValue(provider settings.ActiveProviderSettings) string {
	if provider.Kind == "mcp" {
		server := provider.Server
		if server == "" {
			server = "local-router"
		}
		return "local:" + server
	}
	if provider.Kind == "anthropic-api" {
		if provider.Account != "" {
			return "provider:" + settings.ProviderKey(provider)
		}
		return "anthropic-api:" + provider.Model
	}
	if provider.Kind == "claude-subscription" {
		if provider.Account != "" {
			return "provider:" + settings.ProviderKey(provider)
		}
		return "claude-subscription:" + provider.Model
	}
	return provider.Model
}

func (m *Model) ensureDefaultLocalTools() {
	if m.localDirectTool == "" {
		m.localDirectTool = "local_direct"
	}
	if m.localImplementTool == "" {
		m.localImplementTool = "local_implement"
	}
}

func (m Model) activeModelDisplayName() string {
	provider, ok := m.activeMCPProvider()
	if !ok {
		if provider, ok := m.providerForCurrentMode(); ok && provider.Kind != "mcp" && provider.Model != "" {
			return accountProviderDisplayName(provider)
		}
		return m.modelName
	}
	return mcpProviderDisplayName(provider, m.localModelName(provider.Server))
}

func mcpProviderDisplayName(provider settings.ActiveProviderSettings, fallbackModel string) string {
	server := provider.Server
	if server == "" {
		server = "local-router"
	}
	model := provider.Model
	if model == "" {
		model = fallbackModel
	}
	if model == "" || model == server {
		return "MCP · " + server
	}
	return fmt.Sprintf("MCP · %s · %s", model, server)
}

func accountProviderDisplayName(provider settings.ActiveProviderSettings) string {
	label := "Claude Subscription"
	if provider.Kind == "anthropic-api" {
		label = "Anthropic API"
	}
	parts := []string{label}
	if provider.Model != "" {
		parts = append(parts, provider.Model)
	}
	if provider.Account != "" {
		parts = append(parts, provider.Account)
	}
	return strings.Join(parts, " · ")
}

func (m Model) effectiveAssistantModelName() string {
	if provider, ok := m.providerForCurrentMode(); ok && provider.Kind != "mcp" && provider.Model != "" {
		return provider.Model
	}
	return m.modelName
}

func (m Model) localModelName(server string) string {
	if m.cfg.MCPManager != nil {
		for _, srv := range m.cfg.MCPManager.Servers() {
			if srv == nil || srv.Name != server {
				continue
			}
			if model := srv.Config.Env["LOCAL_LLM_MODEL"]; model != "" {
				return model
			}
			break
		}
	}
	if server == "local-router" {
		return "qwen3-coder"
	}
	return server
}
