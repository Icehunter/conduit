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

	// AskUserQuestion dialog captures all keys when active.
	if m.questionAsk != nil {
		m2, cmd := m.handleQuestionKey(msg)
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
		rebuildCmd := m2.rebuildSystemCmd()
		return m2, tea.Batch(usageCmd, rebuildCmd, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} })), true

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
				m.input.Reset()
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
			m.questionAsk.reply <- nil
			m.questionAsk = nil
		}
		if m.councilCancel != nil {
			m.councilCancel()
			m.councilCancel = nil
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
			m.cancelTurn = nil
			// Commit whatever partial response was streamed so the next turn
			// has context. Keep the user message in history too.
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
		return m, tea.Quit, true

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
						for i := 0; i < targetLine; i++ {
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

	case "ctrl+y":
		// Copy the raw code from the most recent assistant code block to
		// the system clipboard via OSC 52 (works in iTerm2, kitty, WezTerm).
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == RoleAssistant {
				blocks := extractCodeBlocks(m.messages[i].Content)
				if len(blocks) > 0 {
					copyToClipboard(blocks[len(blocks)-1].code)
					m.flashMsg = "✓ Copied to clipboard"
					return m, tea.Tick(2000000000, func(_ time.Time) tea.Msg { return clearFlash{} }), true
				}
			}
		}
		m.flashMsg = "No code block found"
		return m, tea.Tick(1500000000, func(_ time.Time) tea.Msg { return clearFlash{} }), true

	case "enter":
		if m.running {
			// Queue the message for delivery after the current turn completes.
			text := strings.TrimSpace(m.input.Value())
			if text != "" && !strings.HasPrefix(text, "/") {
				m.pendingMessages = append(m.pendingMessages, text)
				m.input.Reset()
				m.flashMsg = fmt.Sprintf("[queued — %d pending]", len(m.pendingMessages))
				return m, tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
			}
			// Empty input while running: drill into running sub-agent log if any.
			if entries := subagent.Default.Snapshot(); len(entries) > 0 {
				m = m.openSubagentPanel()
				return m, tickSubagentPanel(), true
			}
			return m, nil, true
		}

		// Not running, empty input: open agent log panel if recent sub-agents exist.
		if strings.TrimSpace(m.input.Value()) == "" && len(subagent.Default.SnapshotAll()) > 0 {
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
			m.input.Reset()
			m.dismissWelcome()
			if m.cfg.Commands != nil {
				if res, ok := m.cfg.Commands.Dispatch("/" + selected.Name); ok {
					m2, cmd := m.applyCommandResult(res)
					return m2, cmd, true
				}
			}
			return m, nil, true
		}

		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil, true
		}

		// Dispatch slash commands before sending to the agent.
		if strings.HasPrefix(text, "/") {
			m.dismissWelcome()
			m.input.Reset()
			if m.cfg.Commands != nil {
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
			m.input.Reset()
			m.refreshViewport()
			m.vp.GotoBottom()
			return m, nil, true
		}

		m.dismissWelcome()
		m.input.Reset()
		// Append to history only if it differs from the last entry.
		if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != text {
			m.inputHistory = append(m.inputHistory, text)
		}
		m.historyIdx = -1
		m.historyDraft = ""
		m.messages = append(m.messages, Message{Role: RoleUser, Content: text})

		// Council mode: bypass the agent loop entirely and ask all council
		// members directly. The synthesis becomes the assistant response.
		if m.permissionMode == permissions.ModeCouncil && !usingMCPProvider {
			m.history = append(m.history, api.Message{
				Role:    "user",
				Content: []api.ContentBlock{{Type: "text", Text: m.expandPastePlaceholders(text)}},
			})
			m.running = true
			m.cancelled = false
			m.streaming = ""
			m.apiRetryStatus = ""
			m.turnStarted = time.Now()
			m.refreshViewport()
			m.vp.GotoBottom()
			return m, func() tea.Msg { return councilChatMsg{question: text} }, true
		}
		if usingMCPProvider {
			apiText := m.expandPastePlaceholders(text)
			m.pastedBlocks = nil
			m.pendingImages = nil
			m.pendingPDFs = nil
			userContent := m.userTextContent(apiText)
			m.history = append(m.history, api.Message{
				Role:    "user",
				Content: userContent,
			})
			call := commands.NewLocalDirectCallWithTool(activeMCP.Server, activeMCP.DirectTool, localChatPromptFromContent(userContent, activeMCP.Model))
			m.running = true
			m.cancelled = false
			m.streaming = ""
			m.turnAssistant = ""
			m.turnProviderKind = ""
			m.turnProvider = ""
			m.apiRetryStatus = ""
			m.turnStarted = time.Now()
			m.refreshViewport()
			m.vp.GotoBottom()
			m.turnID++
			turnID := m.turnID
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelTurn = cancel
			manager := m.cfg.MCPManager
			if manager == nil {
				m.running = false
				m.cancelTurn = nil
				m.messages = append(m.messages, Message{Role: RoleError, Content: "Local provider unavailable: MCP manager is not configured."})
				m.refreshViewport()
				return m, nil, true
			}
			input, err := json.Marshal(call.Arguments)
			if err != nil {
				m.running = false
				m.cancelTurn = nil
				m.messages = append(m.messages, Message{Role: RoleError, Content: "Local provider input invalid: " + err.Error()})
				m.refreshViewport()
				return m, nil, true
			}
			return m, tea.Batch(setWindowTitleCmd("conduit · working"), func() tea.Msg {
				return runLocalCall(ctx, manager, call, input, turnID, true)
			}), true
		}
		// Expand paste placeholders before sending to the API.
		// The textarea holds "[Pasted text #N +X lines]" tokens; the agent
		// receives the raw pasted content. After expansion, clear the map.
		apiText := m.expandPastePlaceholders(text)
		m.pastedBlocks = nil

		// Build user message content. Prepend any queued images/PDFs so Claude
		// sees attachments alongside the text. Accumulate on ctrl+v, send all on Enter.
		userContent := make([]api.ContentBlock, 0, len(m.pendingImages)+len(m.pendingPDFs)+1)
		for _, img := range m.pendingImages {
			userContent = append(userContent, api.ContentBlock{
				Type: "image",
				Source: &api.ImageSource{
					Type:      "base64",
					MediaType: img.MediaType,
					Data:      img.Data,
				},
			})
		}
		m.pendingImages = nil
		for _, pdf := range m.pendingPDFs {
			userContent = append(userContent, api.ContentBlock{
				Type: "document",
				Source: &api.ImageSource{
					Type:      "base64",
					MediaType: pdf.MediaType,
					Data:      pdf.Data,
				},
			})
		}
		m.pendingPDFs = nil
		userContent = append(userContent, m.atMentionContent(apiText)...)
		userContent = append(userContent, api.ContentBlock{Type: "text", Text: apiText})
		m.history = append(m.history, api.Message{
			Role:    "user",
			Content: userContent,
		})
		m.running = true
		m.cancelled = false
		m.streaming = ""
		m.captureTurnProvider()
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

		return m, tea.Batch(setWindowTitleCmd("conduit · working"), func() tea.Msg {
			var usage api.Usage
			newHist, err := m.cfg.Loop.Run(ctx, histCopy, func(ev agent.LoopEvent) {
				if ev.Type == agent.EventUsage {
					usage = accumulateUsage(usage, ev.Usage)
				}
				prog.Send(agentMsg{event: ev})
			})
			return agentDoneMsg{turnID: turnID, history: newHist, err: err, cancelled: ctx.Err() != nil, usage: usage}
		}), true
	}
	return m, nil, false
}
