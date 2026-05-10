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
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

// ── Types ────────────────────────────────────────────────────────────────────

type accountPanelView int

const (
	accountViewList       accountPanelView = 0
	accountViewDetail     accountPanelView = 1
	accountViewCredDetail accountPanelView = 2 // per-provider credential detail/actions
	accountViewCredForm   accountPanelView = 3 // single-line API-key entry form
)

type accountItem struct {
	id       string
	email    string
	kind     string
	addedAt  time.Time
	hasToken bool
	roles    []string // role names this account is assigned to (e.g. "planning", "main")
}

type accountAction struct {
	label  string
	id     string
	danger bool
}

type accountPanelState struct {
	view     accountPanelView
	selected int // cursor: 0..len(accounts)+1+len(builtins)-1

	accounts  []accountItem
	loadErr   string
	detailID  string
	actions   []accountAction
	actionIdx int

	// provider credential section
	credProvID string // ID of the provider being acted on (accountViewCredDetail/Form)
	credInput  string // in-progress API key text (accountViewCredForm)
	credErr    string // validation error shown in form
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
	p.refreshWithRoles(nil, nil)
}

func (p *accountPanelState) refreshWithRoles(providers map[string]settings.ActiveProviderSettings, roles map[string]string) {
	store, err := auth.LoadAccountStore()
	if err != nil {
		p.loadErr = err.Error()
		return
	}
	p.loadErr = ""
	s := secure.NewDefault()

	// Build a map from account email → role names that reference it.
	accountRoles := map[string][]string{}
	if providers != nil && roles != nil {
		for roleName, provKey := range roles {
			if prov, ok := providers[provKey]; ok && prov.Account != "" {
				accountRoles[prov.Account] = append(accountRoles[prov.Account], roleName)
			}
		}
	}

	ids := make([]string, 0, len(store.Accounts))
	for id := range store.Accounts {
		ids = append(ids, id)
	}
	// Accounts with role assignments first, then alphabetical.
	sort.Slice(ids, func(i, j int) bool {
		ei := store.Accounts[ids[i]].Email
		ej := store.Accounts[ids[j]].Email
		ri := len(accountRoles[ei]) > 0 || len(accountRoles[ids[i]]) > 0
		rj := len(accountRoles[ej]) > 0 || len(accountRoles[ids[j]]) > 0
		if ri != rj {
			return ri
		}
		left := accountSortLabel(ids[i], store.Accounts[ids[i]])
		right := accountSortLabel(ids[j], store.Accounts[ids[j]])
		return left < right
	})

	p.accounts = make([]accountItem, 0, len(ids))
	for _, id := range ids {
		entry := store.Accounts[id]
		_, tokenErr := auth.LoadForEmail(s, id)
		// Collect roles assigned to this account (check both id and email).
		var assignedRoles []string
		seen := map[string]bool{}
		for _, r := range accountRoles[id] {
			if !seen[r] {
				assignedRoles = append(assignedRoles, r)
				seen[r] = true
			}
		}
		for _, r := range accountRoles[entry.Email] {
			if !seen[r] {
				assignedRoles = append(assignedRoles, r)
				seen[r] = true
			}
		}
		sort.Strings(assignedRoles)
		p.accounts = append(p.accounts, accountItem{
			id:       id,
			email:    entry.Email,
			kind:     entry.Kind,
			addedAt:  entry.AddedAt,
			hasToken: tokenErr == nil,
			roles:    assignedRoles,
		})
	}

	total := len(p.accounts) + 1 // +1 for "Add account"
	if p.selected >= total {
		p.selected = total - 1
	}
}

func (p *accountPanelState) openDetail(id string) {
	p.detailID = id
	p.actionIdx = 0
	p.actions = []accountAction{
		{"Add / re-login (replace token)", "login", false},
		{"Remove from list (keep token)", "remove", false},
		{"Delete (remove token + list entry)", "delete", true},
		{"Back", "back", false},
	}
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
	builtins := providerauth.BuiltinConfigs()
	store := secure.NewDefault()

	switch a.view {
	case accountViewList:
		total := len(a.accounts) + 1 + len(builtins) // accounts + "+ Add" + cred rows
		switch key {
		case "up":
			a.selected = (a.selected - 1 + total) % total
		case "down":
			a.selected = (a.selected + 1) % total
		case "enter":
			switch {
			case a.selected < len(a.accounts):
				a.openDetail(a.accounts[a.selected].id)
			case a.selected == len(a.accounts):
				m.settingsPanel = nil
				return m, func() tea.Msg { return commandsLoginMsg{} }
			default:
				credIdx := a.selected - len(a.accounts) - 1
				if credIdx >= 0 && credIdx < len(builtins) {
					a.credProvID = builtins[credIdx].ID
					a.credErr = ""
					a.actionIdx = 0
					if providerauth.IsConnected(store, a.credProvID) {
						a.actions = []accountAction{
							{"Rotate key (replace)", "rotate", false},
							{"Disconnect", "disconnect", true},
							{"Back", "back", false},
						}
					} else {
						a.actions = []accountAction{
							{"Connect (enter API key)", "connect", false},
							{"Back", "back", false},
						}
					}
					a.view = accountViewCredDetail
				}
			}
		}

	case accountViewDetail:
		switch key {
		case "up":
			a.actionIdx = (a.actionIdx - 1 + len(a.actions)) % len(a.actions)
		case "down":
			a.actionIdx = (a.actionIdx + 1) % len(a.actions)
		case "enter":
			switch a.actions[a.actionIdx].id {
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
				m.refreshWelcomeCardMessage()
			case "delete":
				s := secure.NewDefault()
				_ = auth.DeleteForEmail(s, a.detailID)
				a.view = accountViewList
				a.refresh()
				m.refreshWelcomeCardMessage()
			case "back":
				a.view = accountViewList
			}
		}

	case accountViewCredDetail:
		switch key {
		case "up":
			a.actionIdx = (a.actionIdx - 1 + len(a.actions)) % len(a.actions)
		case "down":
			a.actionIdx = (a.actionIdx + 1) % len(a.actions)
		case "enter":
			switch a.actions[a.actionIdx].id {
			case "connect", "rotate":
				a.credInput = ""
				a.credErr = ""
				a.view = accountViewCredForm
			case "disconnect":
				_ = providerauth.DeleteCredential(store, a.credProvID)
				a.view = accountViewList
			case "back":
				a.view = accountViewList
			}
		}

	case accountViewCredForm:
		switch key {
		case "esc":
			a.view = accountViewCredDetail
			a.credErr = ""
		case "enter":
			auth2, authErr := providerauth.NewBuiltinAuthorizer(a.credProvID, store)
			if authErr != nil {
				a.credErr = authErr.Error()
				break
			}
			if err := auth2.Validate(context.Background(), a.credInput); err != nil {
				a.credErr = err.Error()
				break
			}
			if _, err := auth2.Authorize(context.Background(), providerauth.MethodAPIKey, map[string]string{"key": a.credInput}); err != nil {
				a.credErr = err.Error()
				break
			}
			a.credInput = ""
			a.credErr = ""
			a.view = accountViewList
		case "backspace":
			if len(a.credInput) > 0 {
				a.credInput = a.credInput[:len(a.credInput)-1]
				a.credErr = ""
			}
		default:
			if len(key) == 1 && key[0] >= ' ' {
				a.credInput += key
				a.credErr = ""
			}
		}
	}

	return m, nil
}

// ── Renderer (called by settings_panel for the Accounts tab body) ────────────

func (m Model) renderSettingsAccounts(sb *strings.Builder, p *settingsPanelState, _, _ int) {
	if p.accts == nil {
		p.accts = &accountPanelState{}
	}
	a := p.accts
	// Refresh with current role assignments so indicators are always current.
	a.refreshWithRoles(m.providers, m.roles)

	accent := styleStatusAccent
	dim := stylePickerDesc
	fg := lipgloss.NewStyle().Foreground(colorFg)
	errStyle := lipgloss.NewStyle().Foreground(colorError)
	danger := lipgloss.NewStyle().Foreground(colorError)
	section := lipgloss.NewStyle().Foreground(colorAccent)

	if a.loadErr != "" {
		sb.WriteString(errStyle.Render("  Error: " + a.loadErr))
		sb.WriteString("\n\n")
		sb.WriteString(dim.Render("  [Esc] close"))
		return
	}

	builtins := providerauth.BuiltinConfigs()
	store := secure.NewDefault()

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
			if !acc.hasToken {
				line += "  " + errStyle.Render("✗ no token")
			}
			sb.WriteString(line + "\n")
			// Show which modes/roles this account is assigned to.
			if len(acc.roles) > 0 {
				sb.WriteString("    " + accent.Render("↳ "+strings.Join(acc.roles, ", ")) + "\n")
			}
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

		// Provider credentials section.
		sb.WriteString("\n")
		sb.WriteString("  " + section.Render("── Provider credentials") + "\n")
		for i, cfg := range builtins {
			listIdx := len(a.accounts) + 1 + i
			isSel := a.selected == listIdx
			cursor := "  "
			if isSel {
				cursor = accent.Render("❯ ")
			}
			nameStyle := fg
			if isSel {
				nameStyle = accent
			}
			connected := providerauth.IsConnected(store, cfg.ID)
			var badge string
			if connected {
				badge = "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Render("api key ✓")
			} else {
				badge = "  " + dim.Render("not set up")
			}
			sb.WriteString(cursor + nameStyle.Render(cfg.DisplayName) + badge + "\n")
		}
		sb.WriteString("\n")
		sb.WriteString(dim.Render("  ↑/↓ navigate · Enter select · Esc close · ←/→ tabs"))

	case accountViewDetail:
		store2, _ := auth.LoadAccountStore()
		entry := store2.Accounts[a.detailID]
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

	case accountViewCredDetail:
		cfg, _ := providerauth.BuiltinByID(a.credProvID)
		connected := providerauth.IsConnected(store, a.credProvID)
		var statusBadge string
		if connected {
			statusBadge = "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Render("connected")
		} else {
			statusBadge = "  " + errStyle.Render("not connected")
		}
		sb.WriteString(accent.Render("  "+cfg.DisplayName) + statusBadge + "\n\n")
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

	case accountViewCredForm:
		cfg, _ := providerauth.BuiltinByID(a.credProvID)
		hint := ""
		if len(cfg.Methods) > 0 {
			hint = cfg.Methods[0].Hint
		}
		envVar := ""
		if len(cfg.Methods) > 0 && cfg.Methods[0].EnvVar != "" {
			envVar = cfg.Methods[0].EnvVar
		}
		sb.WriteString(accent.Render("  "+cfg.DisplayName+" — enter API key") + "\n\n")
		displayKey := a.credInput
		if displayKey == "" {
			displayKey = dim.Render(hint)
		}
		sb.WriteString(fmt.Sprintf("  %s\n", displayKey))
		if a.credErr != "" {
			sb.WriteString("\n  " + errStyle.Render(a.credErr) + "\n")
		}
		if envVar != "" {
			sb.WriteString("\n  " + dim.Render("or set $"+envVar) + "\n")
		}
		if cfg.DocsURL != "" {
			sb.WriteString("  " + dim.Render(cfg.DocsURL) + "\n")
		}
		sb.WriteString("\n")
		sb.WriteString(dim.Render("  Enter confirm · Esc back"))
	}
}
