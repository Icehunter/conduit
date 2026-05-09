package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/commands"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

type providerFormStep int

const (
	providerFormStepCredential providerFormStep = iota
	providerFormStepBaseURL
	providerFormStepModel
	providerFormStepContextWindow
	providerFormStepAPIKey
)

type providerFormState struct {
	step       providerFormStep
	input      string
	credential string
	baseURL    string
	model      string
	contextWin int
	apiKey     string
	editKey    string
	tokenOnly  bool
	err        string
}

type providerPanelRow struct {
	key      string
	provider settings.ActiveProviderSettings
}

func (m Model) handleProvidersTabKey(key string) (Model, tea.Cmd, bool) {
	p := m.settingsPanel
	if p == nil {
		return m, nil, false
	}
	done := func() (Model, tea.Cmd, bool) {
		m.settingsPanel = p
		m.refreshViewport()
		return m, nil, true
	}
	if p.providerForm != nil {
		f := p.providerForm
		switch key {
		case "esc":
			p.providerForm = nil
			f.err = ""
		case "enter":
			if err := m.advanceProviderForm(f, false, false); err != nil {
				f.err = err.Error()
				return done()
			}
			m, _ = m.commitProviderFormIfComplete(f)
		case "up", "left", "shift+tab":
			_ = m.advanceProviderForm(f, true, true)
		case "down", "right", "tab":
			_ = m.advanceProviderForm(f, false, true)
		case "backspace":
			if len(f.input) > 0 {
				f.input = f.input[:len(f.input)-1]
				f.err = ""
			}
		default:
			if len(key) == 1 && key >= " " {
				f.input += key
				f.err = ""
			}
		}
		return done()
	}

	rows := m.providerRows()
	if p.providerDetailKey != "" {
		actions := providerDetailActions()
		switch key {
		case "up":
			p.providerAction = (p.providerAction - 1 + len(actions)) % len(actions)
		case "down":
			p.providerAction = (p.providerAction + 1) % len(actions)
		case "enter":
			action := actions[p.providerAction]
			row, ok := providerRowByKey(rows, p.providerDetailKey)
			if !ok {
				p.providerDetailKey = ""
				return done()
			}
			switch action {
			case "token":
				p.providerForm = formForProvider(row.provider, row.key, true)
			case "edit":
				p.providerForm = formForProvider(row.provider, row.key, false)
			case "delete":
				m.deleteProviderRow(row)
				p.providerDetailKey = ""
			case "back":
				p.providerDetailKey = ""
			}
		case "esc":
			p.providerDetailKey = ""
		}
		return done()
	}

	switch key {
	case "enter":
		if p.providerSel == len(rows) {
			p.providerForm = newProviderForm()
		} else if p.providerSel >= 0 && p.providerSel < len(rows) {
			p.providerDetailKey = rows[p.providerSel].key
			p.providerAction = 0
		}
	case "up":
		total := len(rows) + 1
		if total > 0 {
			p.providerSel = (p.providerSel - 1 + total) % total
		}
	case "down":
		total := len(rows) + 1
		if total > 0 {
			p.providerSel = (p.providerSel + 1) % total
		}
	}
	return done()
}

func newProviderForm() *providerFormState {
	f := &providerFormState{
		step:    providerFormStepCredential,
		baseURL: "https://provider-url",
		model:   "model-name",
	}
	f.input = f.credential
	return f
}

func formForProvider(provider settings.ActiveProviderSettings, key string, tokenOnly bool) *providerFormState {
	f := &providerFormState{
		step:       providerFormStepCredential,
		credential: provider.Credential,
		baseURL:    provider.BaseURL,
		model:      provider.Model,
		contextWin: provider.ContextWindow,
		editKey:    key,
		tokenOnly:  tokenOnly,
	}
	if tokenOnly {
		f.step = providerFormStepAPIKey
	}
	f.input = providerFormValue(f, f.step)
	return f
}

func (m Model) refreshModelCommandProviders() {
	if m.cfg.Commands == nil || m.cfg.Loop == nil {
		return
	}
	commands.RegisterModelCommand(m.cfg.Commands,
		func() string {
			if m.cfg.Live != nil {
				if enabled, server := m.cfg.Live.LocalMode(); enabled {
					if server == "" {
						server = "local-router"
					}
					return "local:" + server
				}
			}
			if m.localMode {
				server := m.localModeServer
				if server == "" {
					server = "local-router"
				}
				return "local:" + server
			}
			return internalmodel.Resolve()
		},
		func(name string) { m.cfg.Loop.SetModel(name) },
		configuredAccountProviders,
		m.cfg.MCPManager,
		m.providers,
	)
}

func (m Model) advanceProviderForm(f *providerFormState, backwards bool, navigate bool) error {
	value := strings.TrimSpace(f.input)
	switch f.step {
	case providerFormStepCredential:
		if !navigate && value == "" {
			return fmt.Errorf("credential name is required")
		}
		if !navigate && providerCredentialAliasLooksSecret(value) {
			return fmt.Errorf("credential is an alias, not the API key; put the secret in the API key field")
		}
		if value != "" {
			f.credential = value
		}
		f.input = f.baseURL
	case providerFormStepBaseURL:
		if !navigate && value == "" {
			return fmt.Errorf("base URL is required")
		}
		if value != "" {
			f.baseURL = strings.TrimRight(value, "/") + "/"
		}
		f.input = f.model
	case providerFormStepModel:
		if !navigate && value == "" {
			return fmt.Errorf("model is required")
		}
		if value != "" {
			f.model = value
		}
		f.input = providerFormValue(f, providerFormStepContextWindow)
	case providerFormStepContextWindow:
		if value == "" {
			f.contextWin = 0
		} else if n, ok := parseProviderContextWindow(value); ok {
			f.contextWin = n
		} else {
			return fmt.Errorf("context window must be a positive token count, e.g. 128k or 1000000")
		}
		f.input = ""
	case providerFormStepAPIKey:
		if !navigate && value == "" && f.editKey == "" {
			return fmt.Errorf("API key is required")
		}
		if value != "" {
			f.apiKey = value
		}
		f.input = ""
	}
	f.err = ""
	if backwards {
		if f.tokenOnly {
			return nil
		}
		if f.step > providerFormStepCredential {
			f.step--
		}
	} else {
		f.step++
	}
	if f.step <= providerFormStepAPIKey {
		f.input = providerFormValue(f, f.step)
	}
	return nil
}

func (m Model) commitProviderFormIfComplete(f *providerFormState) (Model, bool) {
	if f.step <= providerFormStepAPIKey {
		return m, false
	}
	provider := settings.ActiveProviderSettings{
		Kind:          settings.ProviderKindOpenAICompatible,
		Credential:    f.credential,
		BaseURL:       f.baseURL,
		Model:         f.model,
		ContextWindow: f.contextWin,
	}
	if f.editKey != "" && f.editKey != settings.ProviderKey(provider) {
		_ = settings.DeleteProviderEntry(f.editKey)
		delete(m.providers, f.editKey)
		for role, ref := range m.roles {
			if ref == f.editKey {
				delete(m.roles, role)
			}
		}
	}
	if err := settings.SaveProviderEntry(provider); err != nil {
		f.step = providerFormStepAPIKey
		f.err = err.Error()
		return m, false
	}
	if f.apiKey != "" {
		if err := settings.SaveProviderCredential(secure.NewDefault(), f.credential, f.apiKey); err != nil {
			f.step = providerFormStepAPIKey
			f.err = err.Error()
			return m, false
		}
	}
	if m.providers == nil {
		m.providers = map[string]settings.ActiveProviderSettings{}
	}
	m.providers[settings.ProviderKey(provider)] = provider
	m.refreshModelCommandProviders()
	if m.settingsPanel != nil {
		m.settingsPanel.providerForm = nil
		m.settingsPanel.providerDetailKey = settings.ProviderKey(provider)
		m.settingsPanel.providerSel = providerIndex(m.providerRows(), settings.ProviderKey(provider))
	}
	return m, true
}

func (m Model) renderSettingsProviders(sb *strings.Builder, p *settingsPanelState, _, contentH int) {
	accent := styleStatusAccent
	dim := stylePickerDesc
	fg := lipgloss.NewStyle().Foreground(colorFg)
	errStyle := lipgloss.NewStyle().Foreground(colorError)

	if p.providerForm != nil {
		m.renderProviderForm(sb, p.providerForm)
		return
	}
	if p.providerDetailKey != "" {
		m.renderProviderDetail(sb, p, contentH)
		return
	}
	sb.WriteString(dim.Render("  Enter select · Ctrl+M assign roles") + "\n")
	sb.WriteString(dim.Render("  credential is a local alias; the API key is stored securely and not shown") + "\n\n")
	issues := providerPanelIssues(m.providers, m.roles)
	for _, issue := range issues {
		sb.WriteString(errStyle.Render("  "+issue) + "\n")
	}
	if len(issues) > 0 {
		sb.WriteByte('\n')
	}
	rows := m.providerRows()
	visible := contentH - 2
	visible = max(visible, 3)
	start := 0
	if p.providerSel >= visible {
		start = p.providerSel - visible + 1
	}
	for i := start; i < len(rows) && i < start+visible; i++ {
		row := rows[i]
		selected := i == p.providerSel
		cursor := "  "
		if selected {
			cursor = accent.Render("❯ ")
		}
		labelStyle := fg
		if selected {
			labelStyle = accent
		}
		sb.WriteString(cursor + labelStyle.Render(accountProviderDisplayName(row.provider)) + "\n")
		sb.WriteString("    " + dim.Render(row.key) + "\n")
	}
	addSelected := p.providerSel == len(rows)
	cursor := "  "
	if addSelected {
		cursor = accent.Render("❯ ")
	}
	addLabel := lipgloss.NewStyle().Foreground(colorAccent)
	if addSelected {
		addLabel = accent
	}
	if len(rows) == 0 {
		sb.WriteString(dim.Render("  No custom providers configured.") + "\n\n")
	}
	sb.WriteString(cursor + addLabel.Render("+ Add provider") + "\n")
	sb.WriteString("    " + dim.Render("Gemini / OpenAI-compatible") + "\n")
	sb.WriteString("\n")
	sb.WriteString(dim.Render("  ↑/↓ navigate · Enter select · Esc close · ←/→ tabs"))
}

func (m Model) renderProviderDetail(sb *strings.Builder, p *settingsPanelState, _ int) {
	accent := styleStatusAccent
	dim := stylePickerDesc
	fg := lipgloss.NewStyle().Foreground(colorFg)
	danger := lipgloss.NewStyle().Foreground(colorError)

	row, ok := providerRowByKey(m.providerRows(), p.providerDetailKey)
	if !ok {
		sb.WriteString(dim.Render("  Provider no longer exists.") + "\n")
		return
	}
	sb.WriteString(accent.Render("  "+accountProviderDisplayName(row.provider)) + "\n")
	sb.WriteString("  " + dim.Render(row.key) + "\n\n")
	labels := providerDetailActionLabels()
	ids := providerDetailActions()
	for i, action := range labels {
		cursor := "  "
		if i == p.providerAction {
			cursor = accent.Render("❯ ")
		}
		var label string
		switch {
		case ids[i] == "delete" && i == p.providerAction:
			label = danger.Bold(true).Render(action)
		case ids[i] == "delete":
			label = danger.Render(action)
		case ids[i] == "back":
			label = dim.Render(action)
		case i == p.providerAction:
			label = accent.Render(action)
		default:
			label = fg.Render(action)
		}
		sb.WriteString(cursor + label + "\n")
	}
	sb.WriteString("\n")
	sb.WriteString(dim.Render("  ↑/↓ navigate · Enter confirm · Esc back"))
}

func (m Model) renderProviderForm(sb *strings.Builder, f *providerFormState) {
	accent := styleStatusAccent
	dim := stylePickerDesc
	fg := lipgloss.NewStyle().Foreground(colorFg)
	errStyle := lipgloss.NewStyle().Foreground(colorError)

	labels := []string{"Credential alias (not API key)", "Base URL", "Model", "Context window (optional)", "API key"}
	title := "Add OpenAI-compatible Provider"
	if f.editKey != "" {
		title = "Edit OpenAI-compatible Provider"
	}
	if f.tokenOnly {
		title = "Change Provider API Key"
	}
	sb.WriteString(accent.Render("  "+title) + "\n\n")
	for i, label := range labels {
		value := providerFormValue(f, providerFormStep(i))
		if providerFormStep(i) == providerFormStepAPIKey && value == "" && f.editKey != "" {
			value = "(stored securely)"
		}
		if providerFormStep(i) == providerFormStepAPIKey && value != "" {
			if value != "(stored securely)" {
				value = strings.Repeat("•", min(len(value), 12))
			}
		}
		prefix := "  "
		if providerFormStep(i) == f.step {
			prefix = accent.Render("❯ ")
			value = f.input
			if f.step == providerFormStepAPIKey && value != "" {
				value = strings.Repeat("•", min(len(value), 12))
			}
		}
		labelStyle := fg
		valueStyle := fg
		if providerFormStep(i) == f.step {
			labelStyle = accent
			valueStyle = accent
		} else if value == "" || value == "(stored securely)" {
			valueStyle = dim
		}
		sb.WriteString(prefix + labelStyle.Render(label+": ") + valueStyle.Render(value) + "\n")
	}
	if f.err != "" {
		sb.WriteString("\n" + errStyle.Render("  "+f.err) + "\n")
	}
	sb.WriteString("\n" + dim.Render("  Enter/Tab next · ↑/↓ edit fields · paste supported · Esc cancel") + "\n")
}

func providerFormValue(f *providerFormState, step providerFormStep) string {
	switch step {
	case providerFormStepCredential:
		return f.credential
	case providerFormStepBaseURL:
		return f.baseURL
	case providerFormStepModel:
		return f.model
	case providerFormStepContextWindow:
		if f.contextWin <= 0 {
			return ""
		}
		return strconv.Itoa(f.contextWin)
	case providerFormStepAPIKey:
		return f.apiKey
	default:
		return ""
	}
}

func (m Model) providerRows() []providerPanelRow {
	keys := make([]string, 0, len(m.providers))
	for key, provider := range m.providers {
		if provider.Kind == settings.ProviderKindOpenAICompatible ||
			(provider.Kind == settings.ProviderKindAnthropicAPI && provider.Credential != "") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	rows := make([]providerPanelRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, providerPanelRow{key: key, provider: m.providers[key]})
	}
	return rows
}

func providerIndex(rows []providerPanelRow, key string) int {
	for i, row := range rows {
		if row.key == key {
			return i
		}
	}
	return 0
}

func providerRowByKey(rows []providerPanelRow, key string) (providerPanelRow, bool) {
	for _, row := range rows {
		if row.key == key {
			return row, true
		}
	}
	return providerPanelRow{}, false
}

func providerDetailActions() []string {
	return []string{"token", "edit", "delete", "back"}
}

func providerDetailActionLabels() []string {
	return []string{"Change API key", "Edit provider", "Delete provider", "Back"}
}

func (m Model) deleteProviderRow(row providerPanelRow) {
	if err := settings.DeleteProviderEntry(row.key); err != nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "Delete provider failed: " + err.Error()})
		return
	}
	delete(m.providers, row.key)
	for role, ref := range m.roles {
		if ref == row.key {
			delete(m.roles, role)
		}
	}
	if row.provider.Credential != "" {
		_ = settings.DeleteProviderCredential(secure.NewDefault(), row.provider.Credential)
	}
	m.refreshModelCommandProviders()
}

func providerPanelIssues(providers map[string]settings.ActiveProviderSettings, roles map[string]string) []string {
	errs := settings.ValidateProviderRegistry(providers, roles)
	issues := make([]string, 0, len(errs))
	for _, err := range errs {
		issues = append(issues, err.Error())
	}
	if len(issues) > 3 {
		issues = append(issues[:3], fmt.Sprintf("%d more provider config issues", len(issues)-3))
	}
	return issues
}

func providerCredentialAliasLooksSecret(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) > 48 && !strings.ContainsAny(value, " ./@") {
		return true
	}
	for _, prefix := range []string{"sk-", "AIza", "xai-", "ghp_", "glpat-"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func parseProviderContextWindow(value string) (int, bool) {
	s := strings.ToLower(strings.TrimSpace(value))
	if s == "" {
		return 0, false
	}
	switch {
	case strings.HasSuffix(s, "tokens"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "tokens"))
	case strings.HasSuffix(s, "token"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "token"))
	}
	multiplier := 1
	switch {
	case strings.HasSuffix(s, "k"):
		multiplier = 1_000
		s = strings.TrimSpace(strings.TrimSuffix(s, "k"))
	case strings.HasSuffix(s, "m"):
		multiplier = 1_000_000
		s = strings.TrimSpace(strings.TrimSuffix(s, "m"))
	}
	n, err := strconv.Atoi(strings.ReplaceAll(s, "_", ""))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * multiplier, true
}
