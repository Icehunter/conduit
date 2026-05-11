package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/memdir"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/provider/codex"
	"github.com/icehunter/conduit/internal/provider/copilot"
	"github.com/icehunter/conduit/internal/secure"
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

func accountBackedActiveProvider(model, account string, tokens ...auth.PersistedTokens) settings.ActiveProviderSettings {
	kind := settings.ProviderKindClaudeSubscription
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
		return settings.ProviderKindAnthropicAPI
	default:
		return settings.ProviderKindClaudeSubscription
	}
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

// rebuildSystemCmd returns a Cmd that asynchronously rebuilds the loop's
// system blocks to reflect the current permission mode. Council mode appends
// a directive telling the model to always route through ExitPlanMode.
func (m Model) rebuildSystemCmd() tea.Cmd {
	if m.cfg.Loop == nil {
		return nil
	}
	loop := m.cfg.Loop
	claudeMd := m.cfg.ClaudeMd
	skills := m.cfg.Skills
	mode := m.permissionMode
	outputStyle := m.outputStylePrompt
	outputStyleName := m.outputStyleName

	return func() tea.Msg {
		cwd, _ := os.Getwd()
		mem := memdir.BuildPrompt(cwd)
		base := agent.BuildSystemBlocks(mem, claudeMd, skills...)

		if outputStyle != "" {
			base = append(base, api.SystemBlock{
				Type: "text",
				Text: "# Output style: " + outputStyleName + "\n\n" + outputStyle,
			})
		}
		if mode == permissions.ModeCouncil {
			base = append(base, api.SystemBlock{
				Type: "text",
				Text: agent.CouncilModeDirective,
			})
		}
		loop.SetSystem(base)
		return nil
	}
}

func (m *Model) applyEffectiveProviderForMode() {
	provider, ok := m.providerForCurrentMode()
	if !ok || m.cfg.Loop == nil {
		return
	}
	switch provider.Kind {
	case settings.ProviderKindMCP:
		m.cfg.Loop.SetContextWindow(provider.ContextWindow)
		// Switch to local/MCP routing for this role's server and tools.
		// No API client swap needed — turns route through MCP tool calls.
		server := provider.Server
		if server == "" {
			server = "local-router"
		}
		directTool := provider.DirectTool
		if directTool == "" {
			directTool = "local_direct"
		}
		implementTool := provider.ImplementTool
		if implementTool == "" {
			implementTool = "local_implement"
		}
		m.localMode = true
		m.localModeServer = server
		m.localDirectTool = directTool
		m.localImplementTool = implementTool
	case settings.ProviderKindClaudeSubscription, settings.ProviderKindAnthropicAPI:
		if provider.Model == "" {
			return
		}
		// If the configured account differs from the active session account,
		// build a fresh client using that account's stored tokens rather than
		// reusing the active session client (which would bill the wrong account).
		if provider.Account != "" && provider.Account != auth.ActiveEmail() && m.cfg.NewAPIClient != nil {
			if tokens, err := auth.LoadForEmail(secure.NewDefault(), provider.Account); err == nil {
				m.cfg.Loop.SetClient(m.cfg.NewAPIClient(tokens))
			} else if m.cfg.APIClient != nil {
				m.cfg.Loop.SetClient(m.cfg.APIClient)
			}
		} else if m.cfg.APIClient != nil {
			m.cfg.Loop.SetClient(m.cfg.APIClient)
		}
		m.cfg.Loop.SetModel(provider.Model)
		m.cfg.Loop.SetContextWindow(provider.ContextWindow)
		// Leaving MCP/local mode — restore direct API routing.
		if m.localMode {
			m.localMode = false
			m.localModeServer = ""
		}
	case settings.ProviderKindOpenAICompatible:
		if provider.Model == "" {
			return
		}
		if strings.HasPrefix(provider.Credential, copilot.ProviderID) || strings.Contains(strings.ToLower(provider.BaseURL), "api.githubcopilot.com") {
			provider.ContextWindow = copilot.ContextWindowForModel(provider.Model, provider.ContextWindow)
		} else if strings.HasPrefix(provider.Credential, codex.ProviderID) || strings.Contains(strings.ToLower(provider.BaseURL), "chatgpt.com/backend-api/codex") {
			provider.ContextWindow = codex.ContextWindowForModel(provider.Model, provider.ContextWindow)
		}
		if m.cfg.NewProviderAPIClient == nil {
			return
		}
		client, err := m.cfg.NewProviderAPIClient(provider)
		if err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: "Provider client unavailable: " + err.Error()})
			return
		}
		m.cfg.Loop.SetClient(client)
		m.cfg.Loop.SetModel(provider.Model)
		m.cfg.Loop.SetContextWindow(provider.ContextWindow)
		if m.localMode {
			m.localMode = false
			m.localModeServer = ""
		}
	}
}

func (m Model) currentProviderRole() string {
	switch m.permissionMode {
	case permissions.ModePlan:
		return settings.RolePlanning
	case permissions.ModeCouncil:
		return settings.RolePlanning
	case permissions.ModeAcceptEdits, permissions.ModeBypassPermissions:
		// Use the dedicated implement role if configured; fall back to main.
		if _, ok := m.providerForRole(settings.RoleImplement); ok {
			return settings.RoleImplement
		}
		return settings.RoleMain
	default:
		return settings.RoleDefault
	}
}

func (m Model) compactClientAndModel() (*api.Client, string, error) {
	provider, ok := m.providerForCurrentMode()
	if !ok {
		return m.cfg.APIClient, compact.DefaultModel, nil
	}
	if provider.Model == "" {
		provider.Model = m.effectiveAssistantModelName()
	}
	switch provider.Kind {
	case settings.ProviderKindOpenAICompatible:
		if m.cfg.NewProviderAPIClient == nil {
			return nil, "", fmt.Errorf("provider client is not configured")
		}
		client, err := m.cfg.NewProviderAPIClient(provider)
		if err != nil {
			return nil, "", err
		}
		return client, provider.Model, nil
	case settings.ProviderKindClaudeSubscription, settings.ProviderKindAnthropicAPI:
		if provider.Account != "" && provider.Account != auth.ActiveEmail() && m.cfg.NewAPIClient != nil {
			tokens, err := auth.LoadForEmail(secure.NewDefault(), provider.Account)
			if err != nil {
				return nil, "", err
			}
			return m.cfg.NewAPIClient(tokens), provider.Model, nil
		}
		return m.cfg.APIClient, provider.Model, nil
	default:
		return m.cfg.APIClient, m.effectiveAssistantModelName(), nil
	}
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
	return settings.ActiveProviderSettings{}, false
}

func (m Model) providerAvailable(provider settings.ActiveProviderSettings) bool {
	switch provider.Kind {
	case settings.ProviderKindClaudeSubscription, settings.ProviderKindAnthropicAPI:
		return m.accountProviderAvailable(provider)
	case settings.ProviderKindOpenAICompatible:
		return settings.ValidateProviderSettings(provider) == nil
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
	case settings.ProviderKindAnthropicAPI:
		return accountKind == auth.AccountKindAnthropicConsole
	case settings.ProviderKindClaudeSubscription:
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
		return m.localModelPickerItemAvailable(strings.TrimSpace(strings.TrimPrefix(item.Value, "local:")))
	case strings.HasPrefix(item.Value, "provider:"):
		return true
	case strings.HasPrefix(item.Value, "anthropic-api:"):
		return m.anyAccountForProviderKind(settings.ProviderKindAnthropicAPI)
	case strings.HasPrefix(item.Value, "claude-subscription:"):
		return m.anyAccountForProviderKind(settings.ProviderKindClaudeSubscription)
	default:
		return true
	}
}

func (m Model) localModelPickerItemAvailable(server string) bool {
	if server == "" {
		server = "local-router"
	}
	if m.cfg.MCPManager != nil {
		for _, srv := range m.cfg.MCPManager.Servers() {
			if srv == nil || srv.Name != server {
				continue
			}
			if srv.Disabled {
				return false
			}
			break
		}
	}
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return true
	}
	return !mcp.IsDisabled(server, cwd)
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

func (m Model) activeMCPProvider() (settings.ActiveProviderSettings, bool) {
	if provider, ok := m.providerForCurrentMode(); ok && provider.Kind == settings.ProviderKindMCP {
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
		if provider.ContextWindow <= 0 {
			provider.ContextWindow = m.localContextWindow(provider.Server)
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
			Kind:          settings.ProviderKindMCP,
			Server:        server,
			Model:         m.localModelName(server),
			ContextWindow: m.localContextWindow(server),
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
	return ""
}

func providerPickerValue(provider settings.ActiveProviderSettings) string {
	if provider.Kind == settings.ProviderKindMCP {
		server := provider.Server
		if server == "" {
			server = "local-router"
		}
		return "local:" + server
	}
	if provider.Kind == settings.ProviderKindAnthropicAPI {
		if provider.Account != "" {
			return "provider:" + settings.ProviderKey(provider)
		}
		return "anthropic-api:" + provider.Model
	}
	if provider.Kind == settings.ProviderKindClaudeSubscription {
		if provider.Account != "" {
			return "provider:" + settings.ProviderKey(provider)
		}
		return "claude-subscription:" + provider.Model
	}
	if provider.Kind == settings.ProviderKindOpenAICompatible {
		provider.Account = ""
		return "provider:" + settings.ProviderKey(provider)
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
		if provider, ok := m.providerForCurrentMode(); ok && provider.Kind != settings.ProviderKindMCP && provider.Model != "" {
			return accountProviderDisplayName(provider)
		}
		if m.noAuth {
			return ""
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
	switch provider.Kind {
	case settings.ProviderKindAnthropicAPI:
		label = "Anthropic API"
	case settings.ProviderKindOpenAICompatible:
		label = "OpenAI-compatible"
		if strings.HasPrefix(provider.Credential, "github-copilot") || strings.Contains(strings.ToLower(provider.BaseURL), "api.githubcopilot.com") {
			label = "GitHub Copilot"
		} else if strings.HasPrefix(provider.Credential, codex.ProviderID) || strings.Contains(strings.ToLower(provider.BaseURL), "chatgpt.com/backend-api/codex") {
			label = "ChatGPT / Codex"
		}
		provider.Account = ""
	}
	parts := []string{label}
	if provider.Model != "" {
		parts = append(parts, provider.Model)
	}
	if provider.Account != "" {
		parts = append(parts, provider.Account)
	} else if provider.Credential != "" {
		switch provider.Kind {
		case settings.ProviderKindAnthropicAPI, settings.ProviderKindOpenAICompatible:
			parts = append(parts, "credential "+provider.Credential)
		default:
			parts = append(parts, provider.Credential)
		}
	}
	return strings.Join(parts, " · ")
}

func (m Model) effectiveAssistantModelName() string {
	if provider, ok := m.providerForCurrentMode(); ok && provider.Kind != settings.ProviderKindMCP && provider.Model != "" {
		return provider.Model
	}
	return m.modelName
}

func (m Model) effectiveContextWindow() int {
	if provider, ok := m.providerForCurrentMode(); ok {
		if provider.ContextWindow > 0 {
			return provider.ContextWindow
		}
		if provider.Kind == settings.ProviderKindMCP {
			if w := m.localContextWindow(provider.Server); w > 0 {
				return w
			}
		}
	}
	return internalmodel.ContextWindowFor(m.effectiveAssistantModelName())
}

func (m Model) assistantDisplayLabel() string {
	if provider, ok := m.providerForCurrentMode(); ok {
		switch provider.Kind {
		case settings.ProviderKindOpenAICompatible:
			return "‹ " + openAICompatibleAssistantName(provider.Model)
		case settings.ProviderKindAnthropicAPI, settings.ProviderKindClaudeSubscription:
			return prefixClaude
		}
	}
	return prefixClaude
}

func (m Model) assistantMessage(content string) Message {
	label := m.turnAssistant
	if label == "" {
		label = m.assistantDisplayLabel()
	}
	return Message{Role: RoleAssistant, Content: content, AssistantLabel: label}
}

func (m *Model) captureTurnProvider() {
	provider, ok := m.providerForCurrentMode()
	if !ok {
		m.turnAssistant = prefixClaude
		m.turnProviderKind = ""
		m.turnProvider = ""
		return
	}
	m.turnAssistant = m.assistantDisplayLabel()
	m.turnProviderKind = provider.Kind
	m.turnProvider = provider.Model
}

func (m Model) annotateTurnProvider(history []api.Message) []api.Message {
	if m.turnProviderKind == "" || m.turnProvider == "" {
		return history
	}
	out := make([]api.Message, len(history))
	copy(out, history)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == "assistant" && out[i].ProviderKind == "" && out[i].Provider == "" {
			out[i].ProviderKind = m.turnProviderKind
			out[i].Provider = m.turnProvider
			continue
		}
		if out[i].Role == "user" {
			break
		}
	}
	return out
}

func openAICompatibleAssistantName(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(model, "gemini"):
		return "Gemini"
	case strings.Contains(model, "gpt") || strings.Contains(model, "o1") || strings.Contains(model, "o3") || strings.Contains(model, "o4"):
		return "OpenAI"
	default:
		return "OpenAI"
	}
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

func (m Model) localContextWindow(server string) int {
	if m.cfg.MCPManager != nil {
		for _, srv := range m.cfg.MCPManager.Servers() {
			if srv == nil || srv.Name != server {
				continue
			}
			if raw := strings.TrimSpace(srv.Config.Env["LOCAL_LLM_CONTEXT_WINDOW"]); raw != "" {
				if n, ok := parseProviderContextWindow(raw); ok {
					return n
				}
			}
			break
		}
	}
	return 0
}

func parseProviderContextWindow(value string) (int, bool) {
	s := strings.ToLower(strings.TrimSpace(value))
	if s == "" {
		return 0, false
	}
	switch {
	case strings.HasSuffix(s, "tokens"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "tokens"))
	case strings.HasSuffix(s, "token"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "token"))
	}
	multiplier := 1
	switch {
	case strings.HasSuffix(s, "k"):
		multiplier = 1_000
		s = strings.TrimSpace(strings.TrimSuffix(s, "k"))
	case strings.HasSuffix(s, "m"):
		multiplier = 1_000_000
		s = strings.TrimSpace(strings.TrimSuffix(s, "m"))
	}
	n, err := strconv.Atoi(strings.ReplaceAll(s, "_", ""))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * multiplier, true
}
