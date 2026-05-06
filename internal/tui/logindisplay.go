package tui

import tea "charm.land/bubbletea/v2"

// tuiLoginDisplay implements auth.LoginDisplay by sending inline TUI messages
// instead of printing to stderr.
type tuiLoginDisplay struct {
	prog *tea.Program
}

func (d *tuiLoginDisplay) Show(automatic, manual string) {
	d.prog.Send(loginURLMsg{automatic: automatic, manual: manual})
}

func (d *tuiLoginDisplay) BrowserOpenFailed(err error) {
	d.prog.Send(loginBrowserFailMsg{err: err})
}
