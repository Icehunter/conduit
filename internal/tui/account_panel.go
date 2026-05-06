package tui

// Accounts tab — embedded inside the settings panel (Status·Config·Stats·Usage·Accounts).
// /account opens the settings panel on this tab directly.
//
// Views:
//   List   — all saved accounts; "● active", "✗ no token"; + Add account
//   Detail — per-account action menu (Switch / Re-login / Remove / Delete / Back)
//
// Navigation:
//   ↑/↓/jk   navigate list / actions
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
	id       string
	email    string
	kind     string
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

	accounts  []accountItem
	loadErr   string
	detailID  string
	actions   []accountAction
	actionIdx int
}

// ── Messages ─────────────────────────────────────────────────────────────────

// accountSwitchedMsg drives the live credential reload after switching.
type accountSwitchedMsg struct{ account string }

// commandsLoginMsg opens the login picker from "+ Add account".
type commandsLoginMsg struct{}

// ── Constructor ──────────────────────────────────────────────────────────────

func newAccountPanel() *accountPanelState {
	p := &accountPanelState{}
	p.refresh()
	return p
}

func accountSortLabel(id string, entry auth.AccountEntry) string {
	return accountDisplayLabel(id, entry.Email, entry.Kind)
}

func accountDisplayLabel(id, email, kind string) string {
	if email == "" {
		email = id
	}
	switch kind {
	case auth.AccountKindAnthropicConsole:
		return "Anthropic Console · " + email
	case auth.AccountKindClaudeAI, "":
		return "Claude.ai · " + email
	default:
		return kind + " · " + email
	}
}

func (p *accountPanelState) refresh() {
	store, err := auth.LoadAccountStore()
	if err != nil {
		p.loadErr = err.Error()
		return
	}
	p.loadErr = ""
	s := secure.NewDefault()

	ids := make([]string, 0, len(store.Accounts))
	for id := range store.Accounts {
		ids = append(ids, id)
	}
	// Active account first, then alphabetical.
	sort.Slice(ids, func(i, j int) bool {
		ai := ids[i] == store.Active
		aj := ids[j] == store.Active
		if ai != aj {
			return ai
		}
		left := accountSortLabel(ids[i], store.Accounts[ids[i]])
		right := accountSortLabel(ids[j], store.Accounts[ids[j]])
		return left < right
	})

	p.accounts = make([]accountItem, 0, len(ids))
	for _, id := range ids {
		entry := store.Accounts[id]
		_, tokenErr := auth.LoadForEmail(s, id)
		p.accounts = append(p.accounts, accountItem{
			id:       id,
			email:    entry.Email,
			kind:     entry.Kind,
			addedAt:  entry.AddedAt,
			isActive: id == store.Active,
			hasToken: tokenErr == nil,
		})
	}

	total := len(p.accounts) + 1 // +1 for "Add account"
	if p.selected >= total {
		p.selected = total - 1
	}
}

func (p *accountPanelState) openDetail(id string) {
	store, _ := auth.LoadAccountStore()
	isActive := store.Active == id

	p.detailID = id
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

// ── Key handler (called by settings_panel when tab == Accounts) ──────────────

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
		case "up":
			a.selected = (a.selected - 1 + total) % total
		case "down":
			a.selected = (a.selected + 1) % total
		case "enter":
			if a.selected == len(a.accounts) {
				m.settingsPanel = nil
				return m, func() tea.Msg { return commandsLoginMsg{} }
			}
			a.openDetail(a.accounts[a.selected].id)
		}

	case accountViewDetail:
		switch key {
		case "up":
			a.actionIdx = (a.actionIdx - 1 + len(a.actions)) % len(a.actions)
		case "down":
			a.actionIdx = (a.actionIdx + 1) % len(a.actions)
		case "enter":
			switch a.actions[a.actionIdx].id {
			case "switch":
				account := a.detailID
				m.settingsPanel = nil
				return m, func() tea.Msg { return accountSwitchedMsg{account: account} }
			case "login":
				m.settingsPanel = nil
				return m, func() tea.Msg { return commandsLoginMsg{} }
			case "remove":
				store, err := auth.LoadAccountStore()
				if err == nil {
					delete(store.Accounts, a.detailID)
					if store.Active == a.detailID {
						store.Active = ""
					}
					_ = auth.SaveAccountStore(store)
				}
				a.view = accountViewList
				a.refresh()
			case "delete":
				s := secure.NewDefault()
				_ = auth.DeleteForEmail(s, a.detailID)
				a.view = accountViewList
				a.refresh()
			case "back":
				a.view = accountViewList
			}
		}
	}

	return m, nil
}

// ── Renderer (called by settings_panel for the Accounts tab body) ────────────

func (m Model) renderSettingsAccounts(sb *strings.Builder, p *settingsPanelState, _, _ int) {
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
			line := cursor + emailStyle.Render(accountDisplayLabel(acc.id, acc.email, acc.kind))
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
		sb.WriteString(dim.Render("  ↑/↓ navigate · Enter select · Esc close · ←/→ tabs"))

	case accountViewDetail:
		store, _ := auth.LoadAccountStore()
		entry := store.Accounts[a.detailID]
		sb.WriteString(accent.Render("  "+accountDisplayLabel(a.detailID, entry.Email, entry.Kind)) + "\n\n")
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
		sb.WriteString(dim.Render("  ↑/↓ navigate · Enter confirm · Esc back"))
	}
}
