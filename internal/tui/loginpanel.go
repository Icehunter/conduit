package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/settings"
)

// loginPromptState holds the /login account picker state.
type loginPromptState struct {
	selected int
}

var loginOptions = []struct {
	label       string
	description string
	claudeAI    bool
}{
	{"Claude.ai account", "Max, Pro, or Team subscription", true},
	{"Anthropic Console", "Console / Platform / API account", false},
}

func (m Model) handleLoginKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.loginPrompt
	switch msg.String() {
	case "up", "left":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "right", "tab":
		if p.selected < len(loginOptions)-1 {
			p.selected++
		}
	case "enter", "space":
		opt := loginOptions[p.selected]
		m.loginPrompt = nil
		// Remove the "Not logged in" welcome message if present so the entire
		// login flow (including that message) gets swept away on completion.
		m.loginFlowMsgStart = m.findNoAuthMsgIdx()
		if m.loginFlowMsgStart < 0 {
			m.loginFlowMsgStart = len(m.messages)
		}
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Opening browser to sign in…"})
		m.refreshViewport()
		useClaudeAI := opt.claudeAI
		return m, func() tea.Msg {
			return loginStartMsg{claudeAI: useClaudeAI}
		}
	case "esc", "ctrl+c":
		m.loginPrompt = nil
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Login cancelled."})
		m.refreshViewport()
		return m, nil
	case "1":
		p.selected = 0
		m.loginPrompt = p
		return m.handleLoginKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	case "2":
		p.selected = 1
		m.loginPrompt = p
		return m.handleLoginKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	m.loginPrompt = p
	return m, nil
}

func (m Model) renderLoginPicker() string {
	p := m.loginPrompt
	if p == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Sign in to Claude") + "\n\n")
	sb.WriteString(stylePickerDesc.Render("Choose your account type:") + "\n\n")

	for i, opt := range loginOptions {
		var line string
		if i == p.selected {
			line = stylePickerItemSelected.Render(fmt.Sprintf("❯ %d. %s", i+1, opt.label)) +
				"  " + stylePickerDesc.Render(opt.description)
		} else {
			line = stylePickerItem.Render(fmt.Sprintf("  %d. %s", i+1, opt.label)) +
				"  " + stylePickerDesc.Render(opt.description)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter select · 1/2 quick pick · Escape cancel"))

	return sb.String()
}

// welcomeCard returns the two-panel welcome banner shown on startup.
// Content is tab-separated: version, modelName, cwd, displayName, email, orgName, subscriptionType.
func (m Model) welcomeCard() Message {
	cwd, _ := os.Getwd()
	p := m.cfg.Profile
	if provider, ok := m.providerForCurrentMode(); ok && provider.Kind == "claude-subscription" && provider.Account != "" {
		if p.Email != provider.Account {
			p = profile.Info{Email: provider.Account}
		}
		if entry, ok := accountEntryForProvider(provider); ok {
			p.Email = entry.Email
			if entry.DisplayName != "" {
				p.DisplayName = entry.DisplayName
			}
			if entry.OrganizationName != "" {
				p.OrganizationName = entry.OrganizationName
			}
			if entry.SubscriptionType != "" {
				p.SubscriptionType = entry.SubscriptionType
			}
		}
	}
	fields := []string{
		m.cfg.Version,
		m.activeModelDisplayName(),
		cwd,
		p.DisplayName,
		p.Email,
		p.OrganizationName,
		p.SubscriptionType,
	}
	return Message{
		Role:        RoleSystem,
		WelcomeCard: true,
		Content:     strings.Join(fields, "\t"),
	}
}

func accountEntryForProvider(provider settings.ActiveProviderSettings) (auth.AccountEntry, bool) {
	store, err := auth.LoadAccountStore()
	if err != nil {
		return auth.AccountEntry{}, false
	}
	for _, entry := range store.Accounts {
		if entry.Email == provider.Account && tuiProviderKindMatchesAccount(provider.Kind, entry.Kind) {
			return entry, true
		}
	}
	return auth.AccountEntry{}, false
}

// dismissWelcome removes the welcome card from the message list the first time
// the user sends a message or a slash command. Idempotent after first call.
func (m *Model) dismissWelcome() {
	if m.welcomeDismissed {
		return
	}
	m.welcomeDismissed = true
	for i, msg := range m.messages {
		if msg.WelcomeCard {
			m.messages = append(m.messages[:i], m.messages[i+1:]...)
			return
		}
	}
}

func (m *Model) refreshWelcomeCardMessage() {
	for i, msg := range m.messages {
		if msg.WelcomeCard {
			m.messages[i] = m.welcomeCard()
			m.refreshViewport()
			return
		}
	}
}
