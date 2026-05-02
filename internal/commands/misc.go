package commands

import (
	"fmt"
	"os"
	"strings"
)

// RegisterMiscCommands adds the remaining simple slash commands.
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

	// /branch
	r.Register(Command{
		Name:        "branch",
		Description: "Branch the conversation at this point (not yet implemented)",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Conversation branching is not yet implemented."}
		},
	})

	// /color
	r.Register(Command{
		Name:        "color",
		Description: "Change the accent color of the prompt bar",
		Handler: func(args string) Result {
			return Result{Type: "text", Text: "Color customization is not yet implemented.\nThe TUI uses a coral accent (#DA7756) by default."}
		},
	})

	// /desktop
	r.Register(Command{
		Name:        "desktop",
		Description: "Continue this conversation in Claude Desktop",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Claude Desktop integration is not yet implemented."}
		},
	})

	// /ide
	r.Register(Command{
		Name:        "ide",
		Description: "Manage IDE integrations (VS Code, JetBrains)",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "IDE integration is not yet implemented (M10)."}
		},
	})

	// /mobile
	r.Register(Command{
		Name:        "mobile",
		Description: "Show QR code to continue on mobile",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Mobile continuation is not yet implemented."}
		},
	})

	// /output-style
	r.Register(Command{
		Name:        "output-style",
		Description: "Change the output style (deprecated — use /theme)",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Output style customization is not yet implemented. Use /theme."}
		},
	})

	// /privacy-settings
	r.Register(Command{
		Name:        "privacy-settings",
		Description: "View privacy settings and data usage",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Privacy settings: https://www.anthropic.com/privacy\n\nThis binary sends your messages to api.anthropic.com/v1/messages only.\nNo analytics, telemetry, or third-party data sharing."}
		},
	})

	// /session
	r.Register(Command{
		Name:        "session",
		Description: "Show current session information",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Remote session sharing is not yet implemented (M10)."}
		},
	})

	// /tag
	r.Register(Command{
		Name:        "tag",
		Description: "Add a searchable tag to this conversation",
		Handler: func(args string) Result {
			tag := strings.TrimSpace(args)
			if tag == "" {
				return Result{Type: "error", Text: "Usage: /tag <name>"}
			}
			return Result{Type: "text", Text: fmt.Sprintf("Tag %q noted (session transcript tagging not yet persisted).", tag)}
		},
	})

	// /stickers
	r.Register(Command{
		Name:        "stickers",
		Description: "Order Claude Code stickers",
		Handler: func(string) Result {
			url := "https://anthropic.com/stickers"
			openBrowser(url)
			return Result{Type: "text", Text: "Opening: " + url}
		},
	})

	// /upgrade
	r.Register(Command{
		Name:        "upgrade",
		Description: "Upgrade to Claude Max for higher limits",
		Handler: func(string) Result {
			url := "https://claude.ai/upgrade"
			openBrowser(url)
			return Result{Type: "text", Text: "Opening: " + url}
		},
	})

	// /plan
	r.Register(Command{
		Name:        "plan",
		Description: "Enter plan mode — explore and design before coding",
		Handler: func(string) Result {
			return Result{Type: "text", Text: "Plan mode is not yet implemented (M5.2)."}
		},
	})

	// /login
	r.Register(Command{
		Name:        "login",
		Description: "Sign in or switch Anthropic accounts",
		Handler: func(string) Result {
			return Result{Type: "login"}
		},
	})
}
