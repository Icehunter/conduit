package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/buddy"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/mcp"
)

// ---------------------------------------------------------------------------
// Panel types
// ---------------------------------------------------------------------------

type panelTab int

const (
	panelTabMCP panelTab = 0
)

var panelTabNames = []string{"MCP"}

// mcpReconnectDoneMsg is sent after a panel "Reconnect" action completes
// so the TUI can re-open the /mcp panel with fresh server state.
type mcpReconnectDoneMsg struct{}

// panelMCPItem is one MCP server row — carries all info for detail view.
type panelMCPItem struct {
	name      string
	scope     string // "User" | "Project" | "Built-in"
	source    string // config file or "plugin:name"
	status    string // "connected" | "failed" | "pending" | "disabled"
	command   string // stdio command or URL
	args      string // space-separated args
	toolCount int
	err       string
	disabled  bool
	// tools populated on-demand when detail is opened
	tools []panelToolItem
}

// panelToolItem is one tool inside a server detail.
type panelToolItem struct {
	name        string
	fullName    string // e.g. mcp__plugin_context7_context7__resolve-library-id
	description string
}

// panelView is the navigation depth.
type panelView int

const (
	panelViewList       panelView = 0 // tab root list
	panelViewDetail     panelView = 1 // item detail (server/plugin/marketplace)
	panelViewTools      panelView = 2 // tool list inside a server
	panelViewToolDetail panelView = 3 // single tool detail
)

// panelState is the unified browser overlay.
type panelState struct {
	tab      panelTab
	view     panelView
	selected int // cursor within the current view (list row, action row, tool row)
	// serverIdx is preserved when drilling into detail/tools/tool-detail so
	// the render functions always know which server is being inspected.
	serverIdx int

	// raw encoded data
	mcpRaw string

	// parsed lists
	mcpItems []panelMCPItem
}

func newPanel(tab panelTab) *panelState {
	return &panelState{tab: tab}
}

func (p *panelState) parseMCPItems() {
	p.mcpItems = nil
	for _, line := range strings.Split(p.mcpRaw, "\n") {
		if line == "" {
			continue
		}
		// name\tscope\tsource\tstatus\tcommand\targs\ttoolCount\terr\tdisabled
		parts := strings.SplitN(line, "\t", 9)
		item := panelMCPItem{}
		get := func(i int) string {
			if i < len(parts) {
				return parts[i]
			}
			return ""
		}
		item.name = get(0)
		item.scope = get(1)
		item.source = get(2)
		item.status = get(3)
		item.command = get(4)
		item.args = get(5)
		_, _ = fmt.Sscanf(get(6), "%d", &item.toolCount)
		item.err = get(7)
		item.disabled = get(8) == "1"
		p.mcpItems = append(p.mcpItems, item)
	}
	// Sort so User then Project then Built-in — the visual order matches the
	// flat index used by p.selected.
	scopeRank := func(s string) int {
		switch s {
		case "User":
			return 0
		case "Project":
			return 1
		default:
			return 2
		}
	}
	for i := 1; i < len(p.mcpItems); i++ {
		for j := i; j > 0 && scopeRank(p.mcpItems[j].scope) < scopeRank(p.mcpItems[j-1].scope); j-- {
			p.mcpItems[j], p.mcpItems[j-1] = p.mcpItems[j-1], p.mcpItems[j]
		}
	}
}

// loadTabData is a no-op for the MCP-only panel (reserved for future tabs).
func (p *panelState) loadTabData() {}

func (p *panelState) currentLen() int {
	return len(p.mcpItems)
}

func (p *panelState) selectedMCPItem() *panelMCPItem {
	if p.serverIdx >= 0 && p.serverIdx < len(p.mcpItems) {
		return &p.mcpItems[p.serverIdx]
	}
	return nil
}

// handlePanelKey routes keyboard input through the panel navigation stack.
func (m Model) handlePanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) { //nolint:unparam
	p := m.panel
	key := msg.String()

	switch p.view {
	case panelViewList:
		switch key {
		case "left":
			p.tab = panelTab((int(p.tab) + len(panelTabNames) - 1) % len(panelTabNames))
			p.selected = 0
			p.loadTabData()
		case "right":
			p.tab = panelTab((int(p.tab) + 1) % len(panelTabNames))
			p.selected = 0
			p.loadTabData()
		case "up":
			if p.selected > 0 {
				p.selected--
			}
		case "down":
			if p.selected < p.currentLen()-1 {
				p.selected++
			}
		case "enter":
			if p.currentLen() > 0 {
				p.serverIdx = p.selected // remember which server/plugin was selected
				p.view = panelViewDetail
				p.selected = 0 // reset to first action in detail
			}
		case "esc", "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}

	case panelViewDetail:
		switch key {
		case "up":
			if p.selected > 0 {
				p.selected--
			}
		case "down":
			item2 := p.selectedMCPItem()
			if item2 != nil && p.selected < len(mcpActions(item2))-1 {
				p.selected++
			}
		case "enter":
			if p.tab == panelTabMCP {
				item := p.selectedMCPItem()
				if item == nil {
					break
				}
				actions := mcpActions(item)
				if p.selected >= len(actions) {
					break
				}
				action := actions[p.selected]
				switch action {
				case "View tools":
					// Populate tools from the live manager if not already cached.
					if m.cfg.MCPManager != nil && len(item.tools) == 0 {
						for _, srv := range m.cfg.MCPManager.Servers() {
							if srv.Name == item.name {
								prefix := mcp.ToolNamePrefix(srv.Name)
								for _, t := range srv.Tools {
									item.tools = append(item.tools, panelToolItem{
										name:        t.Name,
										fullName:    prefix + t.Name,
										description: t.Description,
									})
								}
								p.mcpItems[p.serverIdx] = *item
								break
							}
						}
					}
					p.view = panelViewTools
					p.selected = 0
				case "Authenticate":
					// Kick off the OAuth flow off the event loop exactly like /mcp auth.
					// Close the panel first so the user can see the progress message.
					m.panel = nil
					m.refreshViewport()
					res := commands.Result{Type: "mcp-auth", Text: item.name, Model: item.command}
					return m.applyMCPAuth(res)
				case "Reconnect":
					if m.cfg.MCPManager != nil {
						cwd, _ := os.Getwd()
						srvName := item.name
						mgr := m.cfg.MCPManager
						trusted := !m.cfg.NeedsTrust
						m.panel = nil
						m.refreshViewport()
						return m, func() tea.Msg {
							_ = mgr.Reconnect(context.Background(), srvName, cwd, trusted)
							return mcpReconnectDoneMsg{}
						}
					}
					p.view = panelViewList
					p.selected = 0
				case "Disable":
					cwd, _ := os.Getwd()
					_ = mcp.SetDisabled(item.name, cwd, true)
					p.mcpItems[p.serverIdx].disabled = true
					p.mcpItems[p.serverIdx].status = "disabled"
					// Close the live connection.
					if m.cfg.MCPManager != nil {
						go func() { m.cfg.MCPManager.DisconnectServer(item.name) }()
					}
					p.view = panelViewList
					p.selected = 0
				case "Enable":
					cwd, _ := os.Getwd()
					_ = mcp.SetDisabled(item.name, cwd, false)
					p.mcpItems[p.serverIdx].disabled = false
					p.mcpItems[p.serverIdx].status = "pending"
					// Reconnect.
					if m.cfg.MCPManager != nil {
						srvName := item.name
						mgr := m.cfg.MCPManager
						trusted := !m.cfg.NeedsTrust
						go func() { _ = mgr.Reconnect(context.Background(), srvName, cwd, trusted) }()
					}
					p.view = panelViewList
					p.selected = 0
				}
			}
		case "esc":
			p.view = panelViewList
			p.selected = 0 // cursor resets to top of list
		case "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}

	case panelViewTools:
		switch key {
		case "up":
			if p.selected > 0 {
				p.selected--
			}
		case "down":
			item := p.selectedMCPItem() // uses serverIdx
			if item != nil && p.selected < len(item.tools)-1 {
				p.selected++
			}
		case "enter":
			p.view = panelViewToolDetail
		case "esc", "q":
			p.view = panelViewDetail
			p.selected = 0
		case "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}

	case panelViewToolDetail:
		switch key {
		case "esc", "enter":
			p.view = panelViewTools
		case "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}
	}

	m.panel = p
	return m, nil
}

// ---------------------------------------------------------------------------
// applyCommandResult
// ---------------------------------------------------------------------------

// applyCommandResult handles a slash command result in the TUI.
func (m Model) applyCommandResult(res commands.Result) (Model, tea.Cmd) {
	switch res.Type {
	case "help_overlay":
		if m.helpOverlay == nil {
			m.helpOverlay = openHelpOverlay(m.width, m.panelHeight(), res.Text)
		}
		return m, nil
	case "commands":
		return m.openCommandPicker(), nil
	case "buddy":
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: res.Text})
		if sc, err := buddy.Load(); err == nil && sc != nil {
			startTick := m.companionName == ""
			m.companionName = sc.Name
			m.refreshViewport()
			if startTick {
				return m, buddyTick()
			}
			return m, nil
		}
		m.refreshViewport()
		return m, nil
	case "clear":
		m.messages = nil
		m.history = nil
		m.pendingMessages = nil
		m.refreshViewport()
		return m, nil
	case "exit":
		m.quitConfirm = &quitConfirmState{selected: 1}
		m.refreshViewport()
		return m, nil
	case "model":
		return m.applyModelSwitch(res)
	case "provider-switch":
		return m.applyProviderSwitch(res)
	case "compact":
		return m.applyCompactResult(res)
	case "local-call":
		return m.applyLocalCall(res)
	case "local-mode":
		return m.applyLocalMode(res)
	case "prompt":
		return m.applyPromptResult(res)
	case "coordinator-toggle":
		return m.applyCoordinatorToggle(res)
	case "usage-toggle":
		return m.applyUsageToggle(res)
	case "error":
		m.messages = append(m.messages, Message{Role: RoleError, Content: res.Text})
		m.refreshViewport()
		return m, nil
	case "login":
		m.loginPrompt = &loginPromptState{selected: 0}
		m.refreshViewport()
		return m, nil
	case "account-switch":
		return m.applyAccountSwitch(res)
	case "add-dir":
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Added directory: " + res.Text})
		m.refreshViewport()
		return m, nil
	case "mcp-dialog":
		p := newPanel(panelTabMCP)
		p.mcpRaw = res.Text
		p.parseMCPItems()
		m.panel = p
		m.refreshViewport()
		return m, nil
	case "plugin-panel":
		return m.applyPluginPanel(res)
	case "settings-panel":
		return m.applySettingsPanel(res)
	case "picker":
		return m.applyPickerResult(res)
	case "resume-pick":
		return m.applyResumePick(res)
	case "search-panel":
		return m.applySearchPanel(res)
	case "doctor-panel":
		// res.Text = newline-separated check lines; res.Model = binary + platform.
		m.doctorPanel = &doctorPanelState{
			checks:   strings.Split(strings.TrimSpace(res.Text), "\n"),
			platform: res.Model,
		}
		m.refreshViewport()
		return m, nil
	case "council-chat":
		if len(m.councilProviders) == 0 {
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: "No council members configured — add providers in /model → Council tab.",
			})
			m.refreshViewport()
			return m, nil
		}
		return m.handleCouncilChat(councilChatMsg{question: res.Text})
	case "output-style":
		return m.applyOutputStyle(res)
	case "rewind":
		return m.applyRewind(res)
	case "export":
		path := res.Text
		if err := m.exportConversation(path); err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Export failed: %v", err)})
		} else {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Conversation exported to: " + path})
		}
		m.refreshViewport()
		return m, nil
	case "mcp-auth":
		return m.applyMCPAuth(res)
	case "flash":
		// Briefly surface in the working row, then queue the next pending
		// MCP approval if any are still waiting after this one resolved.
		if res.Text != "" {
			m.flashMsg = res.Text
		}
		m.refreshViewport()
		var cmd tea.Cmd
		if m.cfg.MCPManager != nil {
			if pending := m.cfg.MCPManager.PendingApprovals(); len(pending) > 0 {
				cmd = func() tea.Msg { return mcpApprovalMsg{pending: pending} }
			}
		}
		return m, tea.Batch(cmd, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearFlash{} }))
	case "catalog-refresh":
		return m.applyCatalogRefresh()
	default: // "text"
		if res.Text != "" {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: res.Text})
			m.refreshViewport()
		}
		return m, nil
	}
}
