package commands

import (
	"fmt"
	"os"
	"strings"
)

// RegisterMiscCommands adds miscellaneous slash commands.
func RegisterMiscCommands(r *Registry) {
	// /add-dir
	r.Register(Command{
		Name:        "add-dir",
		Description: "Add a new working directory to the session",
		Handler: func(args string) Result {
			path := strings.TrimSpace(args)
			if path == "" {
				return Result{Type: "error", Text: "Usage: /add-dir <path>"}
			}
			if _, err := os.Stat(path); err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("Directory not found: %s", path)}
			}
			return Result{Type: "add-dir", Text: path}
		},
	})

	// /privacy-settings
	r.Register(Command{
		Name:        "privacy-settings",
		Description: "View privacy settings and data usage",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Privacy: https://www.anthropic.com/privacy\n\nThis binary sends your messages to api.anthropic.com/v1/messages only.\nNo analytics, telemetry, or third-party data sharing."}
		},
	})

	// /login
	r.Register(Command{
		Name:        "login",
		Description: "Sign in or switch accounts",
		Handler: func(string) Result {
			return Result{Type: "login"}
		},
	})

	// /plan — deferred, but honest about it
	r.Register(Command{
		Name:        "plan",
		Description: "Enter plan mode (coming soon)",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Plan mode is not yet implemented."}
		},
	})

	// /branch — deferred
	r.Register(Command{
		Name:        "branch",
		Description: "Branch the conversation at this point (coming soon)",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Conversation branching is not yet implemented."}
		},
	})

	// /theme — deferred
	r.Register(Command{
		Name:        "theme",
		Description: "Change the terminal theme (coming soon)",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Theme switching is not yet implemented."}
		},
	})
}
