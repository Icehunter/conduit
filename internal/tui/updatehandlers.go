package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/attach"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/coordinator"
	"github.com/icehunter/conduit/internal/planusage"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
)

// handleWindowSize recalculates layout after a terminal resize.
func (m Model) handleWindowSize(msg tea.WindowSizeMsg) (Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	m = m.applyLayout()
	// Erase the entire screen and home the cursor on every resize.
	// tea.ClearScreen only clears the visible area; the explicit sequence
	// also resets the scroll region, preventing ghost chrome lines from
	// appearing in the scrollback after an iTerm2 resize.
	return m, tea.Batch(
		tea.ClearScreen,
		func() tea.Msg {
			// Force a full repaint by sending a no-op that triggers re-render.
			return nil
		},
	)
}

// handleInterrupt processes Ctrl+C / SIGINT: cancels a running turn or quits
// when idle.
func (m Model) handleInterrupt(_ tea.InterruptMsg) (Model, tea.Cmd) {
	if m.questionAsk != nil {
		// Cancel a pending AskUserQuestion — send nil so the tool returns
		// "no answer" rather than blocking forever.
		m.questionAsk.reply <- nil
		m.questionAsk = nil
	}
	if m.running && m.cancelTurn != nil {
		m.cancelTurn()
		m.running = false
		m.cancelTurn = nil
		m.cancelled = true
		if m.streaming != "" {
			m.messages = append(m.messages, m.assistantMessage(m.streaming))
			m.streaming = ""
		}
		// Mark any in-flight tool rows as interrupted so they don't
		// stay stuck showing "running…" after the cancel.
		for i := range m.messages {
			if m.messages[i].Role == RoleTool && m.messages[i].Content == "running…" {
				m.messages[i].Content = "interrupted."
				m.messages[i].ToolError = true
			}
		}
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Interrupted."})
		m.refreshViewport()
		m.input.Focus()
		return m, nil
	}
	return m, tea.Quit
}

// handlePaste processes bracketed paste events, handling file drag-drop
// detection and large paste placeholders.
func (m Model) handlePaste(msg tea.PasteMsg) (Model, tea.Cmd) {
	if m.settingsPanel != nil && m.settingsPanel.providerForm != nil {
		content := strings.ReplaceAll(msg.Content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
		m.settingsPanel.providerForm.input += strings.TrimSpace(content)
		m.refreshViewport()
		return m, nil
	}
	if m.pluginPanel != nil && m.pluginPanel.view == pluginViewAddMkt {
		content := strings.ReplaceAll(msg.Content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
		m.pluginPanel.addMktInput += strings.TrimSpace(content)
		m.refreshViewport()
		return m, nil
	}
	hasOverlay := m.loginPrompt != nil || m.resumePrompt != nil ||
		m.panel != nil || m.pluginPanel != nil || m.settingsPanel != nil ||
		m.permPrompt != nil || m.picker != nil || m.onboarding != nil ||
		m.doctorPanel != nil || m.searchPanel != nil || m.helpOverlay != nil
	if !hasOverlay {
		content := strings.ReplaceAll(msg.Content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")

		// File drag-drop detection: terminals paste dragged files as
		// "file:///path/to/file" URIs or shell-escaped absolute paths.
		// Images → pendingImages badge; PDFs → pendingPDFs badge; other files → @mention.
		if paths, ok := attach.DetectDroppedPaths(strings.TrimSpace(content)); ok {
			for _, p := range paths {
				switch attach.DroppedFileType(p) {
				case attach.DropImage:
					if img, err := attach.ReadImageFile(p); err == nil {
						m.pendingImages = append(m.pendingImages, img)
					} else {
						m.input.InsertString(attach.MentionPath(p))
					}
				case attach.DropPDF:
					if pdf, err := attach.ReadPDFFile(p); err == nil {
						m.pendingPDFs = append(m.pendingPDFs, pdf)
					} else {
						m.input.InsertString(attach.MentionPath(p))
					}
				default:
					m.input.InsertString(attach.MentionPath(p))
				}
			}
			n := len(m.pendingImages) + len(m.pendingPDFs)
			if n > 0 {
				parts := []string{}
				if ni := len(m.pendingImages); ni > 0 {
					parts = append(parts, fmt.Sprintf("%d image(s)", ni))
				}
				if np := len(m.pendingPDFs); np > 0 {
					parts = append(parts, fmt.Sprintf("%d PDF(s)", np))
				}
				m.flashMsg = "📎 [" + strings.Join(parts, ", ") + "]  · Enter to send · Esc to clear"
				return m, tea.Tick(5*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })
			}
			return m, nil
		}

		lineCount := strings.Count(content, "\n") + 1
		isLarge := lineCount > 1 || len(content) > 300
		if isLarge {
			// Store raw content and insert a removable placeholder token.
			// Mirrors CC's "[Pasted text #N +X lines]" UX. The placeholder
			// is a single pseudo-word so backspace removes it whole.
			m.pastedSeq++
			seq := m.pastedSeq
			if m.pastedBlocks == nil {
				m.pastedBlocks = map[int]string{}
			}
			m.pastedBlocks[seq] = content
			placeholder := fmt.Sprintf("[Pasted text #%d +%d lines]", seq, lineCount)
			m.input.InsertString(placeholder)
			m.flashMsg = fmt.Sprintf("Pasted %d lines  (Esc to clear)", lineCount)
			return m, tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })
		}
		m.input.InsertString(content)
	}
	return m, nil
}

// handlePlanUsage processes a completed plan usage fetch.
func (m Model) handlePlanUsage(msg planUsageMsg) (Model, tea.Cmd) {
	m.planUsageFetching = false
	if msg.err != nil {
		backoff := planUsageErrBackoff(msg.err)
		m.planUsageBackoff = time.Now().Add(backoff)
		// Only surface the error when we have no cached data to show.
		if m.planUsageCachedAt.IsZero() {
			m.planUsageErr = msg.err.Error()
		}
		// Persist updated backoff so other instances (and restarts) respect it.
		entry := planusage.CacheEntry{
			Info:         m.planUsage,
			CachedAt:     m.planUsageCachedAt,
			BackoffUntil: m.planUsageBackoff,
		}
		saveCacheCmd := savePlanUsageCacheCmd(settings.ConduitDir(), m.planUsageProvider, entry)
		if m.usageStatusEnabled {
			return m, tea.Batch(planUsageTick(), saveCacheCmd)
		}
		return m, saveCacheCmd
	}
	m.planUsage = msg.info
	m.planUsageCachedAt = time.Now()
	m.planUsageBackoff = time.Time{}
	m.planUsageErr = ""
	entry := planusage.CacheEntry{
		Info:     m.planUsage,
		CachedAt: m.planUsageCachedAt,
	}
	saveCacheCmd := savePlanUsageCacheCmd(settings.ConduitDir(), m.planUsageProvider, entry)
	if m.usageStatusEnabled {
		return m, tea.Batch(planUsageTick(), saveCacheCmd)
	}
	return m, saveCacheCmd
}

// handleAgentDone processes the completion of an agent turn.
func (m Model) handleAgentDone(msg agentDoneMsg) (Model, tea.Cmd) {
	var cmds []tea.Cmd

	if msg.turnID != m.turnID {
		// Stale completion from a previous (interrupted) turn — discard.
		return m, nil
	}
	m.running = false
	m.cancelled = false
	m.cancelTurn = nil
	m.apiRetryStatus = ""
	if m.streaming != "" {
		m.messages = append(m.messages, m.assistantMessage(m.streaming))
		m.streaming = ""
	}
	if msg.cancelled || isCancelError(msg.err) {
		// Context was cancelled — Ctrl+C already committed partial history.
	} else if msg.err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: msg.err.Error()})
		if len(m.history) > 0 && m.history[len(m.history)-1].Role == "user" {
			m.history = m.history[:len(m.history)-1]
		}
	} else {
		m.history = m.annotateTurnProvider(msg.history)
		m.tallyTokens()
		// Record per-turn cost delta in both model and LiveState (LiveState
		// is read by GetTurnCosts from outside the Bubble Tea event loop).
		turnCostDelta := m.costUSD - m.prevCostUSD
		if turnCostDelta > 0 {
			m.turnCosts = append(m.turnCosts, turnCostDelta)
			if m.cfg.Live != nil {
				m.cfg.Live.AppendTurnCost(turnCostDelta)
			}
		}
		m.prevCostUSD = m.costUSD
		m.persistNewMessages(msg.history)
		if m.cfg.Session != nil && m.totalInputTokens > 0 {
			_ = m.cfg.Session.AppendCost(m.totalInputTokens, m.totalOutputTokens, m.costUSD)
		}
		// Short responses (≤4 lines, ≤200 chars) when user addressed the
		// companion go to the bubble only. Longer responses (Claude being
		// snarky, actually answering) stay in chat.
		var bubbleCmd tea.Cmd
		m, bubbleCmd = m.maybeFireCompanionBubble()
		if bubbleCmd != nil {
			cmds = append(cmds, bubbleCmd)
		}
		m.appendAssistantInfo(turnCostDelta)
	}
	// Final assistant message just committed — refreshViewport's
	// sticky-bottom honors a scrolled-up user. They explicitly
	// scrolled away while results were streaming; don't yank them
	// back when the turn finalizes.
	m.refreshViewport()
	m.input.Focus()

	// Drain pending messages: if the user typed while we were running,
	// auto-submit the first queued message now. Subsequent ones will be
	// sent in future agentDoneMsg cycles.
	if len(m.pendingMessages) > 0 {
		next := m.pendingMessages[0]
		m.pendingMessages = m.pendingMessages[1:]
		// Inject into input so the normal submit path fires.
		m.input.SetValue(next)
		// Send the synthetic Enter key to trigger submission.
		cmds = append(cmds, func() tea.Msg { return tea.KeyPressMsg{Code: tea.KeyEnter} })
	}

	return m, tea.Batch(cmds...)
}

// handleLoginMsg dispatches all auth-flow messages. Returns (Model, Cmd,
// handled) where handled=false means the message was not one of the auth
// types and the caller should continue its own switch.
func (m Model) handleLoginMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case loginStartMsg:
		useClaudeAI := msg.claudeAI
		prog := *m.cfg.Program
		loadAuth := m.cfg.LoadAuth
		newAPIClient := m.cfg.NewAPIClient
		return m, func() tea.Msg {
			display := &tuiLoginDisplay{prog: prog}
			if err := runLoginFlow(useClaudeAI, display); err != nil {
				prog.Send(loginDoneMsg{err: err})
				return nil
			}
			if loadAuth != nil && newAPIClient != nil {
				tok, prof, err := loadAuth(context.Background())
				if err != nil {
					prog.Send(loginDoneMsg{err: fmt.Errorf("reload credentials: %w", err)})
					return nil
				}
				prog.Send(loginDoneMsg{client: newAPIClient(tok), profile: prof, tokens: tok})
				return nil
			}
			prog.Send(loginDoneMsg{})
			return nil
		}, true

	case loginURLMsg:
		var sb strings.Builder
		sb.WriteString("Opening browser to sign in.\n")
		sb.WriteString("If the browser doesn't open, paste this URL:\n\n")
		sb.WriteString("  " + msg.automatic + "\n\n")
		sb.WriteString("Or, for a code-paste flow:\n\n")
		sb.WriteString("  " + msg.manual)
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: sb.String()})
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, nil, true

	case loginBrowserFailMsg:
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: fmt.Sprintf("Couldn't open browser (%v). Paste the URL above.", msg.err),
		})
		m.refreshViewport()
		return m, nil, true

	case loginDoneMsg:
		m2, cmd := m.handleLoginDone(msg)
		return m2, cmd, true

	case authReloadMsg:
		m2, cmd := m.handleAuthReload(msg)
		return m2, cmd, true

	case accountSwitchedMsg:
		m2, cmd := m.handleAccountSwitched(msg)
		return m2, cmd, true

	case commandsLoginMsg:
		// Trigger login flow from account panel "+ Add account" action.
		m.loginPrompt = &loginPromptState{selected: 0}
		m.refreshViewport()
		return m, nil, true
	}

	return m, nil, false
}

// handleLoginDone processes a completed login attempt.
func (m Model) handleLoginDone(msg loginDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		// Strip the ephemeral "Opening browser…" / URL messages on failure too.
		if m.loginFlowMsgStart >= 0 && m.loginFlowMsgStart < len(m.messages) {
			m.messages = m.messages[:m.loginFlowMsgStart]
		}
		m.loginFlowMsgStart = -1
		m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Login failed: %v", msg.err)})
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, nil
	}
	// Strip all ephemeral login flow messages (picker, "Opening browser…", URLs).
	if m.loginFlowMsgStart >= 0 && m.loginFlowMsgStart < len(m.messages) {
		m.messages = m.messages[:m.loginFlowMsgStart]
	}
	m.loginFlowMsgStart = -1
	m.noAuth = false
	if msg.client != nil && m.cfg.Loop != nil {
		m.cfg.APIClient = msg.client
		m.cfg.Loop.SetClient(msg.client)
		if msg.profile != nil {
			m.cfg.Profile = *msg.profile
		}
		m.messages = nil
		m.history = nil
		m.welcomeDismissed = false
		if _, ok := m.activeMCPProvider(); !ok {
			provider := accountBackedActiveProvider(m.modelName, m.cfg.Profile.Email, msg.tokens)
			m.setActiveProvider(provider)
			if suffix := persistActiveProvider(provider); suffix != "" {
				m.messages = append(m.messages, Message{Role: RoleError, Content: strings.TrimSpace(suffix)})
			}
		}
	}
	m.messages = append(m.messages, m.welcomeCard())
	m.refreshViewport()
	m.vp.GotoBottom()
	if msg.client != nil && m.usageStatusEnabled && m.cfg.FetchPlanUsage != nil {
		return m.startPlanUsageFetch()
	}
	return m, nil
}

// handleAuthReload processes a credential reload after an account switch.
func (m Model) handleAuthReload(msg authReloadMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Could not reload credentials: %v", msg.err)})
	} else if msg.client != nil {
		m.cfg.APIClient = msg.client
		m.cfg.Loop.SetClient(msg.client)
		if msg.profile != nil {
			m.cfg.Profile = *msg.profile
		}
		// Clear conversation and show welcome card for the new account.
		m.messages = nil
		m.history = nil
		m.welcomeDismissed = false
		m.messages = append(m.messages, m.welcomeCard())
		if _, ok := m.activeMCPProvider(); !ok {
			provider := accountBackedActiveProvider(m.modelName, m.cfg.Profile.Email, msg.tokens)
			m.setActiveProvider(provider)
			if suffix := persistActiveProvider(provider); suffix != "" {
				m.messages = append(m.messages, Message{Role: RoleError, Content: strings.TrimSpace(suffix)})
			}
		}
		if m.usageStatusEnabled && m.cfg.FetchPlanUsage != nil {
			return m.startPlanUsageFetch()
		}
	}
	m.refreshViewport()
	m.vp.GotoBottom()
	return m, nil
}

// handleAccountSwitched switches the active account and reloads credentials.
func (m Model) handleAccountSwitched(msg accountSwitchedMsg) (Model, tea.Cmd) {
	store, err := auth.ListAccounts()
	if err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "account switch: " + err.Error()})
		m.refreshViewport()
		return m, nil
	}
	if err := auth.SetActive(&store, msg.account); err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: err.Error()})
		m.refreshViewport()
		return m, nil
	}
	if m.cfg.LoadAuth != nil && m.cfg.NewAPIClient != nil && m.cfg.Loop != nil {
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Switching to %s…", msg.account)})
		m.refreshViewport()
		return m, func() tea.Msg {
			ctx := context.Background()
			tok, prof, err := m.cfg.LoadAuth(ctx)
			if err != nil {
				if errors.Is(err, auth.ErrNotLoggedIn) {
					return authReloadMsg{err: fmt.Errorf("no saved credentials for %s — run /login to add this account", msg.account)}
				}
				return authReloadMsg{err: fmt.Errorf("account switch: %w", err)}
			}
			return authReloadMsg{client: m.cfg.NewAPIClient(tok), profile: prof, tokens: tok}
		}
	}
	m.refreshViewport()
	return m.startPlanUsageFetch()
}

// handleCompactDone processes the result of a conversation compact operation.
func (m Model) handleCompactDone(msg compactDoneMsg) (Model, tea.Cmd) {
	m.running = false
	m.cancelTurn = nil
	if msg.err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Compact failed: %v", msg.err)})
	} else {
		m.history = msg.newHistory
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Conversation compacted. Summary:\n\n%s", msg.summary)})
		m.tallyTokens()
	}
	m.refreshViewport()
	m.vp.GotoBottom()
	m.input.Focus()
	return m, nil
}

// handleLocalCallDone processes the result of a local (MCP) tool call.
func (m Model) handleLocalCallDone(msg localCallDoneMsg) (Model, tea.Cmd) {
	if msg.turnID != m.turnID {
		return m, nil
	}
	m.running = false
	m.cancelTurn = nil
	if msg.err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Local call failed: %v", msg.err)})
		if msg.chat && len(m.history) > 0 && m.history[len(m.history)-1].Role == "user" {
			m.history = m.history[:len(m.history)-1]
		}
	} else {
		label := msg.call.Server
		if label == "" {
			label = "local"
		}
		text := strings.TrimSpace(msg.text)
		if text == "" {
			text = "(empty local response)"
		}
		m.messages = append(m.messages, Message{Role: RoleLocal, Content: text, ToolName: label})
		if msg.chat {
			m.history = append(m.history, api.Message{
				Role:         "assistant",
				Content:      []api.ContentBlock{{Type: "text", Text: text}},
				ProviderKind: "mcp",
				Provider:     label,
			})
			m.persistNewMessages(m.history)
		}
	}
	m.refreshViewport()
	m.vp.GotoBottom()
	m.input.Focus()
	return m, nil
}

// handleMCPApproval opens a 3-option picker for the first pending MCP server
// approval request.
func (m Model) handleMCPApproval(msg mcpApprovalMsg) (Model, tea.Cmd) {
	if len(msg.pending) == 0 {
		return m, nil
	}
	name := msg.pending[0]
	m.picker = &pickerState{
		kind:  "mcp-approve",
		title: fmt.Sprintf("Approve MCP server %q from .mcp.json?", name),
		items: []pickerItem{
			{Value: name + " yes", Label: "Yes — approve this server"},
			{Value: name + " yes_all", Label: "Yes, all project servers"},
			{Value: name + " no", Label: "No — deny and don't ask again"},
		},
		selected: 0,
	}
	m.refreshViewport()
	return m, nil
}

// handleResumeLoad processes a loaded previous session.
func (m Model) handleResumeLoad(msg resumeLoadMsg) (Model, tea.Cmd) {
	// Remove the "Loading session…" message.
	if len(m.messages) > 0 && m.messages[len(m.messages)-1].Content == "Loading session…" {
		m.messages = m.messages[:len(m.messages)-1]
	}
	if msg.err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Failed to load session: %v", msg.err)})
		m.refreshViewport()
		return m, nil
	}
	// Replace current history and rebuild display.
	m.history = msg.msgs
	m.persistedCount = len(msg.msgs)
	// Repoint cfg.Session so new turns append to the resumed file.
	if msg.filePath != "" {
		cwd, _ := os.Getwd()
		resumeSession, err := session.ImportForWrite(cwd, msg.filePath)
		if err != nil {
			resumeSession = session.FromFile(msg.filePath)
		}
		m.cfg.Session = resumeSession
		if m.cfg.Live != nil {
			m.cfg.Live.SetSessionID(resumeSession.ID)
			m.cfg.Live.SetSessionFile(resumeSession.FilePath)
		}
		// Restore coordinator mode if the session was in coordinator mode.
		if notice := coordinator.MatchSessionMode(m.cfg.Session.ReadMode()); notice != "" {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: notice})
		}
	}
	m.messages = append(m.messages, Message{
		Role:    RoleSystem,
		Content: fmt.Sprintf("Resumed previous conversation (%d messages). ↑ scroll to see history.", len(msg.msgs)),
	})
	m.messages = append(m.messages, historyToDisplayMessages(msg.msgs)...)
	m.tallyTokens()
	m.refreshViewport()
	m.vp.GotoBottom()
	return m, nil
}

// handlePluginInstall processes the result of a plugin install operation.
func (m Model) handlePluginInstall(msg pluginInstallMsg) (Model, tea.Cmd) {
	if m.pluginPanel == nil {
		return m, nil
	}
	p := m.pluginPanel
	if msg.err != nil {
		p.errors = append(p.errors, fmt.Sprintf("install %s: %v", msg.pluginID, msg.err))
		p.tab = pluginTabErrors
		p.view = pluginViewList
		m.pluginPanel = p
		return m, nil
	}
	// Reload full panel from disk so version/description/sort are correct.
	return m, reloadPluginPanelCmd(m.cfg.MCPManager, p.tab, p.errors)
}

func (m Model) handlePluginMarketplaceAdd(msg pluginMarketplaceAddMsg) (Model, tea.Cmd) {
	if m.pluginPanel == nil {
		return m, nil
	}
	p := m.pluginPanel
	if msg.err != nil {
		p.errors = append(p.errors, fmt.Sprintf("marketplace add %s: %v", msg.name, msg.err))
		p.tab = pluginTabErrors
		p.view = pluginViewList
		m.pluginPanel = p
		return m, nil
	}
	m.pluginPanel = p
	return m, reloadPluginPanelCmd(m.cfg.MCPManager, pluginTabMarketplaces, p.errors)
}

// handlePluginPanelReload rebuilds the plugin panel after an install/uninstall
// and kicks off a fresh install-counts fetch.
func (m Model) handlePluginPanelReload(msg pluginPanelReloadMsg) (Model, tea.Cmd) {
	if m.pluginPanel == nil {
		return m, nil
	}
	newPanel := rebuildPluginPanel(msg)
	newPanel.selected = 0
	m.pluginPanel = newPanel
	return m, func() tea.Msg {
		counts, err := plugins.LoadInstallCounts()
		return pluginCountsMsg{counts: counts, err: err}
	}
}
