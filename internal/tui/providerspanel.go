// Package tui implements the Conduit terminal UI.
package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/catalog"
	"github.com/icehunter/conduit/internal/commands"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/provider/codex"
	"github.com/icehunter/conduit/internal/provider/copilot"
	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

type providerFormStep int

const (
	providerFormStepPicker     providerFormStep = iota // choose known provider or custom
	providerFormStepCredential                         // alias name
	providerFormStepBaseURL                            // base URL
	providerFormStepAPIKey                             // API key
	providerFormStepOAuth                              // OAuth flow step
)

type knownProviderEntry struct {
	id          string // providerauth ID (also used as catalog provider slug)
	displayName string
	baseURL     string
	catalogSlug string // provider slug in catalog data
	isOAuth     bool
	oauthKind   string
}

var knownProviders = []knownProviderEntry{
	{id: "openai", displayName: "OpenAI", baseURL: "https://api.openai.com/v1/", catalogSlug: "openai"},
	{id: "gemini", displayName: "Gemini (Google AI Studio)", baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", catalogSlug: "google"},
	{id: "openrouter", displayName: "OpenRouter", baseURL: "https://openrouter.ai/api/v1/", catalogSlug: "openrouter"},
	{id: "github-copilot", displayName: "GitHub Copilot", isOAuth: true, oauthKind: "copilot"},
	{id: "chatgpt-codex", displayName: "ChatGPT / Codex", isOAuth: true, oauthKind: "codex"},
}

type providerFormState struct {
	step      providerFormStep
	pickerIdx int // index into knownProviders; len(knownProviders) = "Custom"
	input     string

	credential string
	baseURL    string
	apiKey     string
	editKey    string
	tokenOnly  bool
	err        string

	oauthStarting bool
	oauthInfo     *copilot.DeviceCodeResponse
	oauthProvider bool
	oauthKind     string
}

type copilotOAuthStartedMsg struct {
	info *copilot.DeviceCodeResponse
	err  error
}

type copilotOAuthCompletedMsg struct {
	models  []string
	warning string
	err     error
}

type codexOAuthCompletedMsg struct {
	models []string
	err    error
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
	doneWithCmd := func(cmd tea.Cmd) (Model, tea.Cmd, bool) {
		m.settingsPanel = p
		m.refreshViewport()
		return m, cmd, true
	}
	done := func() (Model, tea.Cmd, bool) {
		return doneWithCmd(nil)
	}
	if p.providerForm != nil {
		f := p.providerForm
		switch key {
		case "esc":
			p.providerForm = nil
			f.err = ""
		case "up", "left", "shift+tab":
			if f.step == providerFormStepPicker {
				total := len(knownProviders) + 1
				f.pickerIdx = (f.pickerIdx - 1 + total) % total
			} else {
				_ = m.advanceProviderForm(f, true, true)
			}
		case "down", "right", "tab":
			if f.step == providerFormStepPicker {
				total := len(knownProviders) + 1
				f.pickerIdx = (f.pickerIdx + 1) % total
			} else {
				_ = m.advanceProviderForm(f, false, true)
			}
		case "enter":
			if f.step == providerFormStepPicker {
				m.applyProviderPicker(f)
			} else if f.oauthProvider && f.step == providerFormStepCredential && f.editKey == "" {
				value := strings.TrimSpace(f.input)
				if value == "" {
					f.err = "credential alias is required"
					return done()
				}
				if providerCredentialAliasLooksSecret(value) {
					f.err = "credential is an alias, not a token"
					return done()
				}
				f.credential = value
				f.step = providerFormStepOAuth
				f.oauthStarting = true
				f.err = ""
				if f.oauthKind == "codex" {
					return doneWithCmd(codexStartOAuthCmd(f.credential))
				}
				return doneWithCmd(copilotStartOAuthCmd(f.credential))
			} else if f.oauthProvider && f.step == providerFormStepCredential && f.editKey != "" {
				if err := m.advanceProviderForm(f, false, false); err != nil {
					f.err = err.Error()
					return done()
				}
				m = m.commitProviderFormIfComplete(f)
			} else if f.step == providerFormStepOAuth {
				if f.oauthKind == "codex" {
					if err := m.completeCodexOAuthForm(f, nil); err != nil {
						f.err = err.Error()
						return done()
					}
					m = m.commitProviderFormIfComplete(f)
					return done()
				}
				if err := m.completeCopilotOAuthForm(f, nil); err != nil {
					f.err = err.Error()
					return done()
				}
				m = m.commitProviderFormIfComplete(f)
			} else {
				if err := m.advanceProviderForm(f, false, false); err != nil {
					f.err = err.Error()
					return done()
				}
				m = m.commitProviderFormIfComplete(f)
			}
		case "backspace":
			if f.step != providerFormStepPicker && len(f.input) > 0 {
				f.input = f.input[:len(f.input)-1]
				f.err = ""
			}
		default:
			if f.step != providerFormStepPicker && len(key) == 1 && key >= " " {
				f.input += key
				f.err = ""
			}
		}
		return done()
	}

	rows := m.providerRows()
	if p.providerDetailKey != "" {
		switch key {
		case "enter":
			row, ok := providerRowByKey(rows, p.providerDetailKey)
			if !ok {
				p.providerDetailKey = ""
				return done()
			}
			actions := providerDetailActionsFor(row.provider)
			if p.providerAction >= len(actions) {
				p.providerAction = 0
			}
			action := actions[p.providerAction]
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
		case "up":
			row, ok := providerRowByKey(rows, p.providerDetailKey)
			if !ok {
				p.providerDetailKey = ""
				return done()
			}
			actions := providerDetailActionsFor(row.provider)
			p.providerAction = (p.providerAction - 1 + len(actions)) % len(actions)
		case "down":
			row, ok := providerRowByKey(rows, p.providerDetailKey)
			if !ok {
				p.providerDetailKey = ""
				return done()
			}
			actions := providerDetailActionsFor(row.provider)
			p.providerAction = (p.providerAction + 1) % len(actions)
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
	return &providerFormState{
		step:      providerFormStepPicker,
		pickerIdx: 0,
	}
}

func formForProvider(provider settings.ActiveProviderSettings, key string, tokenOnly bool) *providerFormState {
	f := &providerFormState{
		step:       providerFormStepCredential,
		credential: provider.Credential,
		baseURL:    provider.BaseURL,
		editKey:    key,
		tokenOnly:  tokenOnly,
		pickerIdx:  len(knownProviders), // skip picker — editing existing
	}
	if isCopilotProvider(provider) {
		f.oauthProvider = true
		f.oauthKind = "copilot"
		f.baseURL = copilot.ChatBaseURL
	} else if isCodexProvider(provider) {
		f.oauthProvider = true
		f.oauthKind = "codex"
		f.baseURL = codex.CodexBaseURL
	}
	if tokenOnly {
		f.step = providerFormStepAPIKey
	}
	f.input = providerFormValue(f, f.step)
	return f
}

func (m Model) applyProviderPicker(f *providerFormState) {
	total := len(knownProviders)
	if f.pickerIdx >= total {
		f.step = providerFormStepCredential
		f.input = f.credential
		return
	}
	preset := knownProviders[f.pickerIdx]

	if preset.isOAuth {
		f.oauthProvider = true
		f.oauthKind = preset.oauthKind
		if f.oauthKind == "codex" {
			f.credential = m.nextCodexCredentialAlias()
			f.baseURL = codex.CodexBaseURL
		} else {
			f.credential = m.nextCopilotCredentialAlias()
			f.baseURL = copilot.ChatBaseURL
		}
		f.step = providerFormStepCredential
		f.oauthInfo = nil
		f.err = ""
		f.input = f.credential
		return
	}

	f.baseURL = preset.baseURL
	f.credential = preset.id

	store := secure.NewDefault()
	if existing, err := providerauth.LoadCredential(store, preset.id); err == nil && existing != "" {
		f.apiKey = existing
	}

	f.step = providerFormStepCredential
	f.input = f.credential
}

func (m Model) nextCodexCredentialAlias() string {
	return m.nextOAuthCredentialAlias(codex.ProviderID, isCodexProvider)
}

func (m Model) nextCopilotCredentialAlias() string {
	return m.nextOAuthCredentialAlias(copilot.ProviderID, isCopilotProvider)
}

func (m Model) nextOAuthCredentialAlias(base string, matches func(settings.ActiveProviderSettings) bool) string {
	used := map[string]bool{}
	for _, provider := range m.providers {
		if matches(provider) && provider.Credential != "" {
			used[provider.Credential] = true
		}
	}
	if _, err := settings.LoadStructuredProviderCredential(secure.NewDefault(), base); err == nil {
		used[base] = true
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		alias := fmt.Sprintf("%s-%d", base, i)
		if used[alias] {
			continue
		}
		if _, err := settings.LoadStructuredProviderCredential(secure.NewDefault(), alias); err == nil {
			continue
		}
		return alias
	}
}

func (m Model) refreshModelCommandProviders() {
	if m.cfg.Commands == nil || m.cfg.Loop == nil {
		return
	}
	m.ensureCopilotFallbackProviders(copilot.ProviderID)
	m.ensureCodexFallbackProviders(codex.ProviderID)
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
		m.catalogData,
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
		if f.oauthProvider && f.editKey != "" {
			f.step = providerFormStepAPIKey + 1
			f.input = ""
			f.err = ""
			return nil
		}
		f.input = f.baseURL
	case providerFormStepBaseURL:
		if !navigate && value == "" {
			return fmt.Errorf("base URL is required")
		}
		if value != "" {
			f.baseURL = strings.TrimRight(value, "/") + "/"
		}
		f.input = ""
	case providerFormStepAPIKey:
		if !navigate && value == "" && f.editKey == "" && f.apiKey == "" {
			return fmt.Errorf("API key is required")
		}
		if value != "" {
			f.apiKey = value
		}
		f.input = ""
	case providerFormStepOAuth:
	}
	f.err = ""
	if backwards {
		if f.tokenOnly {
			return nil
		}
		// Don't step back into the picker when editing an existing provider.
		minStep := providerFormStepCredential
		if f.step > minStep {
			f.step--
		}
	} else {
		f.step++
		// Skip the API key step when providerauth already supplied a key.
		if f.step == providerFormStepAPIKey && f.apiKey != "" && f.editKey == "" {
			f.step++
		}
	}
	if f.step <= providerFormStepAPIKey {
		f.input = providerFormValue(f, f.step)
	}
	return nil
}

func (m Model) commitProviderFormIfComplete(f *providerFormState) Model {
	if f.step <= providerFormStepAPIKey || f.step == providerFormStepOAuth {
		return m
	}
	if f.oauthProvider {
		if f.editKey == "" {
			return m
		}
		baseURL := copilot.ChatBaseURL
		if f.oauthKind == "codex" {
			baseURL = codex.CodexBaseURL
		}
		if err := m.renameProviderGroup(f.editKey, f.credential, baseURL, ""); err != nil {
			f.step = providerFormStepCredential
			f.err = err.Error()
			return m
		}
		if m.settingsPanel != nil {
			m.settingsPanel.providerForm = nil
			m.settingsPanel.providerDetailKey = providerAccountRowKey(m.providerRows(), f.credential, baseURL)
			m.settingsPanel.providerSel = providerIndex(m.providerRows(), m.settingsPanel.providerDetailKey)
		}
		m.refreshModelCommandProviders()
		return m
	}
	if f.tokenOnly {
		if strings.TrimSpace(f.apiKey) == "" {
			f.step = providerFormStepAPIKey
			f.err = "API key is required"
			return m
		}
		if err := settings.SaveProviderCredential(secure.NewDefault(), f.credential, f.apiKey); err != nil {
			f.step = providerFormStepAPIKey
			f.err = err.Error()
			return m
		}
		if m.settingsPanel != nil {
			m.settingsPanel.providerForm = nil
			m.settingsPanel.providerDetailKey = f.editKey
		}
		return m
	}
	provider := settings.ActiveProviderSettings{
		Kind:       settings.ProviderKindOpenAICompatible,
		Credential: f.credential,
		BaseURL:    f.baseURL,
	}
	if f.editKey != "" {
		if err := m.renameProviderGroup(f.editKey, f.credential, f.baseURL, f.apiKey); err != nil {
			f.step = providerFormStepAPIKey
			f.err = err.Error()
			return m
		}
		if m.settingsPanel != nil {
			m.settingsPanel.providerForm = nil
			m.settingsPanel.providerDetailKey = providerAccountRowKey(m.providerRows(), f.credential, f.baseURL)
			m.settingsPanel.providerSel = providerIndex(m.providerRows(), m.settingsPanel.providerDetailKey)
		}
		m.refreshModelCommandProviders()
		return m
	}
	if err := settings.SaveProviderEntry(provider); err != nil {
		f.step = providerFormStepAPIKey
		f.err = err.Error()
		return m
	}
	// Persist API key: prefer the form's typed key; fall back to the
	// providerauth credential that applyProviderPicker pre-loaded.
	keyToSave := f.apiKey
	if keyToSave != "" {
		store := secure.NewDefault()
		if err := settings.SaveProviderCredential(store, f.credential, keyToSave); err != nil {
			f.step = providerFormStepAPIKey
			f.err = err.Error()
			return m
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
	return m
}

func (m *Model) renameProviderGroup(editKey, credential, baseURL, apiKey string) error {
	row, ok := providerRowByKey(m.providerRows(), editKey)
	if !ok {
		return fmt.Errorf("provider no longer exists")
	}
	credential = strings.TrimSpace(credential)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/"
	if credential == "" {
		return fmt.Errorf("credential alias is required")
	}
	if providerCredentialAliasLooksSecret(credential) {
		return fmt.Errorf("credential is an alias, not the API key")
	}
	oldCredential := row.provider.Credential
	oldBaseURL := row.provider.BaseURL
	if isCopilotProvider(row.provider) {
		baseURL = copilot.ChatBaseURL
		oldBaseURL = copilot.ChatBaseURL
	} else if isCodexProvider(row.provider) {
		baseURL = codex.CodexBaseURL
		oldBaseURL = codex.CodexBaseURL
	}

	updated := map[string]string{}
	nextProviders := cloneProviderMap(m.providers)
	for key, provider := range m.providers {
		if !sameProviderAccount(provider, oldCredential, oldBaseURL) {
			continue
		}
		delete(nextProviders, key)
		provider.Credential = credential
		provider.BaseURL = baseURL
		if isCopilotProvider(provider) {
			provider.ContextWindow = copilot.ContextWindowForModel(provider.Model, provider.ContextWindow)
		} else if isCodexProvider(provider) {
			provider.ContextWindow = codex.ContextWindowForModel(provider.Model, provider.ContextWindow)
		}
		if err := settings.ValidateProviderSettings(provider); err != nil {
			return err
		}
		newKey := settings.ProviderKey(provider)
		nextProviders[newKey] = provider
		updated[key] = newKey
	}
	if len(updated) == 0 {
		return fmt.Errorf("provider no longer exists")
	}

	nextRoles := cloneStringMap(m.roles)
	for role, ref := range nextRoles {
		if newRef, ok := updated[ref]; ok {
			nextRoles[role] = newRef
		}
	}
	if err := settings.UpdateConduitConfig(func(cfg *settings.ConduitConfig) {
		if cfg.Providers == nil {
			cfg.Providers = map[string]settings.ActiveProviderSettings{}
		}
		cfg.Providers, cfg.Roles, _ = settings.CanonicalizeProviderRegistry(cfg.Providers, cfg.Roles)
		for oldKey, newKey := range updated {
			delete(cfg.Providers, oldKey)
			cfg.Providers[newKey] = nextProviders[newKey]
		}
		if cfg.Roles != nil {
			for role, ref := range cfg.Roles {
				if newRef, ok := updated[ref]; ok {
					cfg.Roles[role] = newRef
				}
			}
		}
		for i, ref := range cfg.CouncilProviders {
			if newRef, ok := updated[strings.TrimPrefix(ref, "provider:")]; ok {
				cfg.CouncilProviders[i] = newRef
			}
		}
		if newRef, ok := updated[strings.TrimPrefix(cfg.CouncilSynthesizer, "provider:")]; ok {
			cfg.CouncilSynthesizer = newRef
		}
		if cfg.CouncilRoles != nil {
			for ref, role := range cfg.CouncilRoles {
				if newRef, ok := updated[strings.TrimPrefix(ref, "provider:")]; ok {
					delete(cfg.CouncilRoles, ref)
					cfg.CouncilRoles[newRef] = role
				}
			}
		}
		if cfg.ActiveProvider != nil && sameProviderAccount(*cfg.ActiveProvider, oldCredential, oldBaseURL) {
			cfg.ActiveProvider.Credential = credential
			cfg.ActiveProvider.BaseURL = baseURL
		}
	}); err != nil {
		return err
	}
	if apiKey != "" {
		if err := settings.SaveProviderCredential(secure.NewDefault(), credential, apiKey); err != nil {
			return err
		}
		if credential != oldCredential {
			_ = settings.DeleteProviderCredential(secure.NewDefault(), oldCredential)
		}
	} else if credential != oldCredential {
		_ = moveProviderCredential(secure.NewDefault(), oldCredential, credential)
	}
	m.providers = nextProviders
	m.roles = nextRoles
	if m.activeProvider != nil && sameProviderAccount(*m.activeProvider, oldCredential, oldBaseURL) {
		m.activeProvider.Credential = credential
		m.activeProvider.BaseURL = baseURL
	}
	return nil
}

func sameProviderAccount(provider settings.ActiveProviderSettings, credential, baseURL string) bool {
	if provider.Kind != settings.ProviderKindOpenAICompatible {
		return false
	}
	return provider.Credential == credential && strings.TrimRight(provider.BaseURL, "/") == strings.TrimRight(baseURL, "/")
}

func moveProviderCredential(store secure.Storage, oldName, newName string) error {
	if oldName == "" || newName == "" || oldName == newName {
		return nil
	}
	if cred, err := settings.LoadStructuredProviderCredential(store, oldName); err == nil {
		if err := settings.SaveStructuredProviderCredential(store, newName, cred); err != nil {
			return err
		}
		return settings.DeleteProviderCredential(store, oldName)
	}
	if key, err := settings.LoadProviderCredential(store, oldName); err == nil && key != "" {
		if err := settings.SaveProviderCredential(store, newName, key); err != nil {
			return err
		}
		return settings.DeleteProviderCredential(store, oldName)
	}
	return nil
}

func copilotStartOAuthCmd(credential string) tea.Cmd {
	return func() tea.Msg {
		auth := copilot.NewAuthorizerForCredential(secure.NewDefault(), credential)
		info, err := auth.InitiateDeviceFlow(context.Background())
		if err != nil {
			return copilotOAuthStartedMsg{err: err}
		}
		return copilotOAuthStartedMsg{info: info}
	}
}

func copilotPollOAuthCmd(credential string, info *copilot.DeviceCodeResponse) tea.Cmd {
	return func() tea.Msg {
		if info == nil {
			return copilotOAuthCompletedMsg{err: fmt.Errorf("copilot: missing device flow state")}
		}
		auth := copilot.NewAuthorizerForCredential(secure.NewDefault(), credential)
		timeout := time.Duration(info.ExpiresIn) * time.Second
		if timeout <= 0 {
			timeout = 15 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if _, err := auth.PollToken(ctx, info.DeviceCode, info.Interval); err != nil {
			return copilotOAuthCompletedMsg{err: err}
		}
		return discoverCopilotModels(auth, "authorized, but model discovery failed")
	}
}

func discoverCopilotModels(auth *copilot.Authorizer, prefix string) tea.Msg {
	models, err := auth.FetchModels(context.Background())
	if err != nil {
		return copilotOAuthCompletedMsg{models: copilotModelIDs(copilot.FallbackModels()), warning: fmt.Sprintf("%s: %v; using fallback model list", prefix, err)}
	}
	return copilotOAuthCompletedMsg{models: copilotModelIDs(models)}
}

func copilotModelIDs(models []catalog.ModelInfo) []string {
	modelIDs := make([]string, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		modelIDs = append(modelIDs, id)
	}
	sort.Strings(modelIDs)
	if len(modelIDs) == 0 {
		return copilotModelIDs(copilot.FallbackModels())
	}
	return modelIDs
}

func codexStartOAuthCmd(credential string) tea.Cmd {
	return func() tea.Msg {
		auth := codex.NewAuthorizerForCredential(secure.NewDefault(), credential)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := auth.AuthorizeBrowser(ctx); err != nil {
			return codexOAuthCompletedMsg{err: err}
		}
		return codexOAuthCompletedMsg{models: codex.FallbackModelIDs()}
	}
}

func (m Model) handleCopilotOAuthStarted(msg copilotOAuthStartedMsg) (Model, tea.Cmd) {
	if m.settingsPanel == nil || m.settingsPanel.providerForm == nil {
		return m, nil
	}
	f := m.settingsPanel.providerForm
	if f.step != providerFormStepOAuth {
		return m, nil
	}
	f.oauthStarting = false
	if msg.err != nil {
		f.err = msg.err.Error()
		f.step = providerFormStepPicker
		m.refreshViewport()
		return m, nil
	}
	f.oauthInfo = msg.info
	f.err = ""
	m.refreshViewport()
	return m, copilotPollOAuthCmd(f.credential, msg.info)
}

func (m Model) handleCopilotOAuthCompleted(msg copilotOAuthCompletedMsg) (Model, tea.Cmd) {
	if m.settingsPanel == nil || m.settingsPanel.providerForm == nil {
		return m, nil
	}
	f := m.settingsPanel.providerForm
	if f.step != providerFormStepOAuth {
		return m, nil
	}
	if msg.err != nil {
		f.err = gracefulCopilotError(msg.err)
		m.refreshViewport()
		return m, nil
	}
	if err := m.completeCopilotOAuthForm(f, msg.models); err != nil {
		f.err = err.Error()
		m.refreshViewport()
		return m, nil
	}
	if m.settingsPanel != nil {
		m.settingsPanel.providerForm = nil
		m.settingsPanel.providerDetailKey = ""
		m.settingsPanel.providerSel = providerIndex(m.providerRows(), settings.ProviderKey(settings.ActiveProviderSettings{
			Kind:       settings.ProviderKindOpenAICompatible,
			Credential: f.credential,
			BaseURL:    copilot.ChatBaseURL,
			Model:      msg.models[0],
		}))
	}
	m.refreshModelCommandProviders()
	m.flashMsg = fmt.Sprintf("GitHub Copilot connected — %d models available", len(msg.models))
	if msg.warning != "" {
		m.flashMsg = "GitHub Copilot connected — using fallback model list"
	}
	m.refreshViewport()
	return m, tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })
}

func (m Model) handleCodexOAuthCompleted(msg codexOAuthCompletedMsg) (Model, tea.Cmd) {
	if m.settingsPanel == nil || m.settingsPanel.providerForm == nil {
		return m, nil
	}
	f := m.settingsPanel.providerForm
	if f.step != providerFormStepOAuth || f.oauthKind != "codex" {
		return m, nil
	}
	f.oauthStarting = false
	if msg.err != nil {
		f.err = gracefulCodexError(msg.err)
		m.refreshViewport()
		return m, nil
	}
	if err := m.completeCodexOAuthForm(f, msg.models); err != nil {
		f.err = err.Error()
		m.refreshViewport()
		return m, nil
	}
	if m.settingsPanel != nil {
		m.settingsPanel.providerForm = nil
		m.settingsPanel.providerDetailKey = ""
		if len(msg.models) > 0 {
			m.settingsPanel.providerSel = providerIndex(m.providerRows(), settings.ProviderKey(settings.ActiveProviderSettings{
				Kind:       settings.ProviderKindOpenAICompatible,
				Credential: f.credential,
				BaseURL:    codex.CodexBaseURL,
				Model:      msg.models[0],
			}))
		}
	}
	m.refreshModelCommandProviders()
	m.flashMsg = fmt.Sprintf("ChatGPT / Codex connected — %d models available", len(msg.models))
	m.refreshViewport()
	return m, tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })
}

func (m Model) completeCodexOAuthForm(f *providerFormState, models []string) error {
	if f == nil {
		return fmt.Errorf("codex: provider form missing")
	}
	if f.oauthStarting {
		return fmt.Errorf("codex: authorization is still running")
	}
	if len(models) == 0 {
		return fmt.Errorf("codex: no models available")
	}
	if strings.TrimSpace(f.credential) == "" {
		f.credential = codex.ProviderID
	}
	f.baseURL = codex.CodexBaseURL
	f.apiKey = ""
	f.step = providerFormStepAPIKey + 1
	if m.providers == nil {
		m.providers = map[string]settings.ActiveProviderSettings{}
	}
	for _, model := range models {
		provider := settings.ActiveProviderSettings{
			Kind:          settings.ProviderKindOpenAICompatible,
			Credential:    f.credential,
			BaseURL:       codex.CodexBaseURL,
			Model:         model,
			ContextWindow: codex.ContextWindowForModel(model, 0),
		}
		if err := settings.SaveProviderEntry(provider); err != nil {
			return err
		}
		m.providers[settings.ProviderKey(provider)] = provider
	}
	return nil
}

func (m Model) completeCopilotOAuthForm(f *providerFormState, models []string) error {
	if f == nil {
		return fmt.Errorf("copilot: provider form missing")
	}
	if f.oauthStarting {
		return fmt.Errorf("copilot: still requesting device code")
	}
	if f.oauthInfo == nil && len(models) == 0 {
		return fmt.Errorf("copilot: authorize in your browser first")
	}
	if len(models) == 0 {
		return fmt.Errorf("copilot: authorization is still pending")
	}
	if strings.TrimSpace(f.credential) == "" {
		f.credential = copilot.ProviderID
	}
	f.baseURL = copilot.ChatBaseURL
	f.apiKey = ""
	f.step = providerFormStepAPIKey + 1
	if m.providers == nil {
		m.providers = map[string]settings.ActiveProviderSettings{}
	}
	for _, model := range models {
		provider := settings.ActiveProviderSettings{
			Kind:          settings.ProviderKindOpenAICompatible,
			Credential:    f.credential,
			BaseURL:       copilot.ChatBaseURL,
			Model:         model,
			ContextWindow: copilot.ContextWindowForModel(model, 0),
		}
		if err := settings.SaveProviderEntry(provider); err != nil {
			return err
		}
		m.providers[settings.ProviderKey(provider)] = provider
	}
	return nil
}

func (m Model) ensureCopilotFallbackProviders(credential string) {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		credential = copilot.ProviderID
	}
	if _, err := settings.LoadStructuredProviderCredential(secure.NewDefault(), credential); err != nil {
		return
	}
	for _, provider := range m.providers {
		if isCopilotProvider(provider) && provider.Credential == credential {
			return
		}
	}
	if m.providers == nil {
		m.providers = map[string]settings.ActiveProviderSettings{}
	}
	for _, model := range copilot.FallbackModels() {
		provider := settings.ActiveProviderSettings{
			Kind:          settings.ProviderKindOpenAICompatible,
			Credential:    credential,
			BaseURL:       copilot.ChatBaseURL,
			Model:         model.ID,
			ContextWindow: copilot.ContextWindowForModel(model.ID, model.ContextWindow),
		}
		_ = settings.SaveProviderEntry(provider)
		m.providers[settings.ProviderKey(provider)] = provider
	}
}

func (m Model) ensureCodexFallbackProviders(credential string) {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		credential = codex.ProviderID
	}
	if _, err := settings.LoadStructuredProviderCredential(secure.NewDefault(), credential); err != nil {
		return
	}
	for _, provider := range m.providers {
		if isCodexProvider(provider) && provider.Credential == credential {
			return
		}
	}
	if m.providers == nil {
		m.providers = map[string]settings.ActiveProviderSettings{}
	}
	for _, model := range codex.FallbackModels() {
		provider := settings.ActiveProviderSettings{
			Kind:          settings.ProviderKindOpenAICompatible,
			Credential:    credential,
			BaseURL:       codex.CodexBaseURL,
			Model:         model.ID,
			ContextWindow: codex.ContextWindowForModel(model.ID, model.ContextWindow),
		}
		_ = settings.SaveProviderEntry(provider)
		m.providers[settings.ProviderKey(provider)] = provider
	}
}

func gracefulCopilotError(err error) string {
	if errors.Is(err, copilot.ErrNotAvailable) {
		return "GitHub authorized, but this account does not have Copilot API access."
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "GitHub Copilot authorization timed out. Press Esc and try again."
	}
	return err.Error()
}

func gracefulCodexError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "ChatGPT / Codex authorization timed out. Press Esc and try again."
	}
	return err.Error()
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
	labels := providerDetailActionLabelsFor(row.provider)
	ids := providerDetailActionsFor(row.provider)
	if p.providerAction >= len(ids) {
		p.providerAction = 0
	}
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

	if f.step == providerFormStepOAuth {
		name := "GitHub Copilot"
		if f.oauthKind == "codex" {
			name = "ChatGPT / Codex"
		}
		sb.WriteString(accent.Render("  "+name+" Authorization") + "\n\n")
		if f.oauthStarting {
			sb.WriteString(dim.Render("  Preparing " + name + " connection..."))
			return
		}
		if f.oauthKind == "codex" {
			sb.WriteString(dim.Render("  Complete authorization in your browser.") + "\n")
			sb.WriteString(dim.Render("  Waiting for authorization...") + "\n")
		} else if f.oauthInfo != nil {
			sb.WriteString(fmt.Sprintf("  Please visit: %s\n", f.oauthInfo.VerificationURI))
			sb.WriteString(fmt.Sprintf("  And enter code: %s\n\n", f.oauthInfo.UserCode))
			sb.WriteString(dim.Render("  Waiting for authorization and model discovery...") + "\n")
		}
		if f.err != "" {
			sb.WriteString("\n" + errStyle.Render("  "+f.err) + "\n")
			sb.WriteString(dim.Render("  Esc cancel"))
		}
		return
	}

	// ── Picker step ──────────────────────────────────────────────────────────
	if f.step == providerFormStepPicker {
		sb.WriteString(accent.Render("  Add Provider") + "\n\n")
		store := secure.NewDefault()
		for i, p := range knownProviders {
			isSel := i == f.pickerIdx
			cursor := "  "
			if isSel {
				cursor = accent.Render("❯ ")
			}
			nameStyle := fg
			if isSel {
				nameStyle = accent
			}
			suffix := ""
			if p.isOAuth {
				suffix = "  " + dim.Render("OAuth")
			} else if providerauth.IsConnected(store, p.id) {
				suffix = "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Render("api key ✓")
			}
			sb.WriteString(cursor + nameStyle.Render(p.displayName) + suffix + "\n")
		}
		// "Custom / other" row
		isSel := f.pickerIdx == len(knownProviders)
		cursor := "  "
		if isSel {
			cursor = accent.Render("❯ ")
		}
		customStyle := fg
		if isSel {
			customStyle = accent
		}
		sb.WriteString(cursor + customStyle.Render("Custom / other") + "\n")
		sb.WriteString("\n" + dim.Render("  ↑/↓ navigate · Enter select · Esc cancel"))
		return
	}

	// ── Manual form steps ────────────────────────────────────────────────────
	if f.oauthProvider {
		name := "GitHub Copilot"
		if f.oauthKind == "codex" {
			name = "ChatGPT / Codex"
		}
		title := "Add " + name + " Account"
		footer := "  Enter connect · Esc cancel"
		if f.editKey != "" {
			title = "Rename " + name + " Account"
			footer = "  Enter save · Esc cancel"
		}
		sb.WriteString(accent.Render("  "+title) + "\n\n")
		value := f.credential
		prefix := "  "
		labelStyle := fg
		valueStyle := fg
		if f.step == providerFormStepCredential {
			prefix = accent.Render("❯ ")
			labelStyle = accent
			valueStyle = accent
			value = f.input
		}
		sb.WriteString(prefix + labelStyle.Render("Credential alias: ") + valueStyle.Render(value) + "\n")
		if f.err != "" {
			sb.WriteString("\n" + errStyle.Render("  "+f.err) + "\n")
		}
		sb.WriteString("\n" + dim.Render(footer) + "\n")
		return
	}

	labels := []string{
		"Credential alias (not API key)",
		"Base URL",
		"API key",
	}
	// Map providerFormStep values to labels indices (skip providerFormStepPicker=0).
	stepOffset := int(providerFormStepCredential)

	title := "Add OpenAI-compatible Provider"
	if f.editKey != "" {
		title = "Edit OpenAI-compatible Provider"
	}
	if f.tokenOnly {
		title = "Change Provider API Key"
	}
	sb.WriteString(accent.Render("  "+title) + "\n\n")
	for i, label := range labels {
		step := providerFormStep(i + stepOffset)
		value := providerFormValue(f, step)
		// API key display: mask or show stored placeholder.
		if step == providerFormStepAPIKey {
			if value == "" && (f.editKey != "" || f.apiKey != "") {
				value = "(stored securely)"
			} else if value != "" && value != "(stored securely)" {
				value = strings.Repeat("•", min(len(value), 12))
			}
		}
		prefix := "  "
		if step == f.step {
			prefix = accent.Render("❯ ")
			value = f.input
			if step == providerFormStepAPIKey && value != "" {
				value = strings.Repeat("•", min(len(value), 12))
			}
		}
		labelStyle := fg
		valueStyle := fg
		if step == f.step {
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
	seenAccounts := map[string]bool{}
	for _, key := range keys {
		provider := m.providers[key]
		if provider.Kind == settings.ProviderKindOpenAICompatible {
			accountKey := provider.Credential + "\x00" + provider.BaseURL
			if seenAccounts[accountKey] {
				continue
			}
			seenAccounts[accountKey] = true
		}
		rows = append(rows, providerPanelRow{key: key, provider: provider})
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

func providerAccountRowKey(rows []providerPanelRow, credential, baseURL string) string {
	for _, row := range rows {
		if sameProviderAccount(row.provider, credential, baseURL) {
			return row.key
		}
	}
	return ""
}

func providerDetailActions() []string {
	return []string{"token", "edit", "delete", "back"}
}

func providerDetailActionLabels() []string {
	return []string{"Change API key", "Edit provider", "Delete provider", "Back"}
}

func providerDetailActionsFor(provider settings.ActiveProviderSettings) []string {
	if isCopilotProvider(provider) || isCodexProvider(provider) {
		return []string{"edit", "delete", "back"}
	}
	return providerDetailActions()
}

func providerDetailActionLabelsFor(provider settings.ActiveProviderSettings) []string {
	if isCopilotProvider(provider) {
		return []string{"Rename GitHub Copilot account", "Disconnect GitHub Copilot", "Back"}
	}
	if isCodexProvider(provider) {
		return []string{"Rename ChatGPT / Codex account", "Disconnect ChatGPT / Codex", "Back"}
	}
	return providerDetailActionLabels()
}

func (m Model) deleteProviderRow(row providerPanelRow) {
	keys := []string{row.key}
	if row.provider.Kind == settings.ProviderKindOpenAICompatible {
		keys = keys[:0]
		accountKey := row.provider.Credential + "\x00" + row.provider.BaseURL
		for key, provider := range m.providers {
			if provider.Kind == settings.ProviderKindOpenAICompatible && provider.Credential+"\x00"+provider.BaseURL == accountKey {
				keys = append(keys, key)
			}
		}
		if len(keys) == 0 {
			keys = append(keys, row.key)
		}
	}
	for _, key := range keys {
		if err := settings.DeleteProviderEntry(key); err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: "Delete provider failed: " + err.Error()})
			return
		}
		delete(m.providers, key)
	}
	for role, ref := range m.roles {
		for _, key := range keys {
			if ref == key {
				delete(m.roles, role)
				break
			}
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

func isCopilotProvider(provider settings.ActiveProviderSettings) bool {
	return strings.HasPrefix(provider.Credential, copilot.ProviderID) || strings.Contains(strings.ToLower(provider.BaseURL), "api.githubcopilot.com")
}

func isCodexProvider(provider settings.ActiveProviderSettings) bool {
	return strings.HasPrefix(provider.Credential, codex.ProviderID) || strings.Contains(strings.ToLower(provider.BaseURL), "chatgpt.com/backend-api/codex")
}
