package tui

// Account panel — full-screen takeover for multi-account management.
// Matches the settings/plugin panel visual style: rounded border, accent tabs
// with | separator, ❯ cursor for list items.
//
// Views:
//   List   — all saved accounts; "● active", "✗ no token"; + Add account
//   Detail — per-account action menu (Switch / Re-login / Remove / Delete / Back)
//
// Navigation:
//   ↑↓/jk    navigate list / actions
//   Enter     select
//   Esc       back (detail → list → close)
//   q         close from anywhere

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
	label    string
	id       string
	danger   bool
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

// ── Key handler ──────────────────────────────────────────────────────────────

func (m Model) handleAccountPanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.accountPanel
	if p == nil {
		return m, nil
	}
	key := msg.String()

	switch p.view {
	case accountViewList:
		total := len(p.accounts) + 1 // last slot = "+ Add account"
		switch key {
		case "up", "k":
			p.selected = (p.selected - 1 + total) % total
		case "down", "j":
			p.selected = (p.selected + 1) % total
		case "enter":
			if p.selected == len(p.accounts) {
				m.accountPanel = nil
				return m, func() tea.Msg { return commands_loginMsg{} }
			}
			p.openDetail(p.accounts[p.selected].email)
		case "esc", "q", "ctrl+c":
			m.accountPanel = nil
		}

	case accountViewDetail:
		switch key {
		case "up", "k":
			p.actionIdx = (p.actionIdx - 1 + len(p.actions)) % len(p.actions)
		case "down", "j":
			p.actionIdx = (p.actionIdx + 1) % len(p.actions)
		case "enter":
			switch p.actions[p.actionIdx].id {
			case "switch":
				email := p.detailEmail
				m.accountPanel = nil
				return m, func() tea.Msg { return accountSwitchedMsg{email: email} }
			case "login":
				m.accountPanel = nil
				return m, func() tea.Msg { return commands_loginMsg{} }
			case "remove":
				store, err := auth.LoadAccountStore()
				if err == nil {
					delete(store.Accounts, p.detailEmail)
					if store.Active == p.detailEmail {
						store.Active = ""
					}
					_ = auth.SaveAccountStore(store)
				}
				p.view = accountViewList
				p.refresh()
			case "delete":
				s := secure.NewDefault()
				_ = auth.DeleteForEmail(s, p.detailEmail)
				p.view = accountViewList
				p.refresh()
			case "back":
				p.view = accountViewList
			}
		case "esc", "backspace":
			p.view = accountViewList
		case "q", "ctrl+c":
			m.accountPanel = nil
		}
	}

	m.accountPanel = p
	m.refreshViewport()
	return m, nil
}

// ── Renderer ─────────────────────────────────────────────────────────────────

func (m *Model) renderAccountPanel() string {
	p := m.accountPanel
	if p == nil {
		return ""
	}

	w := m.width
	if w < 20 {
		w = 20
	}
	panelH := m.height - 1
	if panelH < 6 {
		panelH = 6
	}
	// innerW mirrors settingspanel.go: outer Width(w-2), border 1+1, padding 2+2
	innerW := w - 8

	// Reusable styles (shared from package-level vars).
	accent := styleStatusAccent  // bright accent, bold
	dim := stylePickerDesc       // muted/secondary text
	fg := lipgloss.NewStyle().Foreground(colorFg)
	err := lipgloss.NewStyle().Foreground(colorError)
	danger := lipgloss.NewStyle().Foreground(colorError)

	var sb strings.Builder

	// ── Tab header ─────────────────────────────────────────────────────────
	sb.WriteString(accent.Render("Accounts"))
	sb.WriteByte('\n')
	sb.WriteString(dim.Render(strings.Repeat("─", innerW)))
	sb.WriteString("\n\n")

	if p.loadErr != "" {
		sb.WriteString(err.Render("  Error: " + p.loadErr))
		sb.WriteString("\n\n")
		sb.WriteString(dim.Render("  [Esc] close"))
		return wrapAccountPanel(sb.String(), w, panelH)
	}

	switch p.view {

	// ── List view ───────────────────────────────────────────────────────────
	case accountViewList:
		if len(p.accounts) == 0 {
			sb.WriteString(dim.Render("  No accounts saved."))
			sb.WriteByte('\n')
		}

		for i, acc := range p.accounts {
			isSel := i == p.selected

			cursor := "  "
			if isSel {
				cursor = accent.Render("❯ ")
			}

			// Email label
			emailStyle := fg
			if isSel {
				emailStyle = accent
			}
			line := cursor + emailStyle.Render(acc.email)

			// Status badge
			if acc.isActive {
				line += "  " + accent.Render("● active")
			} else if !acc.hasToken {
				line += "  " + err.Render("✗ no token")
			}
			sb.WriteString(line + "\n")

			// Secondary: added date
			addedLine := "    " + dim.Render("added "+acc.addedAt.Format("2006-01-02"))
			sb.WriteString(addedLine + "\n")
		}

		// "+ Add account" row
		isSel := p.selected == len(p.accounts)
		addCursor := "  "
		if isSel {
			addCursor = accent.Render("❯ ")
		}
		addStyle := lipgloss.NewStyle().Foreground(colorAccent)
		if isSel {
			addStyle = accent
		}
		sb.WriteString(addCursor + addStyle.Render("+ Add account") + "\n")
		sb.WriteString("\n")
		sb.WriteString(dim.Render("  ↑↓/jk navigate · Enter select · Esc/q close"))

	// ── Detail view ─────────────────────────────────────────────────────────
	case accountViewDetail:
		sb.WriteString(accent.Render("  "+p.detailEmail) + "\n\n")

		for i, act := range p.actions {
			isSel := i == p.actionIdx
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
		sb.WriteString(dim.Render("  ↑↓/jk navigate · Enter confirm · Esc back · q close"))
	}

	return wrapAccountPanel(sb.String(), w, panelH)
}

func wrapAccountPanel(content string, w, panelH int) string {
	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1).
		Width(w - 2).
		Height(panelH - 2)
	return style.Render(content)
}

