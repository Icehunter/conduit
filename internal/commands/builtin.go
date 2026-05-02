package commands

import (
	"fmt"
	"strings"

	internalmodel "github.com/icehunter/claude-go/internal/model"
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
	r.Register(Command{
		Name:        "model",
		Description: "Show or switch the active model (/model [name])",
		Handler: func(args string) Result {
			args = strings.TrimSpace(args)
			if args == "" {
				return Result{Type: "text", Text: fmt.Sprintf("Current model: %s\n\nAvailable models:\n  claude-opus-4-7\n  claude-sonnet-4-6\n  claude-haiku-4-5-20251001", getModel())}
			}
			// Normalise shorthand names.
			name := resolveModelName(args)
			setModel(name)
			internalmodel.SetOverride(name)
			return Result{Type: "model", Model: name, Text: fmt.Sprintf("Switched to %s", name)}
		},
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
			sb.WriteString(fmt.Sprintf("  %-12s %s\n", "/"+cmd.Name, cmd.Description))
		}
		return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
	}
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
