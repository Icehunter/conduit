package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// permissionPromptState holds the active permission prompt data.
type permissionPromptState struct {
	toolName  string
	toolInput string
	reply     chan<- permissionReply
	// subAgentLabel is non-empty when this prompt was bubbled up from a
	// sub-agent rather than a direct tool call of the root loop — changes
	// the option list (see permissionOptionsFor) and header text.
	subAgentLabel string
	selected      int // see permissionOptionsFor for what each index means

	// guardFirstKey swallows the first key after the dialog opens so a
	// keystroke already in flight (user was typing when the prompt appeared)
	// cannot auto-accept the tool. Esc/ctrl+c pass through immediately.
	guardFirstKey bool

	// protected is true for a write to a protected path (see
	// permissions.Gate.IsProtectedWrite); "Always allow" is not offered.
	protected bool
	// onceOnly also hides "Always allow", for prompts whose approval is a
	// single user-typed action (shell mode) rather than a tool call.
	onceOnly bool
}

// permChoice is what a permission-modal option does when selected.
type permChoice int

const (
	permAllowOnce permChoice = iota
	permAlwaysAllow
	permSwitchToAuto
	permDeny
)

func (c permChoice) label() string {
	switch c {
	case permAllowOnce:
		return "Allow once"
	case permAlwaysAllow:
		return "Always allow"
	case permSwitchToAuto:
		return "Switch subagent to auto mode"
	default:
		return "Deny"
	}
}

// permissionChoicesFor returns the option list for the modal. A direct
// (root-loop) prompt offers Allow once / Always allow / Deny; a prompt
// bubbled up from a sub-agent instead offers Allow once / Switch subagent to
// auto mode / Deny — "always allow" (a persisted rule) doesn't fit a one-off
// sub-agent run the way a session-scoped mode switch does. A write to a
// protected path never offers "Always allow": the gate prompts for it every
// time, so a persisted rule would promise something it cannot deliver.
func permissionChoicesFor(subAgentLabel string, protected bool) []permChoice {
	switch {
	case subAgentLabel != "":
		return []permChoice{permAllowOnce, permSwitchToAuto, permDeny}
	case protected:
		return []permChoice{permAllowOnce, permDeny}
	}
	return []permChoice{permAllowOnce, permAlwaysAllow, permDeny}
}

// newPermissionPrompt builds the modal state for a permission request.
func (m Model) newPermissionPrompt(req permissionAskMsg) *permissionPromptState {
	return &permissionPromptState{
		toolName:      req.toolName,
		toolInput:     req.toolInput,
		subAgentLabel: req.subAgentLabel,
		reply:         req.reply,
		protected:     m.cfg.Gate != nil && m.cfg.Gate.IsProtectedWrite(req.toolName, req.toolInput),
		guardFirstKey: true,
	}
}

// handlePermissionKey routes keys to the permission modal.
func (m Model) handlePermissionKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.permPrompt
	// Focus guard: swallow the first key after the dialog opens so a
	// keystroke already in flight (e.g. the Enter that was mid-press when
	// the agent triggered a permission check) cannot auto-accept the tool.
	// Esc and ctrl+c bypass the guard so the user can dismiss immediately.
	if p.guardFirstKey {
		p.guardFirstKey = false
		key := msg.String()
		if key != "esc" && key != "ctrl+c" {
			m.permPrompt = p
			return m, nil
		}
	}
	choices := permissionChoicesFor(p.subAgentLabel, p.protected || p.onceOnly)
	switch msg.String() {
	case "up", "left":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "right":
		if p.selected < len(choices)-1 {
			p.selected++
		}
	case "enter":
		choice := choices[min(p.selected, len(choices)-1)]
		reply := permissionReply{
			allow:        choice != permDeny,
			alwaysAllow:  choice == permAlwaysAllow,
			switchToAuto: choice == permSwitchToAuto,
		}
		m.permPrompt = nil
		m.refreshViewport()
		prog := *m.cfg.Program
		return m, func() tea.Msg {
			p.reply <- reply
			_ = prog
			return nil
		}
	case "ctrl+c", "esc":
		reply := permissionReply{allow: false}
		m.permPrompt = nil
		m.refreshViewport()
		return m, func() tea.Msg {
			p.reply <- reply
			return nil
		}
	}
	m.permPrompt = p
	return m, nil
}

// renderPermissionPrompt renders the permission modal overlay.
func (m Model) renderPermissionPrompt() string {
	p := m.permPrompt
	if p == nil {
		return ""
	}

	var sb strings.Builder
	header := "Permission required"
	if p.subAgentLabel != "" {
		header = fmt.Sprintf("Subagent %q wants to…", p.subAgentLabel)
	}
	sb.WriteString(styleStatusAccent.Render(header) + "\n\n")

	// Tool + input.
	label := p.toolName
	if isBashTool(p.toolName) && p.toolInput != "" {
		// Show the whole command, wrapped, so nothing is hidden from the
		// person approving it.
		// A sub-agent may run in another worktree, where a `cd <our cwd>` is
		// not redundant: show its command exactly as sent.
		cwd := currentDisplayCwd()
		if p.subAgentLabel != "" {
			cwd = ""
		}
		cmd := displayBashCommand(p.toolInput, cwd)
		width := floatingInnerWidth(m.width, floatingModalSpec) - 4
		sb.WriteString(stylePickerItem.Render(label) + "\n")
		for _, ln := range wrapBashCommand(cmd, width) {
			sb.WriteString("  " + ln + "\n")
		}
		sb.WriteString("\n")
		label = ""
	} else if p.toolInput != "" {
		short := p.toolInput
		maxLen := floatingInnerWidth(m.width, floatingModalSpec) - 4
		if maxLen > 10 && len(short) > maxLen {
			short = short[:maxLen] + "…"
		}
		label += "(" + short + ")"
	}
	if label != "" {
		sb.WriteString(stylePickerItem.Render(label) + "\n\n")
	}

	for i, choice := range permissionChoicesFor(p.subAgentLabel, p.protected || p.onceOnly) {
		opt := choice.label()
		prefix := "  "
		var rendered string
		if i == p.selected {
			rendered = stylePickerItemSelected.Render("❯ " + opt)
		} else {
			rendered = stylePickerItem.Render("  " + opt)
		}
		sb.WriteString(prefix + rendered + "\n")
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter select · Esc deny"))

	return sb.String()
}
