package commands

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/icehunter/conduit/internal/browser"
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

	// /login [--switch <email>]
	r.Register(Command{
		Name:        "login",
		Description: "Sign in or add an account (/login --switch <email> to switch active account)",
		Handler: func(args string) Result {
			args = strings.TrimSpace(args)
			if strings.HasPrefix(args, "--switch") {
				email := strings.TrimSpace(strings.TrimPrefix(args, "--switch"))
				return Result{Type: "account-switch", Text: email}
			}
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

	// /theme — switch active palette and persist to conduit.json
	r.Register(Command{
		Name:        "theme",
		Description: "Switch theme. Names: " + strings.Join(theme.AvailableThemes(), " | "),
		Handler: func(args string) Result {
			name := strings.TrimSpace(args)
			if name == "" {
				// Open picker: lets the user see and choose any registered
				// theme (built-ins + user-defined from settings.json).
				return pickerResult("theme", "Pick a theme", theme.Active().Name, theme.AvailableThemes())
			}
			if !theme.Set(name) {
				return Result{Type: "text", Text: fmt.Sprintf(
					"Unknown theme %q. Available: %s",
					name, strings.Join(theme.AvailableThemes(), ", "),
				)}
			}
			persistErr := settings.SaveConduitTheme(theme.Active().Name)
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

// RegisterFeedbackCommand adds /feedback that opens a pre-filled GitHub issue
// in the user's default browser. version is shown in the issue body.
func RegisterFeedbackCommand(r *Registry, version string) {
	r.Register(Command{
		Name:        "feedback",
		Description: "Open a GitHub issue to share feedback (/feedback <text>)",
		Handler: func(args string) Result {
			text := strings.TrimSpace(args)
			if text == "" {
				return Result{Type: "text", Text: "Usage: /feedback <text>\nExample: /feedback pressing X causes Y"}
			}
			issueURL := buildFeedbackURL(text, version)
			if err := browser.Open(issueURL); err != nil {
				return Result{Type: "text", Text: fmt.Sprintf(
					"Could not open browser automatically.\nPlease open this URL manually:\n%s", issueURL,
				)}
			}
			return Result{Type: "text", Text: "Opening browser to create GitHub issue…"}
		},
	})
}

// buildFeedbackURL constructs a GitHub new-issue URL pre-filled with the
// feedback text and version metadata. The title is truncated to 60 runes.
func buildFeedbackURL(text, version string) string {
	title := text
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:60])
	}
	body := text + "\n\n---\nVersion: " + version + " | OS: " + runtime.GOOS
	return "https://github.com/icehunter/conduit/issues/new" +
		"?title=" + url.QueryEscape("[Feedback] "+title) +
		"&body=" + url.QueryEscape(body) +
		"&labels=" + url.QueryEscape("feedback")
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
