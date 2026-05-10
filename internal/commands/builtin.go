package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/coordinator"
	"github.com/icehunter/conduit/internal/mcp"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

// RegisterBuiltins adds the standard slash commands to r.
// compact and model commands that need external state are wired in the TUI.
// Callers that know the binary version should call RegisterFeedbackCommand(r, version)
// afterwards to override the placeholder registered here.
func RegisterBuiltins(r *Registry) {
	r.Register(Command{
		Name:        "help",
		Description: "Show available slash commands",
		Handler:     helpHandler(r),
	})
	r.Register(Command{
		Name:        "commands",
		Description: "Open the slash command picker",
		Handler:     func(string) Result { return Result{Type: "commands"} },
	})
	r.Register(Command{
		Name:        "?",
		Description: "Show keyboard shortcut help",
		Handler:     func(args string) Result { return Result{Type: "help_overlay", Text: strings.TrimSpace(args)} },
	})
	r.Register(Command{
		Name:        "clear",
		Description: "Clear the conversation history and start fresh",
		Handler:     func(string) Result { return Result{Type: "clear"} },
	})
	r.Register(Command{
		Name:        "exit",
		Description: "Exit claude",
		Handler:     func(string) Result { return Result{Type: "exit"} },
	})
	r.Register(Command{
		Name:        "quit",
		Description: "Exit claude",
		Handler:     func(string) Result { return Result{Type: "exit"} },
	})
	// Register /feedback with an empty version placeholder; callers that have
	// the real binary version (e.g. cmd/conduit) should call
	// RegisterFeedbackCommand(r, AppVersion) to replace this registration.
	RegisterFeedbackCommand(r, "")
}

// RegisterModelCommand adds /model with the current model name and a setter.
func RegisterModelCommand(
	r *Registry,
	getModel func() string,
	setModel func(string),
	getAccountProviders func() []settings.ActiveProviderSettings,
	manager *mcp.Manager,
	providers map[string]settings.ActiveProviderSettings,
) {
	modelHandler := func(args string) Result {
		args = strings.TrimSpace(args)

		// --refresh triggers an async catalog fetch, not the picker.
		if args == "--refresh" {
			return Result{Type: "catalog-refresh"}
		}

		role := settings.RoleDefault
		if strings.HasPrefix(args, "--role ") {
			rest := strings.TrimSpace(strings.TrimPrefix(args, "--role "))
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) > 0 && parts[0] != "" {
				role = normalizeProviderRole(parts[0])
			}
			args = ""
			if len(parts) > 1 {
				args = strings.TrimSpace(parts[1])
			}
		}
		if args == "" {
			accountProviders := currentAccountProviders(getAccountProviders)
			provider := firstAccountProvider(accountProviders)
			localItems := localModelPickerItems(manager, providers)
			customItems := customModelPickerItems(providers)
			items := make([]PickerOption, 0, len(accountProviders)*4+len(localItems)+len(customItems))
			for _, provider := range accountProviders {
				items = append(items, accountModelPickerItems(provider)...)
			}
			items = append(items, customItems...)
			items = append(items, localItems...)
			return pickerResultItems("model", "Switch Model", currentModelValue(getModel(), provider), items)
		}
		if args == "local" {
			args = "local:" + defaultLocalServer
		}
		if strings.HasPrefix(args, "local:") {
			server := strings.TrimSpace(strings.TrimPrefix(args, "local:"))
			if server == "" {
				server = defaultLocalServer
			}
			provider := configuredMCPProviderForServer(providers, server)
			if provider == nil {
				provider = &settings.ActiveProviderSettings{
					Kind:          settings.ProviderKindMCP,
					Server:        server,
					Model:         localModelName(manager, server),
					DirectTool:    defaultLocalDirectTool,
					ImplementTool: defaultLocalImplementTool,
				}
			}
			if provider.Model == "" {
				provider.Model = localModelName(manager, server)
			}
			if provider.DirectTool == "" {
				provider.DirectTool = defaultLocalDirectTool
			}
			if provider.ImplementTool == "" {
				provider.ImplementTool = defaultLocalImplementTool
			}
			return Result{
				Type:     "provider-switch",
				Role:     role,
				Provider: provider,
			}
		}
		// Normalise shorthand names.
		accountProviders := currentAccountProviders(getAccountProviders)
		provider := firstAccountProvider(accountProviders)
		if strings.HasPrefix(args, "provider:") {
			key := strings.TrimSpace(strings.TrimPrefix(args, "provider:"))
			if selected, ok := configuredProviderByKey(providers, key); ok {
				if selected.Kind == settings.ProviderKindOpenAICompatible {
					selected.Account = ""
				}
				if role == settings.RoleDefault && selected.Model != "" && selected.Kind != settings.ProviderKindOpenAICompatible {
					setModel(selected.Model)
					internalmodel.SetOverride(selected.Model)
				}
				return Result{
					Type:     "provider-switch",
					Model:    selected.Model,
					Role:     role,
					Text:     fmt.Sprintf("Switched to %s", selected.Model),
					Provider: &selected,
				}
			}
			if selected, ok := accountProviderByKey(accountProviders, key); ok {
				if role == settings.RoleDefault {
					setModel(selected.Model)
					internalmodel.SetOverride(selected.Model)
				}
				return Result{
					Type:     "provider-switch",
					Model:    selected.Model,
					Role:     role,
					Text:     fmt.Sprintf("Switched to %s", selected.Model),
					Provider: &selected,
				}
			}
		}
		if strings.HasPrefix(args, "anthropic-api:") {
			provider.Kind = settings.ProviderKindAnthropicAPI
			args = strings.TrimSpace(strings.TrimPrefix(args, "anthropic-api:"))
		} else if strings.HasPrefix(args, "claude-subscription:") {
			provider.Kind = settings.ProviderKindClaudeSubscription
			args = strings.TrimSpace(strings.TrimPrefix(args, "claude-subscription:"))
		}
		name := resolveModelName(args)
		if role == settings.RoleDefault {
			setModel(name)
			internalmodel.SetOverride(name)
		}
		return Result{
			Type:  "provider-switch",
			Model: name,
			Role:  role,
			Text:  fmt.Sprintf("Switched to %s", name),
			Provider: &settings.ActiveProviderSettings{
				Kind:    provider.Kind,
				Model:   name,
				Account: provider.Account,
			},
		}
	}
	r.Register(Command{
		Name:        "model",
		Description: "Show or switch the active model (/model [name] | --refresh)",
		Handler:     modelHandler,
	})
	r.Register(Command{
		Name:        "models",
		Description: "Open the model picker (/models | --refresh to update catalog)",
		Handler:     modelHandler,
	})
}

func currentAccountProviders(getAccountProviders func() []settings.ActiveProviderSettings) []settings.ActiveProviderSettings {
	if getAccountProviders == nil {
		return []settings.ActiveProviderSettings{{Kind: settings.ProviderKindClaudeSubscription}}
	}
	var out []settings.ActiveProviderSettings
	seen := map[string]bool{}
	for _, provider := range getAccountProviders() {
		switch provider.Kind {
		case settings.ProviderKindAnthropicAPI, settings.ProviderKindClaudeSubscription:
			key := provider.Kind + "\x00" + provider.Account
			if !seen[key] {
				out = append(out, settings.ActiveProviderSettings{
					Kind:    provider.Kind,
					Account: provider.Account,
				})
				seen[key] = true
			}
		}
	}
	return out
}

func firstAccountProvider(providers []settings.ActiveProviderSettings) settings.ActiveProviderSettings {
	if len(providers) == 0 {
		return settings.ActiveProviderSettings{}
	}
	return providers[0]
}

func accountProviderByKey(providers []settings.ActiveProviderSettings, key string) (settings.ActiveProviderSettings, bool) {
	for _, provider := range providers {
		for _, model := range accountModelNames() {
			candidate := provider
			candidate.Model = model
			if settings.ProviderKey(candidate) == key {
				return candidate, true
			}
		}
	}
	return settings.ActiveProviderSettings{}, false
}

func configuredProviderByKey(providers map[string]settings.ActiveProviderSettings, key string) (settings.ActiveProviderSettings, bool) {
	if len(providers) == 0 || key == "" {
		return settings.ActiveProviderSettings{}, false
	}
	if provider, ok := providers[key]; ok {
		return provider, true
	}
	for _, provider := range providers {
		if settings.ProviderKey(provider) == key {
			return provider, true
		}
	}
	return settings.ActiveProviderSettings{}, false
}

func accountModelPickerItems(provider settings.ActiveProviderSettings) []PickerOption {
	section := "Claude Subscription"
	if provider.Kind == settings.ProviderKindAnthropicAPI {
		section = "Anthropic API"
	}
	if provider.Account != "" {
		section += " · " + provider.Account
	}
	models := accountModelNames()
	return []PickerOption{
		{Label: section, Section: true},
		{Value: providerPickerValue(provider, models[0]), Label: "Opus 4.7   — most capable"},
		{Value: providerPickerValue(provider, models[1]), Label: "Sonnet 4.6 — balanced (default)"},
		{Value: providerPickerValue(provider, models[2]), Label: "Haiku 4.5  — fastest, cheapest"},
	}
}

func accountModelNames() []string {
	return []string{"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5-20251001"}
}

func customModelPickerItems(providers map[string]settings.ActiveProviderSettings) []PickerOption {
	if len(providers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(providers))
	for key := range providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]PickerOption, 0, len(keys)*2)
	for _, key := range keys {
		provider := providers[key]
		if provider.Model == "" {
			continue
		}
		label, ok := customModelPickerSection(provider)
		if !ok {
			continue
		}
		items = append(items, PickerOption{Label: label, Section: true})
		items = append(items, PickerOption{Value: "provider:" + settings.ProviderKey(provider), Label: provider.Model})
	}
	return items
}

func customModelPickerSection(provider settings.ActiveProviderSettings) (string, bool) {
	switch provider.Kind {
	case settings.ProviderKindOpenAICompatible:
		label := "OpenAI-compatible"
		if provider.Credential != "" {
			label += " · credential " + provider.Credential
		}
		return label, true
	case settings.ProviderKindAnthropicAPI:
		if provider.Credential == "" || provider.Account != "" {
			return "", false
		}
		return "Anthropic API · credential " + provider.Credential, true
	default:
		return "", false
	}
}

func providerPickerValue(provider settings.ActiveProviderSettings, model string) string {
	if provider.Kind == settings.ProviderKindOpenAICompatible {
		return "provider:" + settings.ProviderKey(provider)
	}
	if provider.Account != "" {
		provider.Model = model
		return "provider:" + settings.ProviderKey(provider)
	}
	if provider.Kind == settings.ProviderKindAnthropicAPI {
		return "anthropic-api:" + model
	}
	return "claude-subscription:" + model
}

func currentModelValue(model string, provider settings.ActiveProviderSettings) string {
	if strings.HasPrefix(model, "local:") {
		return model
	}
	if provider.Kind == "" {
		return model
	}
	return providerPickerValue(provider, model)
}

func normalizeProviderRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case settings.RoleDefault:
		return settings.RoleDefault
	case settings.RoleMain:
		return settings.RoleMain
	case settings.RoleBackground, "bg":
		return settings.RoleBackground
	case settings.RolePlanning, "plan":
		return settings.RolePlanning
	case settings.RoleImplement, "implementation":
		return settings.RoleImplement
	default:
		return settings.RoleDefault
	}
}

// RegisterCompactCommand adds /compact that callers implement by returning Type=="compact".
func RegisterCompactCommand(r *Registry) {
	r.Register(Command{
		Name:        "compact",
		Description: "Summarise conversation history to save context",
		Handler:     func(args string) Result { return Result{Type: "compact", Text: args} },
	})
}

func helpHandler(r *Registry) Handler {
	return func(string) Result {
		var sb strings.Builder
		sb.WriteString("Available slash commands:\n\n")
		for _, cmd := range r.All() {
			if cmd.Name == "quit" {
				continue // deduplicate exit/quit
			}
			fmt.Fprintf(&sb, "  %-12s %s\n", "/"+cmd.Name, cmd.Description)
		}
		return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
	}
}

// RegisterPermissionsCommand adds /permissions showing current mode and allow/deny/ask lists.
func RegisterPermissionsCommand(r *Registry, gate *permissions.Gate) {
	r.Register(Command{
		Name:        "permissions",
		Description: "Show current permission mode and allow/deny/ask rules",
		Handler: func(_ string) Result {
			if gate == nil {
				return Result{Type: "text", Text: "Permissions: no gate configured (all tools allowed)"}
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Permission mode: %s\n", gate.Mode())

			allow, deny, ask := gate.Lists()

			if len(allow) == 0 {
				sb.WriteString("\nAllow list: (empty)\n")
			} else {
				sb.WriteString("\nAllow list:\n")
				for _, r := range allow {
					fmt.Fprintf(&sb, "  %s\n", r)
				}
			}

			if len(deny) == 0 {
				sb.WriteString("\nDeny list: (empty)\n")
			} else {
				sb.WriteString("\nDeny list:\n")
				for _, r := range deny {
					fmt.Fprintf(&sb, "  %s\n", r)
				}
			}

			if len(ask) == 0 {
				sb.WriteString("\nAsk list: (empty)\n")
			} else {
				sb.WriteString("\nAsk list:\n")
				for _, r := range ask {
					fmt.Fprintf(&sb, "  %s\n", r)
				}
			}

			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		},
	})
}

// RegisterHooksCommand adds /hooks showing configured hook matchers.
func RegisterHooksCommand(r *Registry, hooksConfig *settings.HooksSettings) {
	r.Register(Command{
		Name:        "hooks",
		Description: "Show configured PreToolUse/PostToolUse/SessionStart/Stop hooks",
		Handler: func(_ string) Result {
			if hooksConfig == nil {
				return Result{Type: "text", Text: "Hooks: no hooks configured"}
			}

			var sb strings.Builder
			printMatchers := func(label string, matchers []settings.HookMatcher) {
				if len(matchers) == 0 {
					fmt.Fprintf(&sb, "%s: (none)\n", label)
					return
				}
				fmt.Fprintf(&sb, "%s:\n", label)
				for _, m := range matchers {
					matcher := m.Matcher
					if matcher == "" {
						matcher = "(all tools)"
					}
					for _, h := range m.Hooks {
						fmt.Fprintf(&sb, "  [%s] %s\n", matcher, h.Command)
					}
				}
			}

			printMatchers("PreToolUse", hooksConfig.PreToolUse)
			printMatchers("PostToolUse", hooksConfig.PostToolUse)
			printMatchers("SessionStart", hooksConfig.SessionStart)
			printMatchers("Stop", hooksConfig.Stop)

			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		},
	})
}

// RegisterCoordinatorCommand adds /coordinator that toggles coordinator mode on/off.
// When active, the agent acts as an orchestrator managing sub-agent workers.
func RegisterCoordinatorCommand(r *Registry) {
	r.Register(Command{
		Name:        "coordinator",
		Description: "Toggle coordinator mode — orchestrate tasks across multiple sub-agent workers",
		Handler: func(_ string) Result {
			coordinator.SetActive(!coordinator.IsActive())
			if coordinator.IsActive() {
				return Result{
					Type: "coordinator-toggle",
					Text: "Coordinator mode enabled. You are now an orchestrator. Use the Task tool to spawn workers and manage parallel workstreams.\n\nType /coordinator again to disable.",
				}
			}
			return Result{
				Type: "coordinator-toggle",
				Text: "Coordinator mode disabled. Back to standard agent mode.",
			}
		},
	})
}

// resolveModelName expands shorthand model names to their full API IDs.
func resolveModelName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "opus", "opus4", "opus-4", "opus4.7", "opus-4.7":
		return "claude-opus-4-7"
	case "sonnet", "sonnet4", "sonnet-4", "sonnet4.6", "sonnet-4.6":
		return "claude-sonnet-4-6"
	case "haiku", "haiku4", "haiku-4", "haiku4.5", "haiku-4.5":
		return "claude-haiku-4-5-20251001"
	}
	return s
}
