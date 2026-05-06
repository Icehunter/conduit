package commands

import (
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/coordinator"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

// RegisterBuiltins adds the standard slash commands to r.
// compact and model commands that need external state are wired in the TUI.
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
}

// RegisterModelCommand adds /model with the current model name and a setter.
func RegisterModelCommand(r *Registry, getModel func() string, setModel func(string)) {
	modelHandler := func(args string) Result {
		args = strings.TrimSpace(args)
		if args == "" {
			values := []string{"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5-20251001"}
			labels := []string{
				"Opus 4.7   — most capable",
				"Sonnet 4.6 — balanced (default)",
				"Haiku 4.5  — fastest, cheapest",
			}
			return pickerResult("model", "Pick a model", getModel(), values, labels)
		}
		// Normalise shorthand names.
		name := resolveModelName(args)
		setModel(name)
		internalmodel.SetOverride(name)
		// Persist so the choice survives restart. Best-effort — surface
		// the failure in the message rather than swallowing it.
		suffix := ""
		if err := settings.SaveRawKey("model", name); err != nil {
			suffix = fmt.Sprintf(" (failed to persist: %v)", err)
		}
		return Result{Type: "model", Model: name, Text: fmt.Sprintf("Switched to %s%s", name, suffix)}
	}
	r.Register(Command{
		Name:        "model",
		Description: "Show or switch the active model (/model [name])",
		Handler:     modelHandler,
	})
	r.Register(Command{
		Name:        "models",
		Description: "Open the model picker",
		Handler:     modelHandler,
	})
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
