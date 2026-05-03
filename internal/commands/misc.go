package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/theme"
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

	// /theme — switch active palette and persist to settings.json
	r.Register(Command{
		Name:        "theme",
		Description: "Switch theme. Names: " + strings.Join(theme.AvailableThemes(), " | "),
		Handler: func(args string) Result {
			name := strings.TrimSpace(args)
			if name == "" {
				return Result{Type: "text", Text: themeStatusText("Current theme")}
			}
			if !theme.Set(name) {
				return Result{Type: "text", Text: fmt.Sprintf(
					"Unknown theme %q. Available: %s",
					name, strings.Join(theme.AvailableThemes(), ", "),
				)}
			}
			persistErr := settings.SaveRawKey("theme", theme.Active().Name)
			head := fmt.Sprintf("Theme switched to %s", theme.Active().Name)
			if persistErr != nil {
				head += fmt.Sprintf(" (failed to persist: %v)", persistErr)
			} else {
				head += " and saved"
			}
			return Result{Type: "text", Text: themeStatusText(head)}
		},
	})
}

// themeStatusText renders a heading + the active palette as a color swatch
// so the user can SEE what just changed (otherwise theme-vs-theme differences
// like dark vs dark-accessible are subtle and only show in /doctor).
func themeStatusText(headline string) string {
	p := theme.Active()
	swatch := func(label, hex string) string {
		return fmt.Sprintf("  %s%-14s%s %s███%s %s",
			ansiBold, label+":", ansiReset,
			theme.AnsiFG(hex), ansiReset, hex)
	}
	var sb strings.Builder
	sb.WriteString(ansiBold + headline + " (" + p.Name + ")" + ansiReset + "\n\n")
	sb.WriteString(swatch("Primary", p.Primary) + "\n")
	sb.WriteString(swatch("Secondary", p.Secondary) + "\n")
	sb.WriteString(swatch("Accent", p.Accent) + "\n")
	sb.WriteString(swatch("Success", p.Success) + "\n")
	sb.WriteString(swatch("Danger", p.Danger) + "\n")
	sb.WriteString(swatch("Warning", p.Warning) + "\n")
	sb.WriteString(swatch("Info", p.Info) + "\n")
	sb.WriteString("\n  " + ansiDim + "Note: already-rendered messages keep their original colors." + ansiReset + "\n")
	sb.WriteString("  " + ansiDim + "Available: dark, light, dark-accessible, light-accessible" + ansiReset)
	return sb.String()
}
