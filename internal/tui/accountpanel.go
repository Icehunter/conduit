package tui

// Accounts tab — embedded inside the settings panel (Status·Config·Stats·Usage·Accounts).
// /account opens the settings panel on this tab directly.
//
// Views:
//   List   — all saved accounts; "● active", "✗ no token"; + Add account
//   Detail — per-account action menu (Switch / Re-login / Remove / Delete / Back)
//
// Navigation:
//   ↑↓/jk   navigate list / actions
//   Enter    select
//   Esc      detail → list; list → close panel
//   ←/→      switch to adjacent tab (handled by settings panel)

import (
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/secure"
)

// ── Types ────────────────────────────────────────────────────────────────────

type accountPanelView int

const (
	accountViewList   accountPanelView = 0
	accountViewDetail accountPanelView = 1
)

type accountItem struct {
	email    string
	addedAt  time.Time
	isActive bool
	hasToken bool
}

type accountAction struct {
	label  string
	id     string
	danger bool
}

type accountPanelState struct {
	view     accountPanelView
	selected int // cursor: 0..len(accounts) inclusive (last = "+ Add")

	accounts    []accountItem
	loadErr     string
	detailEmail string
	actions     []accountAction
	actionIdx   int
}

// ── Messages ─────────────────────────────────────────────────────────────────

// accountSwitchedMsg drives the live credential reload after switching.
type accountSwitchedMsg struct{ email string }

// commands_loginMsg opens the login picker from "+ Add account".
type commands_loginMsg struct{}

// ── Constructor ──────────────────────────────────────────────────────────────

func newAccountPanel() *accountPanelState {
	p := &accountPanelState{}
	p.refresh()
	return p
}

func (p *accountPanelState) refresh() {
	store, err := auth.LoadAccountStore()
	if err != nil {
		p.loadErr = err.Error()
		return
	}
	p.loadErr = ""
	s := secure.NewDefault()

	emails := make([]string, 0, len(store.Accounts))
	for e := range store.Accounts {
		emails = append(emails, e)
	}
	// Active account first, then alphabetical.
	sort.Slice(emails, func(i, j int) bool {
		ai := emails[i] == store.Active
		aj := emails[j] == store.Active
		if ai != aj {
			return ai
		}
		return emails[i] < emails[j]
	})

	p.accounts = make([]accountItem, 0, len(emails))
	for _, e := range emails {
		entry := store.Accounts[e]
		_, tokenErr := auth.LoadForEmail(s, e)
		p.accounts = append(p.accounts, accountItem{
			email:    e,
			addedAt:  entry.AddedAt,
			isActive: e == store.Active,
			hasToken: tokenErr == nil,
		})
	}

	total := len(p.accounts) + 1 // +1 for "Add account"
	if p.selected >= total {
		p.selected = total - 1
	}
}

func (p *accountPanelState) openDetail(email string) {
	store, _ := auth.LoadAccountStore()
	isActive := store.Active == email

	p.detailEmail = email
	p.actionIdx = 0
	p.actions = nil
	if !isActive {
		p.actions = append(p.actions, accountAction{"Switch to this account", "switch", false})
	}
	p.actions = append(p.actions,
		accountAction{"Add / re-login (replace token)", "login", false},
		accountAction{"Remove from list (keep token)", "remove", false},
		accountAction{"Delete (remove token + list entry)", "delete", true},
		accountAction{"Back", "back", false},
	)
	p.view = accountViewDetail
}

// ── Key handler (called by settingspanel when tab == Accounts) ───────────────

// handleAccountsTabKey handles keys for the Accounts tab embedded in the
// settings panel. Esc and left/right are handled by the caller before this
// is invoked.
func (m Model) handleAccountsTabKey(key string) (Model, tea.Cmd) {
	p := m.settingsPanel
	if p == nil || p.accts == nil {
		return m, nil
	}
	a := p.accts

	switch a.view {
	case accountViewList:
		total := len(a.accounts) + 1
		switch key {
		case "up", "k":
			a.selected = (a.selected - 1 + total) % total
		case "down", "j":
			a.selected = (a.selected + 1) % total
		case "enter":
			if a.selected == len(a.accounts) {
				m.settingsPanel = nil
				return m, func() tea.Msg { return commands_loginMsg{} }
			}
			a.openDetail(a.accounts[a.selected].email)
		}

	case accountViewDetail:
		switch key {
		case "up", "k":
			a.actionIdx = (a.actionIdx - 1 + len(a.actions)) % len(a.actions)
		case "down", "j":
			a.actionIdx = (a.actionIdx + 1) % len(a.actions)
		case "enter":
			switch a.actions[a.actionIdx].id {
			case "switch":
				email := a.detailEmail
				m.settingsPanel = nil
				return m, func() tea.Msg { return accountSwitchedMsg{email: email} }
			case "login":
				m.settingsPanel = nil
				return m, func() tea.Msg { return commands_loginMsg{} }
			case "remove":
				store, err := auth.LoadAccountStore()
				if err == nil {
					delete(store.Accounts, a.detailEmail)
					if store.Active == a.detailEmail {
						store.Active = ""
					}
					_ = auth.SaveAccountStore(store)
				}
				a.view = accountViewList
				a.refresh()
			case "delete":
				s := secure.NewDefault()
				_ = auth.DeleteForEmail(s, a.detailEmail)
				a.view = accountViewList
				a.refresh()
			case "back":
				a.view = accountViewList
			}
		}
	}

	return m, nil
}

// ── Renderer (called by settingspanel for the Accounts tab body) ─────────────

func (m Model) renderSettingsAccounts(sb *strings.Builder, p *settingsPanelState, innerW, _ int) {
	if p.accts == nil {
		p.accts = newAccountPanel()
	}
	a := p.accts

	accent := styleStatusAccent
	dim := stylePickerDesc
	fg := lipgloss.NewStyle().Foreground(colorFg)
	errStyle := lipgloss.NewStyle().Foreground(colorError)
	danger := lipgloss.NewStyle().Foreground(colorError)

	if a.loadErr != "" {
		sb.WriteString(errStyle.Render("  Error: " + a.loadErr))
		sb.WriteString("\n\n")
		sb.WriteString(dim.Render("  [Esc] close"))
		return
	}

	switch a.view {
	case accountViewList:
		if len(a.accounts) == 0 {
			sb.WriteString(dim.Render("  No accounts saved."))
			sb.WriteByte('\n')
		}
		for i, acc := range a.accounts {
			isSel := i == a.selected
			cursor := "  "
			if isSel {
				cursor = accent.Render("❯ ")
			}
			emailStyle := fg
			if isSel {
				emailStyle = accent
			}
			line := cursor + emailStyle.Render(acc.email)
			if acc.isActive {
				line += "  " + accent.Render("● active")
			} else if !acc.hasToken {
				line += "  " + errStyle.Render("✗ no token")
			}
			sb.WriteString(line + "\n")
			sb.WriteString("    " + dim.Render("added "+acc.addedAt.Format("2006-01-02")) + "\n")
		}
		isSel := a.selected == len(a.accounts)
		addCursor := "  "
		if isSel {
			addCursor = accent.Render("❯ ")
		}
		addLabel := lipgloss.NewStyle().Foreground(colorAccent)
		if isSel {
			addLabel = accent
		}
		sb.WriteString(addCursor + addLabel.Render("+ Add account") + "\n")
		sb.WriteString("\n")
		sb.WriteString(dim.Render("  ↑↓/jk navigate · Enter select · Esc close · ←/→ tabs"))

	case accountViewDetail:
		sb.WriteString(accent.Render("  "+a.detailEmail) + "\n\n")
		for i, act := range a.actions {
			isSel := i == a.actionIdx
			cursor := "  "
			if isSel {
				cursor = accent.Render("❯ ")
			}
			var label string
			switch {
			case act.danger && isSel:
				label = danger.Bold(true).Render(act.label)
			case act.danger:
				label = danger.Render(act.label)
			case act.id == "back":
				label = dim.Render(act.label)
			case isSel:
				label = accent.Render(act.label)
			default:
				label = fg.Render(act.label)
			}
			sb.WriteString(cursor + label + "\n")
		}
		sb.WriteString("\n")
		sb.WriteString(dim.Render("  ↑↓/jk navigate · Enter confirm · Esc back"))
	}
}
