package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
	"github.com/icehunter/conduit/internal/tools/tasktool"
	"github.com/icehunter/conduit/internal/tui/workinganim"
)

// Update is the Elm update function. It wraps update to arm the animation
// clock whenever something needs frames (see needsAnimation): an idle session
// does no per-frame work.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(Model); ok && nm.wakePending && !nm.busy() {
		// Whatever kept the model busy has ended: deliver the notifications
		// that arrived meanwhile. A finishing turn leaves its title reset to
		// us (titleClearDeferred) so it cannot land on top of the new turn's.
		nm.wakePending = false
		var wake tea.Cmd
		nm, wake = nm.startInjectedTurn()
		if wake == nil && nm.titleClearDeferred {
			wake = clearWindowTitleCmd()
		}
		nm.titleClearDeferred = false
		next = nm
		if wake != nil {
			cmd = tea.Batch(cmd, wake)
		}
	}
	if nm, ok := next.(Model); ok && !nm.animTicking && nm.needsAnimation() {
		nm.animTicking = true
		return nm, tea.Batch(cmd, nm.working.Step())
	}
	return next, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		if msg.Code == tea.KeyEnter {
			m.replayingSubmit = false // the Enter a replay was waiting for has arrived
		}
		m2, cmd, consumed := m.handleKey(msg)
		m = m2
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if consumed {
			// Key was fully handled — skip textarea/viewport so the raw key
			// doesn't also move the textarea cursor or scroll the viewport.
			if m.cfg.Commands != nil {
				m.cmdMatches, m.cmdSelected = m.computeCommandMatches()
			}
			if !m.running {
				m = m.updateAtMatches()
			}
			// A consumed key (e.g. Backspace, Ctrl+C clearing the input) may have
			// emptied the textarea. Promote any deferred prompts and drain the
			// first pending message now so they surface without waiting for the
			// non-consumed code path.
			if m.input.Value() == "" {
				if m.pendingQuestion != nil && m.questionAsk == nil {
					m.questionAsk = newQuestionAskState(*m.pendingQuestion)
					m.pendingQuestion = nil
					m.refreshViewport()
					m.vp.GotoBottom()
				}
				if m.pendingPermission != nil && m.permPrompt == nil {
					p := m.pendingPermission
					m.pendingPermission = nil
					m.permPrompt = m.newPermissionPrompt(*p)
					m.refreshViewport()
				}
				if !m.running {
					var cmd tea.Cmd
					var drained bool
					if m, cmd, drained = m.drainPending(); drained {
						cmds = append(cmds, cmd)
					}
				}
			}
			return m, tea.Batch(cmds...)
		}
		// Not consumed — fall through so textarea and viewport get the key.

	case agentMsg:
		m = m.applyAgentEvent(msg.event)
		if msg.event.Type == agent.EventSessionTitle {
			return m, m.renameSessionCmd(msg.event.Notice)
		}
		return m, nil

	case promptScreenedMsg:
		return m.handlePromptScreened(msg)

	case sessionRenamedMsg:
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "UserPromptSubmit hook could not rename the session: " + msg.err.Error()})
			m.refreshViewport()
		}
		return m, nil

	case pastedImagesPrunedMsg:
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Could not prune old pasted images: " + msg.err.Error()})
		m.refreshViewport()
		return m, nil

	case compactSaveFailedMsg:
		return m.handleCompactSaveFailed(msg)

	case sessionResetDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Could not mark the cleared conversation in the session: %v", msg.err)})
			m.refreshViewport()
		}
		return m, nil

	case compactRewriteDoneMsg:
		return m.handleCompactRewriteDone(msg), nil

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
		// MCP reconnects from here on count the workspace as trusted, so
		// approved .mcp.json servers held at startup can connect.
		m.cfg.NeedsTrust = false
		m.refreshViewport()
		return m, nil

	case permissionAskMsg:
		if m.input.Value() != "" {
			// User is mid-draft. Queue the permission prompt until their input
			// clears — same deferral pattern as pendingQuestion. The tool
			// goroutine blocks on <-reply; ctx.Done() unblocks it on cancel.
			copy := msg
			m.pendingPermission = &copy
			m.flashMsg = "permission request waiting — send or clear your draft first"
			m.refreshViewport()
			return m, nil
		}
		m.permPrompt = m.newPermissionPrompt(msg)
		m.refreshViewport()
		return m, nil

	case questionAskMsg:
		// AskUserQuestion: open the interactive selection dialog overlay.
		if m.input.Value() != "" {
			// User is mid-draft. Queue the question until their input clears.
			// The tool goroutine blocks on <-reply; a pending question deferred
			// here will surface as soon as the input empties (see promotion check
			// in KeyPressMsg). On cancelTurn, the ctx.Done() unblocks the goroutine.
			copy := msg // copy to avoid pointer aliasing
			m.pendingQuestion = &copy
			m.flashMsg = "question waiting — send or clear your draft first"
			m.refreshViewport()
			return m, nil
		}
		// Input is empty — activate immediately.
		m.questionAsk = newQuestionAskState(msg)
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
		m.pendingMessages = append(m.pendingMessages, queuedMessage{text: msg.text})
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

	case btwDoneMsg:
		return m.handleBtwDone(msg), nil

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

	case shellHookMsg:
		return m.shellAuthorize(msg)

	case shellDoneMsg:
		return m.applyShellDone(msg)

	case editorReadyMsg:
		return m, runEditorCmd(msg)

	case editorDoneMsg:
		if msg.err != nil {
			m.flashMsg = "Editor: " + msg.err.Error()
			return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return clearFlash{} })
		}
		m.input.SetValue(msg.text)
		m.input.CursorEnd()
		return m.applyLayout(), nil

	case copyResultMsg:
		m.flashMsg = copyFlash(msg)
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearFlash{} })

	case memoryExtractDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("extract: %v", msg.err)})
		} else {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Memory extraction complete."})
		}
		m.refreshViewport()
		return m, nil

	case locationInfoMsg:
		cwdChanged := m.locCwd != msg.cwd
		m.locCwd, m.locBranch = msg.cwd, msg.branch
		if cwdChanged {
			// Rendered Bash rows drop a redundant `cd <cwd> &&`.
			setDisplayCwd(msg.cwd)
			m.refreshViewport()
		}
		return m, nil

	case locationTickMsg:
		return m, tea.Batch(readLocationCmd(), locationTick())

	case coordTickMsg:
		// Re-arm once a second while any task is in progress; this keeps the
		// coordinator footer fresh. It is independent of the animation clock,
		// which needs a fresh arm (via Update) when work starts after idle.
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
		// Keep the clock alive only while something needs frames; the wrapper
		// in Update re-arms it when the next run, sub-agent or workflow starts.
		if animCmd := m.working.Animate(msg); m.needsAnimation() {
			cmds = append(cmds, animCmd)
		} else {
			m.animTicking = false
		}
		// Maintain the spinner status suffix ("(thought for Xs · ↑ N · Thinking)").
		// Capture run start on the first tick after running flips on; clear
		// status when running flips off.
		if m.running {
			if m.runStartedAt.IsZero() {
				m.runStartedAt = time.Now()
			}
			m.working.SetStatus(time.Since(m.runStartedAt), m.contextInputTokens, m.totalOutputTokens, m.streamingCostUSD)
		} else if !m.runStartedAt.IsZero() {
			m.runStartedAt = time.Time{}
			m.streamingCostUSD = 0 // reset streaming cost when run ends
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

	case catalogRefreshedMsg:
		if msg.err != nil {
			m.flashMsg = "Catalog refresh failed: " + msg.err.Error()
		} else if msg.cat != nil {
			m.catalogData = msg.cat
			internalmodel.SetReportedWindows(msg.cat.LiveContextWindows())
			m.refreshModelCommandProviders()
			m.flashMsg = fmt.Sprintf("Model catalog updated — %d models", len(msg.cat.Models))
		}
		m.refreshViewport()
		return m, tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })

	case copilotOAuthStartedMsg:
		return m.handleCopilotOAuthStarted(msg)

	case copilotOAuthCompletedMsg:
		return m.handleCopilotOAuthCompleted(msg)

	case grokOAuthStartedMsg:
		return m.handleGrokOAuthStarted(msg)

	case grokOAuthCompletedMsg:
		return m.handleGrokOAuthCompleted(msg)

	case codexOAuthCompletedMsg:
		return m.handleCodexOAuthCompleted(msg)

	case councilChatMsg:
		return m.handleCouncilChat(msg)

	case councilChatDoneMsg:
		m.running = false
		m.directTurn = false
		if isCancelError(msg.err) {
			m = m.restoreQueueToInput()
		} else {
			m = m.reclaimSteer()
		}
		m.cancelTurn = nil
		m.councilCancel = nil
		m.councilRound = 0
		m.councilSynthesizing = false
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: msg.err.Error()})
			m.refreshViewport()
			m.vp.GotoBottom()
			m.input.Focus()
			m, cmd, _ := m.drainPending()
			return m, cmd
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
		m, cmd, _ := m.drainPending()
		return m, cmd

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
		m2.applySystemOverlay()
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

	case bashesViewMsg:
		return m.applyCommandResult(msg.res)

	case injectedWakeMsg:
		return m.startInjectedTurn()

	case workflowPanelRefreshMsg:
		if m.workflowPanel == nil {
			return m, nil
		}
		return m, tickWorkflowPanel()

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
		m.questionAsk != nil || m.planApproval != nil || m.trustDialog != nil ||
		m.quitConfirm != nil
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
	if m.cfg.Commands != nil {
		m.cmdMatches, m.cmdSelected = m.computeCommandMatches()
	}
	// Recompute @ file picker matches after every key.
	if !m.running {
		m = m.updateAtMatches()
	}

	// If a question or permission prompt was deferred while the user was typing
	// and the input is now empty, promote it to the active dialog.
	// Pending-message draining is intentionally NOT done here — it only fires
	// in the consumed-key path and in handleAgentDone to avoid a race where an
	// async event (tick, agent status) triggers an auto-submit while the user
	// is actively working in the input box.
	if m.input.Value() == "" {
		if m.pendingQuestion != nil && m.questionAsk == nil {
			m.questionAsk = newQuestionAskState(*m.pendingQuestion)
			m.pendingQuestion = nil
			m.refreshViewport()
			m.vp.GotoBottom()
		}
		if m.pendingPermission != nil && m.permPrompt == nil {
			p := m.pendingPermission
			m.pendingPermission = nil
			m.permPrompt = m.newPermissionPrompt(*p)
			m.refreshViewport()
		}
	}

	return m, tea.Batch(cmds...)
}
