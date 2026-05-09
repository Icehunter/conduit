package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/tools/planmodetool"
	"github.com/icehunter/conduit/internal/tools/tasktool"
	"github.com/icehunter/conduit/internal/tui/workinganim"
)

// Update is the Elm update function.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)

	case tea.InterruptMsg:
		return m.handleInterrupt(msg)

	case tea.PasteMsg:
		return m.handlePaste(msg)

	case tea.MouseClickMsg:
		if m.handleMouseClick(msg, image.Rect(0, 0, m.width, m.height)) {
			return m, tea.Batch(cmds...)
		}

	case tea.MouseMotionMsg:
		if m.handleMouseMotion(msg, image.Rect(0, 0, m.width, m.height)) {
			return m, tea.Batch(cmds...)
		}

	case tea.MouseReleaseMsg:
		if handled, cmd := m.handleMouseRelease(msg, image.Rect(0, 0, m.width, m.height)); handled {
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}

	case tea.MouseWheelMsg:
		mouse := msg.Mouse()
		if m.planApproval != nil {
			// Wheel scrolls the plan viewport when the plan-approval modal is open.
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.planApproval.vp.ScrollUp(3)
			case tea.MouseWheelDown:
				m.planApproval.vp.ScrollDown(3)
			}
			return m, nil
		}
		if m.diffReview != nil {
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.diffReview.diffVP.ScrollUp(3)
			case tea.MouseWheelDown:
				m.diffReview.diffVP.ScrollDown(3)
			}
			return m, nil
		}
		// Otherwise fall through to sub-component propagation for the main viewport.

	case tea.KeyPressMsg:
		m2, cmd, consumed := m.handleKey(msg)
		m = m2
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if consumed {
			// Key was fully handled — skip textarea/viewport so the raw key
			// doesn't also move the textarea cursor or scroll the viewport.
			if !m.running && m.cfg.Commands != nil {
				m.cmdMatches, m.cmdSelected = m.computeCommandMatches()
			}
			if !m.running {
				m = m.updateAtMatches()
			}
			return m, tea.Batch(cmds...)
		}
		// Not consumed — fall through so textarea and viewport get the key.

	case agentMsg:
		m = m.applyAgentEvent(msg.event)
		return m, nil

	case planUsageMsg:
		return m.handlePlanUsage(msg)

	case planUsageTickMsg:
		if !m.usageStatusEnabled || m.cfg.FetchPlanUsage == nil || m.planUsageFetching {
			return m, nil
		}
		if time.Now().Before(m.planUsageBackoff) {
			return m, planUsageTick()
		}
		return m.startPlanUsageFetch()

	case agentDoneMsg:
		return m.handleAgentDone(msg)

	case loginStartMsg, loginURLMsg, loginBrowserFailMsg, loginDoneMsg,
		authReloadMsg, accountSwitchedMsg, commandsLoginMsg:
		m2, cmd, _ := m.handleLoginMsg(msg)
		return m2, cmd

	case trustAcceptedMsg:
		// Trust accepted and persisted — dialog already cleared in acceptTrust.
		m.refreshViewport()
		return m, nil

	case permissionAskMsg:
		m.permPrompt = &permissionPromptState{
			toolName:  msg.toolName,
			toolInput: msg.toolInput,
			reply:     msg.reply,
			selected:  0,
		}
		m.refreshViewport()
		return m, nil

	case questionAskMsg:
		// AskUserQuestion: open the interactive selection dialog overlay.
		state := &questionAskState{
			question:   msg.question,
			options:    msg.options,
			multi:      msg.multi,
			reply:      msg.reply,
			focusedIdx: 0,
			selected:   make([]bool, len(msg.options)),
		}
		m.questionAsk = state
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, nil

	case planApprovalAskMsg:
		// ExitPlanMode: open the plan-approval take-over modal.
		// Sized lazily by the renderer; pass placeholder dims so the state
		// is constructable, then resize() happens on first render.
		m.planApproval = newPlanApprovalState(msg.plan, msg.reply, 60, 12)
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, nil

	case diffReviewAskMsg:
		// Diff-first review gate: open the per-file approve/revert overlay.
		m.diffReview = newDiffReviewState(msg.entries, msg.reply)
		m.refreshViewport()
		return m, nil

	case diffReviewFollowupMsg:
		// Enqueue the feedback message so it fires as the next user turn
		// once the agent-done transition clears the running state.
		m.pendingMessages = append(m.pendingMessages, msg.text)
		return m, nil

	case mcpReconnectDoneMsg:
		// Reconnect finished — re-open the /mcp panel with fresh server state.
		if m.cfg.Commands != nil {
			if res, ok := m.cfg.Commands.Dispatch("/mcp"); ok {
				m2, cmd := m.applyCommandResult(res)
				return m2, cmd
			}
		}
		return m, nil

	case mcpAuthDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("MCP OAuth failed for %q: %v", msg.name, msg.err)})
		} else if msg.reconnectErr != nil {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("OAuth tokens saved for %q. Reconnect failed (%v) — run /mcp auth %s to retry.", msg.name, msg.reconnectErr, msg.name)})
		} else {
			m.flashMsg = fmt.Sprintf("Authenticated %q ✓", msg.name)
		}
		m.refreshViewport()
		return m, nil

	case compactDoneMsg:
		return m.handleCompactDone(msg)

	case localCallDoneMsg:
		return m.handleLocalCallDone(msg)

	case resumePickMsg:
		if len(msg.sessions) == 0 {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "No previous sessions found for this directory."})
			m.refreshViewport()
			return m, nil
		}
		m.resumePrompt = &resumePromptState{sessions: msg.sessions, selected: 0}
		m.refreshViewport()
		return m, nil

	case coordTickMsg:
		// Re-arm the tick whenever there's still active work to display.
		// When idle, we let it fall off — next sub-agent run schedules a
		// fresh tick from wherever the work was kicked off (TaskCreate
		// could call coordTick if needed, but the working indicator tick is
		// already running during agent.Run so we don't lose ticks during work).
		hasActive := false
		for _, t := range tasktool.GlobalStore().List() {
			if t.Status == tasktool.StatusInProgress {
				hasActive = true
				break
			}
		}
		if hasActive {
			cmds = append(cmds, coordTick())
		}
		return m, tea.Batch(cmds...)

	case mcpApprovalMsg:
		return m.handleMCPApproval(msg)

	case resumeLoadMsg:
		return m.handleResumeLoad(msg)

	case workinganim.StepMsg:
		cmds = append(cmds, m.working.Animate(msg))
		// Maintain the spinner status suffix ("(thought for Xs · ↑ N · Thinking)").
		// Capture run start on the first tick after running flips on; clear
		// status when running flips off.
		if m.running {
			if m.runStartedAt.IsZero() {
				m.runStartedAt = time.Now()
			}
			m.working.SetStatus(time.Since(m.runStartedAt), m.contextInputTokens, m.totalOutputTokens)
		} else if !m.runStartedAt.IsZero() {
			m.runStartedAt = time.Time{}
			m.working.ClearStatus()
		}

	case clearFlash:
		m.flashMsg = ""
		return m, nil

	case clearBubble:
		m.companionBubble = ""
		return m, nil

	case clearMouseSelectionMsg:
		m.mouseSelect = nil
		m.applyViewportSelection()
		return m, nil

	case companionBubbleMsg:
		m.companionBubble = msg.text
		return m, tea.Tick(5*time.Second, func(_ time.Time) tea.Msg { return clearBubble{} })

	case buddyTickMsg:
		if m.companionName != "" {
			m.buddyFrame++
			return m, buddyTick()
		}
		return m, nil

	case councilChatMsg:
		return m.handleCouncilChat(msg)

	case councilChatDoneMsg:
		m.running = false
		m.cancelTurn = nil
		m.councilCancel = nil
		m.councilRound = 0
		m.councilSynthesizing = false
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: msg.err.Error()})
			m.refreshViewport()
			m.vp.GotoBottom()
			m.input.Focus()
			return m, nil
		}
		if msg.synthesis != "" {
			m.totalInputTokens += msg.usage.InputTokens
			m.totalOutputTokens += msg.usage.OutputTokens
			m.costUSD += msg.costUSD
			// Post the council synthesis to history so it's visible above the
			// plan-approval modal. The picker gives the user a chance to approve
			// (which activates a permission mode) or chat about it further.
			m.messages = append(m.messages, Message{
				Role:     RoleCouncil,
				Content:  msg.synthesis,
				ToolName: "Council",
			})
			m.refreshViewport()
			m.vp.GotoBottom()
			if msg.reply != nil {
				m.planApproval = newPlanApprovalState(msg.synthesis, msg.reply, 60, 12)
				m.refreshViewport()
				return m, nil
			}
		}
		m.refreshViewport()
		m.vp.GotoBottom()
		m.input.Focus()
		return m, nil

	case councilStartMsg:
		return m.handleCouncilFlow(msg)

	case councilRoundStartMsg:
		m.councilRound = msg.round
		m.councilMaxRounds = msg.total
		m.councilActiveCount = msg.active
		m.councilSynthesizing = false
		return m, nil

	case councilMemberResponseMsg:
		if m.verboseMode {
			m.messages = append(m.messages, Message{
				Role:     RoleCouncil,
				Content:  msg.text,
				ToolName: msg.label,
			})
			if msg.agreed {
				m.messages = append(m.messages, Message{
					Role:     RoleCouncil,
					Content:  "✓ agrees with current direction",
					ToolName: msg.label,
				})
			}
			m.refreshViewport()
			m.vp.GotoBottom()
		}
		return m, nil

	case councilMemberEjectedMsg:
		// Always show ejections — a silent failure means synthesis gets an empty
		// prompt and produces a confusing "I don't see any responses" reply.
		m.messages = append(m.messages, Message{
			Role:     RoleCouncil,
			Content:  "⚠ ejected: " + msg.reason,
			ToolName: msg.label,
		})
		m.refreshViewport()
		return m, nil

	case councilSynthesisStartMsg:
		m.councilSynthesizing = true
		m.messages = append(m.messages, Message{
			Role:     RoleCouncil,
			Content:  "Synthesising agreed points…",
			ToolName: "Council",
		})
		m.refreshViewport()
		return m, nil

	case councilDoneMsg:
		m.councilCancel = nil
		m.councilRound = 0
		m.councilSynthesizing = false
		m.totalInputTokens += msg.usage.InputTokens
		m.totalOutputTokens += msg.usage.OutputTokens
		m.costUSD += msg.costUSD

		// A plan is "valid" only if there's no error AND the synthesis is
		// non-empty after trimming. Empty/error plans must NOT open the
		// approval modal — doing so leaves the user staring at a blank
		// viewport whose Approve button fires a decision against nothing.
		trimmedPlan := strings.TrimSpace(msg.plan)
		validPlan := msg.err == nil && trimmedPlan != ""

		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: msg.err.Error()})
		} else if !validPlan {
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: "Council synthesis returned an empty plan. Try a more focused question, raise councilSynthesizerMaxTokens in conduit.json, or check the synthesizer model's availability.",
			})
		} else {
			m.messages = append(m.messages, Message{
				Role:     RoleCouncil,
				Content:  msg.plan,
				ToolName: "Council",
			})
		}
		m.refreshViewport()
		m.vp.GotoBottom()

		reply := m.councilReply
		if reply != nil {
			m.councilReply = nil
			if validPlan {
				m.planApproval = newPlanApprovalState(msg.plan, reply, 60, 12)
				m.refreshViewport()
			} else {
				// Release the waiting goroutine — without this the chat-path
				// reader in handleCouncilChat blocks forever on replyCh.
				go func() {
					reply <- planmodetool.PlanApprovalDecision{
						Approved: false,
						Feedback: "council synthesis failed",
					}
				}()
			}
		}
		return m, nil

	case setPermissionModeMsg:
		m.applyPermissionMode(msg.mode)
		m2, cmd := m.startPlanUsageFetch()
		if rebuildCmd := m2.rebuildSystemCmd(); rebuildCmd != nil {
			return m2, tea.Batch(cmd, rebuildCmd)
		}
		return m2, cmd

	case setModelNameMsg:
		m.modelName = msg.name
		m.fastMode = msg.fast
		m.syncLive()
		return m, nil

	case pluginCountsMsg:
		if m.pluginPanel != nil && msg.err == nil {
			m.pluginPanel.loadingCounts = false
			m.pluginPanel.applyInstallCounts(msg.counts)
		}
		return m, nil

	case pluginInstallMsg:
		return m.handlePluginInstall(msg)

	case pluginMarketplaceAddMsg:
		return m.handlePluginMarketplaceAdd(msg)

	case pluginPanelReloadMsg:
		return m.handlePluginPanelReload(msg)

	case settingsStatsMsg:
		if m.settingsPanel != nil {
			m.settingsPanel.statsData = msg.stats
			m.settingsPanel.statsLoaded = true
			m.refreshViewport()
		}
		return m, nil

	case subagentPanelRefreshMsg:
		if m.subagentPanel == nil {
			return m, nil
		}
		// If the tracked sub-agent is gone, keep panel open so user can read the log.
		return m, tickSubagentPanel()

	}

	// Propagate remaining messages to sub-components.
	// Skip textarea/viewport when an overlay is active — they must not
	// consume keys (especially Escape) that belong to the overlay.
	overlayActive := m.loginPrompt != nil || m.resumePrompt != nil ||
		m.panel != nil || m.pluginPanel != nil || m.settingsPanel != nil ||
		m.permPrompt != nil || m.picker != nil || m.onboarding != nil ||
		m.questionAsk != nil || m.planApproval != nil || m.trustDialog != nil
	var taCmd, vpCmd tea.Cmd
	if !overlayActive {
		prevLines := m.input.LineCount()
		// Pre-grow before a newline insert. Without this, bubbles textarea
		// receives the insert at the old Height=N, repositionView scrolls
		// the viewport down by 1 to keep the cursor visible, and the
		// textarea's internal YOffset becomes 1. A later SetHeight(N+1)
		// only grows the *capacity* — it doesn't reset YOffset, so the
		// first row stays scrolled offscreen. Pre-growing means the insert
		// happens with capacity already in place: cursor on row N is still
		// within [YOffset=0, YOffset+Height-1=N], no scroll fires.
		if k, ok := msg.(tea.KeyPressMsg); ok && isNewlineInsertKey(k) {
			nextLines := m.input.LineCount() + 1
			cap := chromeHeight(nextLines, m.height-m.usageFooterRows()) - chromeFixed
			if nextLines <= cap {
				m.input.SetHeight(nextLines)
			}
		}
		m.input, taCmd = m.input.Update(msg)
		// If the input grew or shrunk a line (Alt+Enter, backspace into a
		// newline boundary, paste of multi-line content), reflow the
		// viewport so chat doesn't get squeezed by a now-taller input.
		if m.input.LineCount() != prevLines {
			m = m.applyLayout()
			m.refreshViewport()
		}
		m.vp, vpCmd = m.vp.Update(msg)
	}
	cmds = append(cmds, taCmd, vpCmd)

	// Recompute command picker matches after every key so the list stays live.
	if !m.running && m.cfg.Commands != nil {
		m.cmdMatches, m.cmdSelected = m.computeCommandMatches()
	}
	// Recompute @ file picker matches after every key.
	if !m.running {
		m = m.updateAtMatches()
	}

	return m, tea.Batch(cmds...)
}
