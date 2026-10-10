package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/attach"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/subagent"
)

// handleKeyBuiltins is the built-in key handler. It never consults the
// keybinding resolver, which means dispatchKeybindingAction can safely
// call it for synthetic re-dispatches without triggering infinite recursion.
func (m Model) handleKeyBuiltins(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	// Trust dialog captures all keys before anything else.
	if m.trustDialog != nil {
		m2, cmd := m.handleTrustKey(msg)
		return m2, cmd, true
	}

	// Viewport scrollback. Plain Up/Down/PgUp/PgDn are owned by the chat
	// input (history navigation, multi-line cursor, textarea paging).
	// Users still need a way to scroll the chat transcript without
	// reaching for the mouse — so reserve Shift+Up/Shift+Down for
	// line-by-line scroll and Shift+PgUp/Shift+PgDn for page scroll.
	// Modern terminals running the Kitty keyboard protocol report
	// these as distinct keys; legacy terminals collapse Shift+arrow
	// onto plain arrow, which falls through to the existing handlers
	// below — so this is purely additive on capable terminals.
	switch msg.String() {
	case "shift+up":
		m.vp.ScrollUp(1)
		return m, nil, true
	case "shift+down":
		m.vp.ScrollDown(1)
		return m, nil, true
	case "shift+pgup", "pgup":
		m.vp.PageUp()
		return m, nil, true
	case "shift+pgdown", "pgdown":
		m.vp.PageDown()
		return m, nil, true
	case "ctrl+home":
		m.vp.GotoTop()
		return m, nil, true
	case "ctrl+end":
		// Pinned to the bottom again, so streaming output follows.
		m.vp.GotoBottom()
		return m, nil, true
	}

	switch msg.String() {
	case "up":
		if m.commandPickerActive() {
			if m.cmdSelected > 0 {
				m.cmdSelected--
			}
			return m, nil, true
		}
		if len(m.atMatches) > 0 {
			if m.atSelected > 0 {
				m.atSelected--
			}
			return m, nil, true
		}
		// History navigation — always consume UP so it never falls through
		// to the viewport keymap. Scroll via trackpad/wheel (MouseWheelMsg)
		// or Shift+Up/Down.
		if !m.running {
			if len(m.inputHistory) > 0 {
				if m.historyIdx == -1 {
					m.historyDraft = m.input.Value()
					m.historyIdx = len(m.inputHistory) - 1
				} else if m.historyIdx > 0 {
					m.historyIdx--
				}
				m.input.SetValue(m.inputHistory[m.historyIdx])
				m.input.CursorEnd()
			}
			return m, nil, true
		}

	case "down":
		if m.commandPickerActive() {
			if m.cmdSelected < len(m.cmdMatches)-1 {
				m.cmdSelected++
			}
			return m, nil, true
		}
		if len(m.atMatches) > 0 {
			if m.atSelected < len(m.atMatches)-1 {
				m.atSelected++
			}
			return m, nil, true
		}
		// History forward — always consume DOWN too.
		if !m.running {
			if m.historyIdx != -1 {
				if m.historyIdx < len(m.inputHistory)-1 {
					m.historyIdx++
					m.input.SetValue(m.inputHistory[m.historyIdx])
				} else {
					m.historyIdx = -1
					m.input.SetValue(m.historyDraft)
				}
				m.input.CursorEnd()
			}
			return m, nil, true
		}

	case "shift+tab":
		// Cycle: default → plan → council → acceptEdits → bypassPermissions → default.
		switch m.permissionMode {
		case "", permissions.ModeDefault:
			m.permissionMode = permissions.ModePlan
		case permissions.ModePlan:
			m.permissionMode = permissions.ModeCouncil
		case permissions.ModeCouncil:
			m.permissionMode = permissions.ModeAcceptEdits
		case permissions.ModeAcceptEdits:
			m.permissionMode = permissions.ModeBypassPermissions
		default:
			m.permissionMode = permissions.ModeDefault
		}
		m.applyPermissionMode(m.permissionMode)
		switch m.permissionMode {
		case permissions.ModePlan:
			m.flashMsg = "⏸ plan mode on (shift+tab to cycle)"
		case permissions.ModeCouncil:
			m.flashMsg = "⚖ council mode on (shift+tab to cycle)"
		case permissions.ModeAcceptEdits:
			m.flashMsg = "⏵⏵ accept edits on (shift+tab to cycle)"
		case permissions.ModeBypassPermissions:
			m.flashMsg = "⏵⏵ auto mode on (shift+tab to cycle)"
		default:
			m.flashMsg = "default mode (shift+tab to cycle)"
		}
		m2, usageCmd := m.startPlanUsageFetch()
		m2.applySystemOverlay()
		return m2, tea.Batch(usageCmd, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} })), true

	case "tab", "esc":
		// Clear pending images and paste placeholders on Esc.
		if msg.String() == "esc" {
			if len(m.pendingImages) > 0 || len(m.pendingPDFs) > 0 || len(m.pastedBlocks) > 0 {
				n := len(m.pendingImages) + len(m.pendingPDFs)
				m.pendingImages = nil
				m.pendingPDFs = nil
				m.pastedBlocks = nil
				m.input.SetValue(rePasteToken.ReplaceAllString(m.input.Value(), ""))
				if n > 0 {
					m.flashMsg = fmt.Sprintf("%d attachment(s) and paste(s) cleared.", n)
				} else {
					m.flashMsg = "Paste(s) cleared."
				}
				return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true
			}
		}
		if len(m.atMatches) > 0 {
			if msg.String() == "tab" || msg.String() == "esc" {
				if msg.String() == "tab" {
					m = m.acceptAtMatch()
				} else {
					m.atMatches = nil
					m.atSelected = 0
				}
				return m, nil, true
			}
		}
		if m.commandPickerActive() {
			if msg.String() == "tab" {
				// Tab: complete to the command name with trailing space, close picker.
				if len(m.cmdMatches) > 0 {
					m.input.SetValue("/" + m.cmdMatches[m.cmdSelected].Name + " ")
					m.input.CursorEnd()
				}
			} else {
				m = m.resetInput()
			}
			m.cmdMatches = nil
			m.cmdSelected = 0
			return m, nil, true
		}
		if msg.String() == "tab" && !m.running && m.cfg.Commands != nil {
			// Fallback tab completion when picker isn't open.
			text := m.input.Value()
			if strings.HasPrefix(text, "/") && !strings.Contains(text, " ") {
				completed := m.tabComplete(text)
				if completed != text {
					m.input.SetValue(completed)
					m.input.CursorEnd()
				}
			}
			return m, nil, true
		}

	case "ctrl+c":
		if m.questionAsk != nil {
			// Non-blocking send: if the reader goroutine already exited via
			// ctx.Done() the buffered slot may be full or gone — don't block
			// the Bubble Tea event loop.
			select {
			case m.questionAsk.reply <- nil:
			default:
			}
			m.questionAsk = nil
			m.pendingQuestion = nil // goroutine unblocks via ctx.Done()
		}
		if m.councilCancel != nil {
			m.councilCancel()
			m.councilCancel = nil
			m.directTurn = false
			m.councilRound = 0
			m.councilSynthesizing = false
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Council cancelled."})
			m.refreshViewport()
			m.input.Focus()
			return m, nil, true
		}
		if m.running && m.cancelTurn != nil {
			m.cancelTurn()
			m.cancelled = true
			m.running = false
			m.directTurn = false
			m.cancelTurn = nil
			m = m.restoreQueueToInput()
			m.shellID = ""            // a cancelled shell command no longer owns the turn
			m.pendingQuestion = nil   // question from the cancelled turn; goroutine unblocks via ctx.Done()
			m.pendingPermission = nil // same — permission goroutine unblocks via ctx.Done()
			// Commit whatever partial response was streamed so the next turn
			// has context. Keep the user message in history too.
			m.flushThinking()
			if m.streaming != "" {
				m.messages = append(m.messages, m.assistantMessage(m.streaming))
				m.history = append(m.history, api.Message{
					Role:    "assistant",
					Content: []api.ContentBlock{{Type: "text", Text: m.streaming}},
				})
				m.streaming = ""
			}
			for i := range m.messages {
				if m.messages[i].Role == RoleTool && m.messages[i].Content == "running…" {
					m.messages[i].Content = "interrupted."
					m.messages[i].ToolError = true
				}
			}
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Interrupted."})
			m.refreshViewport()
			m.input.Focus()
			return m, nil, true
		}
		m.quitConfirm = &quitConfirmState{selected: 1}
		m.refreshViewport()
		return m, nil, true

	case "ctrl+v":
		// Try image first, then PDF, then fall through to textarea text paste.
		img, imgErr := attach.ReadClipboardImage()
		if imgErr == nil && img != nil {
			m.pendingImages = append(m.pendingImages, img)
			n := len(m.pendingImages) + len(m.pendingPDFs)
			m.flashMsg = fmt.Sprintf("📎 %d attachment(s)  (ctrl+v for more · Enter to send · Esc to clear)", n)
			return m, tea.Tick(5*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
		}
		if errors.Is(imgErr, attach.ErrNotSupported) {
			return m, nil, false
		}
		// No image — try PDF.
		pdf, pdfErr := attach.ReadClipboardPDF()
		if pdfErr == nil && pdf != nil {
			m.pendingPDFs = append(m.pendingPDFs, pdf)
			n := len(m.pendingImages) + len(m.pendingPDFs)
			m.flashMsg = fmt.Sprintf("📎 %d attachment(s)  (ctrl+v for more · Enter to send · Esc to clear)", n)
			return m, tea.Tick(5*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
		}
		// Clipboard has text — fall through to textarea for normal paste.
		return m, nil, false

	case "backspace":
		// If the cursor is immediately after a paste placeholder token, delete
		// the entire token in one keystroke (mirroring how CC handles it).
		if len(m.pastedBlocks) > 0 {
			val := m.input.Value()
			col := m.input.Column()
			// Determine the rune position of the cursor within the full value
			// by walking lines up to the current line+col.
			line := m.input.Line()
			pos := 0
			for i, l := range strings.Split(val, "\n") {
				if i == line {
					pos += col
					break
				}
				pos += len(l) + 1 // +1 for the \n
			}
			// Look for any paste token ending exactly at pos.
			prefix := val[:pos]
			if strings.HasSuffix(prefix, "]") {
				loc := rePasteToken.FindStringIndex(prefix)
				if loc != nil && loc[1] == len(prefix) {
					// Found a token ending at the cursor — extract its seq#,
					// delete it from the input, and remove from pastedBlocks.
					token := prefix[loc[0]:]
					if sub := rePasteToken.FindStringSubmatch(token); len(sub) == 2 {
						seq, _ := strconv.Atoi(sub[1])
						delete(m.pastedBlocks, seq)
					}
					newVal := val[:loc[0]] + val[pos:]
					m.input.SetValue(newVal)
					// SetValue leaves cursor at the end. Reposition to loc[0]
					// (where the token was) when there's text after it.
					if val[pos:] != "" {
						prefix2 := newVal[:loc[0]]
						targetLine := strings.Count(prefix2, "\n")
						lines2 := strings.Split(prefix2, "\n")
						targetCol := len(lines2[len(lines2)-1])
						m.input.CursorStart()
						for range targetLine {
							m.input.CursorDown()
						}
						m.input.SetCursorColumn(targetCol)
					}
					return m, nil, true
				}
			}
		}

	case "ctrl+\\":
		// Open mode picker — direct selection without Shift+Tab cycling.
		items := []pickerItem{
			{Value: "default", Label: "Default (ask for edits)"},
			{Value: "plan", Label: "Plan (read-only)"},
			{Value: "council", Label: "Council (multi-model plan)"},
			{Value: "acceptEdits", Label: "Accept Edits"},
			{Value: "bypassPermissions", Label: "Auto (bypass all)"},
		}
		current := string(m.permissionMode)
		m.picker = &pickerState{
			kind:     "mode",
			items:    items,
			current:  current,
			selected: selectedPickerIndex(items, current),
		}
		m.refreshViewport()
		return m, nil, true

	case "ctrl+o":
		m.verboseMode = !m.verboseMode
		if m.verboseMode {
			m.flashMsg = "verbose mode on (ctrl+o to toggle)"
		} else {
			m.flashMsg = "compact mode on (ctrl+o to toggle)"
		}
		m.refreshViewport()
		return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true

	case "ctrl+u":
		m.unlimitedTurns = !m.unlimitedTurns
		if m.unlimitedTurns {
			m.cfg.Loop.SetMaxTurns(0)
			m.flashMsg = "∞ turn cap off (ctrl+u to restore)"
		} else {
			m.cfg.Loop.SetMaxTurns(agent.DefaultMainMaxTurns)
			m.flashMsg = fmt.Sprintf("turn cap on: %d (ctrl+u to toggle)", agent.DefaultMainMaxTurns)
		}
		return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true

	case "ctrl+t":
		// Always the todo strip. Team mode has its own key (ctrl+g) so
		// starting a team session doesn't silently steal this binding —
		// it used to, which left the todo strip stuck whenever team mode
		// was active because there was no other way to toggle it back.
		m.todoStripHidden = !m.todoStripHidden
		m = m.applyLayout()
		if m.todoStripHidden {
			m.flashMsg = "todo strip hidden (ctrl+t to show)"
		} else {
			m.flashMsg = "todo strip visible (ctrl+t to hide)"
		}
		return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true

	case "ctrl+g":
		if !m.teamActive {
			return m, nil, true
		}
		m.teammateStripHidden = !m.teammateStripHidden
		m = m.applyLayout()
		if m.teammateStripHidden {
			m.flashMsg = "agent strip hidden (ctrl+g to show)"
		} else {
			m.flashMsg = "agent strip visible (ctrl+g to hide)"
		}
		return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true

	case " ": // Space toggles expansion of the most recent expandable tool result
		if m.running || m.input.Focused() {
			// Don't handle space during input or when running
			return m, nil, false
		}
		// Find and toggle the most recent expandable tool result
		for i := len(m.messages) - 1; i >= 0; i-- {
			msg := &m.messages[i]
			if msg.Role == RoleTool && msg.Content != "" && msg.Content != "running…" {
				if canCollapse(len(outputLines(msg.Content))) {
					msg.ToolExpanded = !msg.ToolExpanded
					if msg.ToolExpanded {
						m.flashMsg = "▼ expanded"
					} else {
						m.flashMsg = "▶ collapsed"
					}
					m.refreshViewport()
					return m, tea.Tick(1000*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true
				}
			}
		}
		return m, nil, false

	case editorKey:
		if m.running {
			m.flashMsg = "Editor unavailable while a turn is running"
			return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true
		}
		return m, prepareEditorCmd(m.input.Value()), true

	case "ctrl+y":
		// Copy the raw code from the most recent assistant code block to
		// the system clipboard (native tool plus OSC 52).
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == RoleAssistant {
				blocks := extractCodeBlocks(m.messages[i].Content)
				if len(blocks) > 0 {
					return m, copyCmd(blocks[len(blocks)-1].code, "code block"), true
				}
			}
		}
		m.flashMsg = "No code block found"
		return m, tea.Tick(1500000000, func(_ time.Time) tea.Msg { return clearFlash{} }), true

	case "enter":
		if m.running {
			text := strings.TrimSpace(m.input.Value())
			hasAttach := m.hasPendingAttachments()
			// /btw is a side question on a copy of the history; it never
			// touches the running turn, so it is the one slash command that
			// works mid-turn.
			if strings.HasPrefix(text, "/btw ") && m.cfg.Commands != nil {
				if res, ok := m.cfg.Commands.Dispatch(text); ok && res.Type == "btw" {
					m = m.resetInput()
					m2, cmd := m.applyBtwResult(res)
					return m2, cmd, true
				}
			}
			if _, isShell := shellCommand(text); isShell || m.shellID != "" || m.directTurn {
				// A shell, local-MCP or council turn has no agent loop to steer, and "!cmd" must
				// not reach the model as text: queue until the turn ends.
				if (text != "" || hasAttach) && !strings.HasPrefix(text, "/") {
					m = m.resetInput()
					var q queuedMessage
					m, q = m.takeQueuedMessage(text)
					m.pendingMessages = append(m.pendingMessages, q)
					m.flashMsg = fmt.Sprintf("[queued — %d pending]", len(m.pendingMessages))
					return m, tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
				}
			}
			if (text != "" || hasAttach) && !strings.HasPrefix(text, "/") {
				m = m.resetInput()
				if m.cfg.SteerContent != nil {
					// Inject mid-turn: the loop will append this as a user message
					// between the current tool batch and the next API call, so the
					// model steers without the current turn being interrupted.
					// Attachments ride along — otherwise they'd sit in the pending
					// queue and silently attach to the next unrelated message.
					var content []api.ContentBlock
					var attachments int
					images, pdfs := m.pendingImages, m.pendingPDFs
					raw := m.expandPastePlaceholders(text)
					var typed int
					m, content, attachments, typed = m.takeUserContentIdx(text)
					if m.promptHooksActive() {
						// UserPromptSubmit hooks gate steering like any prompt;
						// the verdict arrives as a promptScreenedMsg.
						m.flashMsg = "[screening prompt…]"
						return m, m.screenPromptCmd(routeSteer, raw, content, typed+1, images, pdfs), true
					}
					m.cfg.SteerContent(content)
					m.messages = append(m.messages, userRow(text, images, pdfs))
					m.refreshViewport()
					m.vp.GotoBottom()
					m.flashMsg = "[steering → next tool round]"
					if attachments > 0 {
						m.flashMsg = fmt.Sprintf("[steering → next tool round · %d attachment(s)]", attachments)
					}
				} else {
					// Fallback: queue for delivery after the turn ends, with the
					// attachments pasted alongside it.
					var q queuedMessage
					m, q = m.takeQueuedMessage(text)
					m.pendingMessages = append(m.pendingMessages, q)
					m.flashMsg = fmt.Sprintf("[queued — %d pending]", len(m.pendingMessages))
				}
				return m, tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
			}
			// Empty input while running: drill into running sub-agent log if any.
			if entries := subagent.Default.Snapshot(); len(entries) > 0 {
				m = m.openSubagentPanel()
				return m, tickSubagentPanel(), true
			}
			return m, nil, true
		}

		// Not running, empty input: open agent log panel if recent sub-agents
		// exist. Pending attachments make an empty input a send, not a probe.
		if strings.TrimSpace(m.input.Value()) == "" && !m.hasPendingAttachments() && len(subagent.Default.SnapshotAll()) > 0 {
			m = m.openSubagentPanel()
			return m, tickSubagentPanel(), true
		}

		// If the @ file picker is open, accept selected path.
		if len(m.atMatches) > 0 {
			m = m.acceptAtMatch()
			return m, nil, true
		}

		// If the command picker is open, dispatch the selected command immediately.
		if len(m.cmdMatches) > 0 {
			selected := m.cmdMatches[m.cmdSelected]
			m.cmdMatches = nil
			m.cmdSelected = 0
			m = m.resetInput()
			m.dismissWelcome()
			if m.cfg.Commands != nil {
				m.slashInput = "/" + selected.Name
				if res, ok := m.cfg.Commands.Dispatch("/" + selected.Name); ok {
					m2, cmd := m.applyCommandResult(res)
					return m2, cmd, true
				}
			}
			return m, nil, true
		}

		text := strings.TrimSpace(m.input.Value())
		if text == "" && !m.hasPendingAttachments() {
			return m, nil, true
		}

		// Synthetic todo-continue nudge: show a terse system notice instead
		// of the raw instruction as a "You" message, then strip the sentinel
		// so the model receives only the instruction.
		isAutoPrompt := strings.HasPrefix(text, autoPromptPrefix)
		if isAutoPrompt {
			text = strings.TrimPrefix(text, autoPromptPrefix)
		} else {
			m.todoAutoContinues = 0
		}

		// Shell mode: "!command" runs locally instead of going to the model.
		if shellCmd, ok := shellCommand(text); ok && !isAutoPrompt {
			if shellCmd == "" {
				m.flashMsg = "Type a command after ! to run it"
				return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true
			}
			m.dismissWelcome()
			m = m.resetInput()
			m.rememberInput(text)
			m2, cmd := m.startShellMode(shellCmd)
			return m2, cmd, true
		}

		// Dispatch slash commands before sending to the agent.
		if strings.HasPrefix(text, "/") {
			m.dismissWelcome()
			m = m.resetInput()
			if m.cfg.Commands != nil {
				m.slashInput = text
				if res, ok := m.cfg.Commands.Dispatch(text); ok {
					m2, cmd := m.applyCommandResult(res)
					return m2, cmd, true
				}
			}
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Unknown command: %s (try /help)", text)})
			m.refreshViewport()
			return m, nil, true
		}

		// Reject messages when not authenticated.
		activeMCP, usingMCPProvider := m.activeMCPProvider()
		if m.noAuth && !usingMCPProvider {
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: "Not logged in. Use /login to sign in first.",
			})
			m = m.resetInput()
			m.refreshViewport()
			m.vp.GotoBottom()
			return m, nil, true
		}

		// Council and local MCP prompts bypass the agent loop, so their
		// UserPromptSubmit hooks run here, before anything is sent. The input
		// stays as typed until the verdict arrives and the submit is replayed,
		// so a block leaves the text and its attachments where they were.
		// Triviality is judged on what was typed: a hook's rewrite must not
		// route the replayed Enter differently from the one that was screened.
		if usingMCPProvider && text == "" {
			// Nothing to send: a local provider takes text only, so the
			// pending attachments alone have no message to ride on.
			m.flashMsg = fmt.Sprintf("images not supported by %s; still attached", activeMCP.Model)
			return m, tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
		}
		councilTrivial := isCouncilTrivial(text)
		var hookContext string
		if routeScreened := usingMCPProvider || (m.permissionMode == permissions.ModeCouncil && !councilTrivial); routeScreened && !isAutoPrompt && m.promptHooksActive() {
			expanded := m.expandPastePlaceholders(text)
			if sp := m.screened; sp != nil && sp.raw == expanded {
				m.screened = nil
				text = sp.text
				hookContext = sp.hookContext
			} else {
				m.screened = nil
				if m.screening {
					m.flashMsg = "[waiting for UserPromptSubmit hooks…]"
					return m, tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
				}
				route := routeCouncil
				if usingMCPProvider {
					route = routeMCP
				}
				m.screening = true
				return m, m.screenPromptCmd(route, expanded, []api.ContentBlock{{Type: "text", Text: expanded}}, 1, nil, nil), true
			}
		}

		m.dismissWelcome()
		m.todoStripHidden = true // collapse the task strip on send so the conversation is visible
		m = m.resetInput()
		if isAutoPrompt {
			// Don't pollute input history or show raw instruction text.
			m.promptRow = 0
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "↻ Continuing unfinished tasks…"})
		} else {
			if text != "" {
				m.rememberInput(text)
			}
			m.promptRow = len(m.messages) + 1
			row := userRow(text, nil, nil)
			if !usingMCPProvider {
				// A local provider drops attachments (they stay pending), so
				// its row must not claim they were sent.
				row = m.pendingUserRow(text)
			}
			m.messages = append(m.messages, row)
		}

		// Council mode: bypass the agent loop entirely and ask all council
		// members directly. The synthesis becomes the assistant response.
		// Trivial inputs (greetings, single-word acks) fall through to the
		// normal agent loop to avoid unnecessary latency and cost.
		if m.permissionMode == permissions.ModeCouncil && !usingMCPProvider && !councilTrivial {
			// Attachments ride in the recorded turn; council members are
			// asked the text question only.
			var councilContent []api.ContentBlock
			m, councilContent = m.takeAttachmentBlocks()
			councilContent = append(councilContent, api.ContentBlock{Type: "text", Text: m.expandPastePlaceholders(text)})
			if hookContext != "" {
				councilContent = append(councilContent, api.ContentBlock{Type: "text", Text: hookContext})
			}
			m.history = append(m.history, api.Message{Role: "user", Content: councilContent})
			m.running = true
			m.directTurn = true
			m.cancelled = false
			m.streaming = ""
			m.thinking = ""
			m.apiRetryStatus = ""
			m.turnStarted = time.Now()
			m.refreshViewport()
			m.vp.GotoBottom()
			return m, func() tea.Msg { return councilChatMsg{question: text, hookContext: hookContext} }, true
		}
		if usingMCPProvider {
			apiText := m.expandPastePlaceholders(text)
			m.pastedBlocks = nil
			var noticeCmd tea.Cmd
			if m.hasPendingAttachments() {
				noticeCmd = tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })
				// Dropping them silently would lose the user's files; they
				// stay pending (badge visible) for a provider that takes them.
				m.flashMsg = fmt.Sprintf("images not supported by %s; still attached", activeMCP.Model)
			}
			userContent := m.userTextContent(apiText)
			if hookContext != "" {
				userContent = append(userContent, api.ContentBlock{Type: "text", Text: hookContext})
			}
			m.history = append(m.history, api.Message{
				Role:    "user",
				Content: userContent,
			})
			call := commands.NewLocalDirectCallWithTool(activeMCP.Server, activeMCP.DirectTool, localChatPromptFromContent(userContent, activeMCP.Model))
			m.running = true
			m.directTurn = true
			m.cancelled = false
			m.streaming = ""
			m.thinking = ""
			m.turnAssistant = ""
			m.turnProviderKind = ""
			m.turnProvider = ""
			m.apiRetryStatus = ""
			m.turnStarted = time.Now()
			m.refreshViewport()
			m.vp.GotoBottom()
			m.turnID++
			m.attemptSet = false
			turnID := m.turnID
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelTurn = cancel
			manager := m.cfg.MCPManager
			if manager == nil {
				m.running = false
				m.directTurn = false
				m.cancelTurn = nil
				m.messages = append(m.messages, Message{Role: RoleError, Content: "Local provider unavailable: MCP manager is not configured."})
				m.refreshViewport()
				return m, nil, true
			}
			input, err := json.Marshal(call.Arguments)
			if err != nil {
				m.running = false
				m.directTurn = false
				m.cancelTurn = nil
				m.messages = append(m.messages, Message{Role: RoleError, Content: "Local provider input invalid: " + err.Error()})
				m.refreshViewport()
				return m, nil, true
			}
			return m, tea.Batch(setWindowTitleCmd("conduit · working"), noticeCmd, func() tea.Msg {
				return runLocalCall(ctx, manager, call, input, turnID, true)
			}), true
		}
		// Build the user message: paste placeholders expanded, queued
		// images/PDFs prepended (accumulated on ctrl+v, sent on Enter), and
		// @-mentions resolved.
		var userContent []api.ContentBlock
		rawPrompt := agent.UserPrompt{Raw: m.expandPastePlaceholders(text), Typed: true}
		if isAutoPrompt {
			rawPrompt = agent.UserPrompt{} // a nudge conduit wrote, not a prompt the user typed
		}
		var typedIdx int
		m, userContent, _, typedIdx = m.takeUserContentIdx(text)
		rawPrompt.Block = typedIdx + 1
		m.history = append(m.history, api.Message{
			Role:    "user",
			Content: userContent,
		})
		m.running = true
		m.cancelled = false
		m.streaming = ""
		m.thinking = ""
		m.captureTurnProvider()
		m.apiRetryStatus = ""
		m.turnStarted = time.Now()
		m.refreshViewport()
		m.vp.GotoBottom()

		m.turnID++

		m.attemptSet = false
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelTurn = cancel
		return m, m.runLoopCmd(ctx, m.turnID, rawPrompt), true
	}
	return m, nil, false
}

// rememberInput adds a submitted prompt to the up-arrow history (skipping a
// repeat of the last entry) and queues it for persistence.
func (m *Model) rememberInput(text string) {
	if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != text {
		m.inputHistory = append(m.inputHistory, text)
		if len(m.inputHistory) > inputHistoryMax {
			m.inputHistory = m.inputHistory[len(m.inputHistory)-inputHistoryMax:]
		}
		if m.cfg.PersistInput != nil {
			m.cfg.PersistInput(text)
		}
	}
	m.historyIdx = -1
	m.historyDraft = ""
}
