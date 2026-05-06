package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/coordinator"
	"github.com/icehunter/conduit/internal/memdir"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/planusage"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/ratelimit"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/theme"
)

// applyModelSwitch handles the "model" command result.
func (m Model) applyModelSwitch(res commands.Result) (Model, tea.Cmd) {
	m.modelName = res.Model
	provider := accountBackedActiveProvider(res.Model, m.cfg.Profile.Email)
	m.setActiveProvider(provider)
	persistSuffix := persistActiveProvider(provider)
	m.cfg.Loop.SetModel(res.Model)
	m.syncLive()
	m.refreshWelcomeCardMessage()
	if res.Text != "" {
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: res.Text + persistSuffix})
	}
	m.refreshViewport()
	return m.startPlanUsageFetch()
}

// applyProviderSwitch handles the "provider-switch" command result.
func (m Model) applyProviderSwitch(res commands.Result) (Model, tea.Cmd) {
	if res.Provider == nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "Provider switch payload missing."})
		m.refreshViewport()
		return m, nil
	}
	role := res.Role
	if role == "" {
		role = settings.RoleDefault
	}
	switch res.Provider.Kind {
	case "mcp":
		server := res.Provider.Server
		if server == "" {
			server = "local-router"
		}
		model := res.Provider.Model
		if model == "" {
			model = m.localModelName(server)
		}
		provider := *res.Provider
		provider.Server = server
		provider.Model = model
		if role == settings.RoleDefault {
			m.setActiveProvider(provider)
		}
		suffix := m.persistProviderForRole(role, provider)
		m.applyEffectiveProviderForMode()
		m.syncLive()
		m.refreshWelcomeCardMessage()
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Set %s provider to local %s · %s%s", role, model, server, suffix)})
	default:
		model := res.Provider.Model
		if model == "" {
			model = res.Model
		}
		if model == "" {
			model = m.modelName
		}
		provider := *res.Provider
		provider.Model = model
		if provider.Account == "" {
			provider.Account = m.cfg.Profile.Email
		}
		if provider.Kind == "claude-subscription" {
			provider.Kind = accountBackedActiveProvider(model, provider.Account).Kind
		}
		if role == settings.RoleDefault {
			m.modelName = model
			m.setActiveProvider(provider)
			m.cfg.Loop.SetModel(model)
		}
		suffix := m.persistProviderForRole(role, provider)
		m.applyEffectiveProviderForMode()
		m.syncLive()
		m.refreshWelcomeCardMessage()
		msg := ""
		if role == settings.RoleDefault {
			msg = res.Text
		}
		if msg == "" {
			msg = fmt.Sprintf("Set %s provider to %s", role, model)
		}
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: msg + suffix})
	}
	m.refreshViewport()
	return m.startPlanUsageFetch()
}

// applyCompactResult handles the "compact" command result.
func (m Model) applyCompactResult(res commands.Result) (Model, tea.Cmd) {
	if m.cfg.APIClient == nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "Compact unavailable: no API client."})
		m.refreshViewport()
		return m, nil
	}
	if len(m.history) == 0 {
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Nothing to compact."})
		m.refreshViewport()
		return m, nil
	}
	m.running = true
	m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Compacting conversation…"})
	m.refreshViewport()
	customInstructions := res.Text
	client := m.cfg.APIClient
	backgroundModel := m.backgroundModel()
	histCopy := make([]api.Message, len(m.history))
	copy(histCopy, m.history)
	return m, func() tea.Msg {
		result, err := compact.CompactWithModel(context.Background(), client, backgroundModel, histCopy, customInstructions)
		if err != nil {
			return compactDoneMsg{err: err}
		}
		return compactDoneMsg{newHistory: result.NewHistory, summary: result.Summary}
	}
}

// applyLocalCall handles the "local-call" command result.
func (m Model) applyLocalCall(res commands.Result) (Model, tea.Cmd) {
	if m.running {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "Local call unavailable while another turn is running."})
		m.refreshViewport()
		return m, nil
	}
	if m.cfg.MCPManager == nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "Local call unavailable: MCP manager is not configured."})
		m.refreshViewport()
		return m, nil
	}
	var call commands.LocalCall
	if err := json.Unmarshal([]byte(res.Text), &call); err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "Local call payload invalid: " + err.Error()})
		m.refreshViewport()
		return m, nil
	}
	input, err := json.Marshal(call.Arguments)
	if err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "Local call input invalid: " + err.Error()})
		m.refreshViewport()
		return m, nil
	}
	m.dismissWelcome()
	m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Calling local provider %s…", call.Server)})
	m.running = true
	m.cancelled = false
	m.streaming = ""
	m.apiRetryStatus = ""
	m.turnStarted = time.Now()
	m.refreshViewport()
	m.vp.GotoBottom()
	m.turnID++
	turnID := m.turnID
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	manager := m.cfg.MCPManager
	return m, func() tea.Msg {
		return runLocalCall(ctx, manager, call, input, turnID, false)
	}
}

// applyLocalMode handles the "local-mode" command result.
func (m Model) applyLocalMode(res commands.Result) (Model, tea.Cmd) {
	parts := strings.SplitN(res.Text, "\t", 2)
	action := ""
	server := ""
	if len(parts) > 0 {
		action = parts[0]
	}
	if len(parts) > 1 {
		server = parts[1]
	}
	switch action {
	case "off":
		provider := accountBackedActiveProvider(m.modelName, m.cfg.Profile.Email)
		m.setActiveProvider(provider)
		suffix := persistActiveProvider(provider)
		m.syncLive()
		m.refreshWelcomeCardMessage()
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Local mode off. Normal chat routes to Claude." + suffix})
	case "on":
		if server == "" {
			server = "local-router"
		}
		m.ensureDefaultLocalTools()
		provider := m.mcpActiveProvider(server)
		m.setActiveProvider(provider)
		suffix := persistActiveProvider(provider)
		m.syncLive()
		m.refreshWelcomeCardMessage()
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Local mode on. Normal chat routes to %s. Use /local-mode off to return to Claude.%s", server, suffix)})
	default:
		if _, ok := m.activeMCPProvider(); ok {
			provider := accountBackedActiveProvider(m.modelName, m.cfg.Profile.Email)
			m.setActiveProvider(provider)
			suffix := persistActiveProvider(provider)
			m.syncLive()
			m.refreshWelcomeCardMessage()
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Local mode off. Normal chat routes to Claude." + suffix})
		} else {
			if server == "" {
				server = "local-router"
			}
			m.ensureDefaultLocalTools()
			provider := m.mcpActiveProvider(server)
			m.setActiveProvider(provider)
			suffix := persistActiveProvider(provider)
			m.syncLive()
			m.refreshWelcomeCardMessage()
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Local mode on. Normal chat routes to %s. Use /local-mode off to return to Claude.%s", server, suffix)})
		}
	}
	m.refreshViewport()
	return m, nil
}

// applyPromptResult handles the "prompt" command result.
func (m Model) applyPromptResult(res commands.Result) (Model, tea.Cmd) {
	// Inject text as a user turn and kick off an agent run — same as
	// typing the prompt in the input box, but sourced from a slash command.
	if res.Text == "" || m.running || m.noAuth {
		return m, nil
	}
	m.dismissWelcome()
	m.messages = append(m.messages, Message{Role: RoleUser, Content: res.Text})
	m.history = append(m.history, api.Message{
		Role:    "user",
		Content: []api.ContentBlock{{Type: "text", Text: res.Text}},
	})
	m.running = true
	m.cancelled = false
	m.streaming = ""
	m.apiRetryStatus = ""
	m.turnStarted = time.Now()
	m.refreshViewport()
	m.vp.GotoBottom()
	m.turnID++
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	prog := *m.cfg.Program
	histCopy := make([]api.Message, len(m.history))
	copy(histCopy, m.history)
	turnID := m.turnID
	return m, func() tea.Msg {
		newHist, err := m.cfg.Loop.Run(ctx, histCopy, func(ev agent.LoopEvent) {
			prog.Send(agentMsg{event: ev})
		})
		return agentDoneMsg{turnID: turnID, history: newHist, err: err, cancelled: ctx.Err() != nil}
	}
}

// applyCoordinatorToggle handles the "coordinator-toggle" command result.
func (m Model) applyCoordinatorToggle(res commands.Result) (Model, tea.Cmd) {
	// Persist the new mode to the session JSONL so /resume can restore it.
	if m.cfg.Session != nil {
		mode := "normal"
		if coordinator.IsActive() {
			mode = "coordinator"
		}
		_ = m.cfg.Session.SetMode(mode)
	}
	if res.Text != "" {
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: res.Text})
	}
	m.refreshViewport()
	return m, nil
}

// applyUsageToggle handles the "usage-toggle" command result.
func (m Model) applyUsageToggle(res commands.Result) (Model, tea.Cmd) {
	on := strings.TrimSpace(strings.ToLower(res.Text)) == "on"
	m.usageStatusEnabled = on
	m.cfg.UsageStatusEnabled = on
	m = m.applyLayout()
	if on {
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Plan usage footer enabled."})
		m.planUsageErr = ""
		m.planUsageBackoff = time.Time{} // user explicitly re-enabled; clear any backoff
		m.planUsageFetching = true
		m.refreshViewport()
		if m.cfg.FetchPlanUsage != nil {
			return m.startPlanUsageFetch()
		}
		m.planUsageFetching = false
		m.planUsageErr = "plan usage fetcher unavailable"
		return m, nil
	}
	m.planUsageFetching = false
	m.planUsageErr = ""
	m.planUsage = planusage.Info{}
	m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Plan usage footer disabled."})
	m = m.applyLayout()
	m.refreshViewport()
	return m, nil
}

// applyAccountSwitch handles the "account-switch" command result.
func (m Model) applyAccountSwitch(res commands.Result) (Model, tea.Cmd) {
	account := res.Text
	store, err := auth.ListAccounts()
	if err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "account-switch: " + err.Error()})
		m.refreshViewport()
		return m, nil
	}
	if account == "" {
		// No email given — list accounts.
		var sb strings.Builder
		sb.WriteString("Logged-in accounts:\n\n")
		for id, entry := range store.Accounts {
			active := ""
			if id == store.Active {
				active = "  ← active"
			}
			fmt.Fprintf(&sb, "  %s  (id %s, added %s)%s\n", accountDisplayLabel(id, entry.Email, entry.Kind), id, entry.AddedAt.Format("2006-01-02"), active)
		}
		if len(store.Accounts) == 0 {
			sb.WriteString("  (none — run /login to add an account)\n")
		}
		sb.WriteString("\nUse /login --switch <account-id> to activate an account.")
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: sb.String()})
		m.refreshViewport()
		return m, nil
	}
	if err := auth.SetActive(&store, account); err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: err.Error()})
		m.refreshViewport()
		return m, nil
	}
	// Reload credentials and API client live — same flow as after /login.
	if m.cfg.LoadAuth != nil && m.cfg.NewAPIClient != nil && m.cfg.Loop != nil {
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Switching to %s…", account)})
		m.refreshViewport()
		return m, func() tea.Msg {
			ctx := context.Background()
			tok, prof, err := m.cfg.LoadAuth(ctx)
			if err != nil {
				// ErrNotLoggedIn means we switched the active pointer but have
				// no token for this account — guide the user to /login.
				if errors.Is(err, auth.ErrNotLoggedIn) {
					return authReloadMsg{err: fmt.Errorf("no saved credentials for %s — run /login to sign in to this account", account)}
				}
				return authReloadMsg{err: fmt.Errorf("account switch: %w", err)}
			}
			return authReloadMsg{client: m.cfg.NewAPIClient(tok), profile: prof, tokens: tok}
		}
	}
	m.refreshViewport()
	return m, nil
}

// applyPluginPanel handles the "plugin-panel" command result.
func (m Model) applyPluginPanel(res commands.Result) (Model, tea.Cmd) {
	p, err := newPluginPanel(res.Text)
	if err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: err.Error()})
		m.refreshViewport()
		return m, nil
	}
	// Build discover items synchronously (reads marketplace.json files).
	installedIDs := map[string]bool{}
	for _, item := range p.installedItems {
		installedIDs[item.pluginID] = true
	}
	p.buildDiscoverItems(installedIDs)
	// Inject MCP sub-entries.
	p.injectMCPSubEntries(m.cfg.MCPManager)
	m.pluginPanel = p
	m.refreshViewport()
	// Kick off async install count loading.
	return m, func() tea.Msg {
		counts, err := plugins.LoadInstallCounts()
		return pluginCountsMsg{counts: counts, err: err}
	}
}

// applySettingsPanel handles the "settings-panel" command result.
func (m Model) applySettingsPanel(res commands.Result) (Model, tea.Cmd) {
	// res.Text is the default tab name: "status", "config", "stats", "usage"
	defaultTab := settingsTabStatus
	switch res.Text {
	case "config":
		defaultTab = settingsTabConfig
	case "stats":
		defaultTab = settingsTabStats
	case "usage":
		defaultTab = settingsTabUsage
	case "accounts":
		defaultTab = settingsTabAccounts
	}
	cwd, _ := os.Getwd()
	sessPath := ""
	if m.cfg.Session != nil {
		sessPath = m.cfg.Session.FilePath
	}
	var getMCPInfo func() []mcpInfoRow
	if m.cfg.MCPManager != nil {
		getMCPInfo = func() []mcpInfoRow {
			servers := m.cfg.MCPManager.Servers()
			rows := make([]mcpInfoRow, 0, len(servers))
			for _, srv := range servers {
				rows = append(rows, mcpInfoRow{
					name:   srv.Name,
					status: string(srv.Status),
					tools:  len(srv.Tools),
				})
			}
			return rows
		}
	}
	live := m.cfg.Live
	getStatus := func() statusSnapshot {
		snap := statusSnapshot{}
		if live != nil {
			snap.sessionID = live.SessionID()
			snap.model = live.ModelName()
			snap.fastMode = live.FastMode()
			snap.effort = live.EffortLevel()
			snap.rateLimitWarn = live.RateLimitWarning()
			in, _, cost := live.Tokens()
			snap.inputTokens = in
			snap.costUSD = cost
			switch live.PermissionMode() {
			case permissions.ModeAcceptEdits:
				snap.permMode = "acceptEdits"
			case permissions.ModePlan:
				snap.permMode = "plan"
			case permissions.ModeBypassPermissions:
				snap.permMode = "bypassPermissions"
			default:
				snap.permMode = "default"
			}
		}
		snap.version = m.cfg.Version
		snap.authenticated = !m.noAuth
		return snap
	}
	rlInfo := ratelimit.Info{}
	saveConfigFn := func(id string, value interface{}) {
		// Map config item IDs to settings keys where they differ.
		key := id
		switch id {
		case "defaultPermissionMode":
			if s, ok := value.(string); ok {
				_ = settings.SaveConduitPermissionsField("defaultMode", permModeStoredVal(s))
				return
			}
		case "notifChannel":
			key = "preferredNotifChannel"
		case "alwaysThinkingEnabled":
			key = "alwaysThinkingEnabled"
		case "outputStyle":
			if s, ok := value.(string); ok {
				_ = settings.SaveOutputStyle(outputStyleStoredVal(s))
				return
			}
		case "theme":
			// Apply the theme live so the panel re-renders with new colors.
			if s, ok := value.(string); ok {
				theme.Set(s)
				_ = settings.SaveConduitTheme(s)
				return
			}
		}
		_ = settings.SaveConduitRawKey(key, value)
	}
	panel, statsCmd := newSettingsPanel(
		defaultTab, getStatus, getMCPInfo,
		saveConfigFn,
		m.cfg.Gate, m.cfg.MCPManager, sessPath, rlInfo, cwd,
	)
	m.settingsPanel = panel
	m.refreshViewport()
	return m, statsCmd
}

// applyPickerResult handles the "picker" command result.
func (m Model) applyPickerResult(res commands.Result) (Model, tea.Cmd) {
	// Open generic picker overlay. JSON payload in res.Text:
	//   {"title":"...","current":"dark","items":[{"value":"dark","label":"Dark"}]}
	// Kind ("theme"|"model"|"output-style") comes from res.Model.
	var payload struct {
		Title   string       `json:"title"`
		Current string       `json:"current"`
		Items   []pickerItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(res.Text), &payload); err != nil || len(payload.Items) == 0 {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "picker: invalid or empty payload"})
		m.refreshViewport()
		return m, nil
	}
	if res.Model == "model" {
		payload.Items = m.filterModelPickerItems(payload.Items)
		if len(payload.Items) == 0 {
			m.messages = append(m.messages, Message{Role: RoleError, Content: "No configured model providers. Add an account or MCP provider first."})
			m.refreshViewport()
			return m, nil
		}
	}
	current := payload.Current
	role := settings.RoleDefault
	if res.Model == "model" {
		current = m.providerValueForRole(role)
	}
	// Position cursor on the current value if present.
	sel := selectedPickerIndex(payload.Items, current)
	m.picker = &pickerState{
		kind:     res.Model,
		title:    payload.Title,
		items:    payload.Items,
		selected: sel,
		current:  current,
		role:     role,
	}
	m.refreshViewport()
	return m, nil
}

// applyResumePick handles the "resume-pick" command result.
func (m Model) applyResumePick(res commands.Result) (Model, tea.Cmd) {
	// Parse tab-separated session lines from the command result.
	// Format: filePath\tage\ttitle\trecordCount\tsize
	var sessions []resumeSession
	for _, line := range strings.Split(res.Text, "\n") {
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 3 {
			continue
		}
		rs := resumeSession{
			filePath: parts[0],
			age:      parts[1],
			preview:  parts[2],
		}
		if len(parts) == 4 {
			_, _ = fmt.Sscanf(parts[3], "%d", &rs.msgCount)
		}
		if len(parts) == 5 {
			_, _ = fmt.Sscanf(parts[3], "%d", &rs.msgCount)
			rs.size = parts[4]
		}
		sessions = append(sessions, rs)
	}
	if len(sessions) == 0 {
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "No previous sessions found."})
		m.refreshViewport()
		return m, nil
	}
	p := &resumePromptState{sessions: sessions, selected: 0}
	// Initialize filtered list with all sessions (no filter yet).
	p.filtered = make([]int, len(sessions))
	for i := range sessions {
		p.filtered[i] = i
	}
	m.resumePrompt = p
	m.refreshViewport()
	return m, nil
}

// applySearchPanel handles the "search-panel" command result.
func (m Model) applySearchPanel(res commands.Result) (Model, tea.Cmd) {
	// res.Text = newline-separated tab-separated results; res.Model = query term.
	var results []searchResult
	for _, line := range strings.Split(strings.TrimSpace(res.Text), "\n") {
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 5 {
			continue
		}
		results = append(results, searchResult{
			filePath: parts[0],
			title:    parts[1],
			age:      parts[2],
			role:     parts[3],
			snippet:  parts[4],
		})
	}
	m.searchPanel = &searchPanelState{query: res.Model, results: results}
	m.refreshViewport()
	return m, nil
}

// applyOutputStyle handles the "output-style" command result.
func (m Model) applyOutputStyle(res commands.Result) (Model, tea.Cmd) {
	// res.Model = style name, res.Text = style prompt (empty to clear).
	m.outputStyleName = res.Model
	m.outputStylePrompt = res.Text
	// Rebuild system blocks. The Max-subscription wire fingerprint
	// requires system[0..3] to be billing/identity/agent/output-guidance
	// in exact order — prepending anything else returns a literal
	// 429 rate_limit_error with no quota actually hit. So we append
	// the style block AFTER the base fingerprint blocks.
	if m.cfg.Loop != nil {
		cwd, _ := os.Getwd()
		mem := memdir.BuildPrompt(cwd)
		baseBlocks := agent.BuildSystemBlocks(mem, "")
		if res.Text != "" {
			styleBlock := api.SystemBlock{Type: "text", Text: "# Output style: " + res.Model + "\n\n" + res.Text}
			newBlocks := append(baseBlocks, styleBlock)
			m.cfg.Loop.SetSystem(newBlocks)
		} else {
			m.cfg.Loop.SetSystem(baseBlocks)
		}
	}
	// Persist to settings so the style survives restarts.
	_ = settings.SaveOutputStyle(res.Model)
	msg := "Output style cleared."
	if res.Model != "" {
		msg = fmt.Sprintf("Output style set to: %s", res.Model)
	}
	m.messages = append(m.messages, Message{Role: RoleSystem, Content: msg})
	m.refreshViewport()
	return m, nil
}

// applyRewind handles the "rewind" command result.
func (m Model) applyRewind(res commands.Result) (Model, tea.Cmd) {
	// res.Text is the number of turns removed (as string from the command).
	// Trim from m.history: each "turn" is one user+assistant message pair.
	n := 1
	_, _ = fmt.Sscanf(res.Text, "%d", &n)
	removed := 0
	for i := 0; i < n && len(m.history) >= 2; i++ {
		// Remove the last user+assistant pair from the API history.
		m.history = m.history[:len(m.history)-2]
		removed++
	}
	// Also trim display messages — keep system messages, remove last n user+assistant pairs.
	for i := 0; i < removed; i++ {
		// Walk backwards to find and remove the last assistant then user display message.
		for j := len(m.messages) - 1; j >= 0; j-- {
			if m.messages[j].Role == RoleAssistant {
				m.messages = append(m.messages[:j], m.messages[j+1:]...)
				break
			}
		}
		for j := len(m.messages) - 1; j >= 0; j-- {
			if m.messages[j].Role == RoleUser {
				m.messages = append(m.messages[:j], m.messages[j+1:]...)
				break
			}
		}
	}
	if removed > 0 {
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Rewound %d turn(s).", removed)})
	} else {
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Nothing to rewind."})
	}
	m.refreshViewport()
	return m, nil
}
