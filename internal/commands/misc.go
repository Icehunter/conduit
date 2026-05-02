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

	// /plan — analysis-only mode: Claude proposes but does not edit
	r.Register(Command{
		Name:        "plan",
		Description: "Enter plan mode — Claude analyzes and proposes but makes no changes",
		Handler: func(args string) Result {
			return Result{
				Type: "prompt",
				Text: "Enter plan mode. You are now in PLAN MODE. In this mode:\n" +
					"- Analyze the codebase and understand the problem\n" +
					"- Propose a detailed implementation plan\n" +
					"- Do NOT make any edits to files\n" +
					"- Do NOT run commands that modify state\n" +
					"- Present your plan and wait for approval before doing anything\n" +
					"Acknowledge that you are in plan mode and ask what the user wants to plan.",
			}
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
