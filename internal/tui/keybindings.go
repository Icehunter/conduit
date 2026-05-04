package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/keybindings"
)

// AllBindings returns the flat binding list from the active resolver, suitable
// for /keybindings display. Falls back to Defaults() when the resolver is nil.
func (m Model) AllBindings() []keybindings.Binding {
	if m.kb == nil {
		return keybindings.Defaults()
	}
	return m.kb.Bindings()
}

// activeContexts returns the keybinding context stack for the current UI
// state. "Global" is always present; one specific context is prepended
// based on which overlay or input mode is active.
func (m Model) activeContexts() []string {
	switch {
	case m.permPrompt != nil:
		return []string{"Confirmation", "Global"}
	case m.picker != nil, m.resumePrompt != nil, m.loginPrompt != nil:
		return []string{"Select", "Global"}
	case m.settingsPanel != nil:
		return []string{"Settings", "Global"}
	case m.pluginPanel != nil:
		return []string{"Plugin", "Global"}
	case m.panel != nil:
		return []string{"Global"}
	default:
		return []string{"Chat", "Global"}
	}
}

// dispatchKeybindingAction maps a resolved action ID to an existing handler.
// Returns (model, cmd, true) when the action was handled, (m, nil, false)
// when the action ID is not (yet) wired here and should fall through.
//
// "command:*" actions dispatch a slash command as if the user had typed it.
// Other IDs mirror the built-in switch in handleKey.
func (m Model) dispatchKeybindingAction(action string, _ tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	// "command:help" → run /help, "command:compact" → run /compact, etc.
	if strings.HasPrefix(action, "command:") {
		cmdName := strings.TrimPrefix(action, "command:")
		if m.cfg.Commands == nil {
			return m, nil, false
		}
		if res, ok := m.cfg.Commands.Dispatch("/" + cmdName); ok {
			m2, cmd := m.applyCommandResult(res)
			return m2, cmd, true
		}
		return m, nil, false
	}

	switch action {
	// App-level
	case "app:interrupt":
		// Same as ctrl+c: cancel turn if running, quit if idle.
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		return m2, cmd, true
	case "app:exit":
		return m, tea.Quit, true
	case "app:redraw":
		return m, tea.ClearScreen, true

	// Chat input
	// All re-dispatch cases use handleKeyBuiltins — NOT handleKey — to
	// break the recursion: handleKey runs the KB resolver which calls
	// dispatchKeybindingAction again for the same action.
	case "chat:cancel":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyEsc})
		return m2, cmd, true
	case "chat:submit":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyEnter})
		return m2, cmd, true
	case "chat:cycleMode":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		return m2, cmd, true

	case "select:next":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyDown})
		return m2, cmd, true
	case "select:previous":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyUp})
		return m2, cmd, true
	case "select:accept":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyEnter})
		return m2, cmd, true
	case "select:cancel":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyEsc})
		return m2, cmd, true

	case "confirm:yes":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'y'})
		return m2, cmd, true
	case "confirm:no":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'n'})
		return m2, cmd, true
	}

	return m, nil, false
}
