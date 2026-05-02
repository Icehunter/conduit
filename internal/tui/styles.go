package tui

import "github.com/charmbracelet/lipgloss"

// Palette — matches Claude Code's dark-terminal aesthetic.
var (
	colorAccent    = lipgloss.Color("#DA7756") // coral/salmon (Claude brand)
	colorMuted     = lipgloss.Color("#6C7280") // gray for hints/status
	colorError     = lipgloss.Color("#F87171") // red for errors
	colorToolLabel = lipgloss.Color("#60A5FA") // blue for tool names
	colorCode      = lipgloss.Color("#A3E635") // green for inline code
)

var (
	styleUser = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E2E8F0"))

	styleAssistant = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E2E8F0"))

	styleToolLabel = lipgloss.NewStyle().
			Foreground(colorToolLabel).
			Bold(true)

	styleError = lipgloss.NewStyle().
			Foreground(colorError)

	stylePromptCursor = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	styleStatusLine = lipgloss.NewStyle().
			Foreground(colorMuted)

	styleUserPrefix = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	styleAssistantPrefix = lipgloss.NewStyle().
				Foreground(colorMuted)

	styleCodeBlock = lipgloss.NewStyle().
			Foreground(colorCode).
			Background(lipgloss.Color("#1E2433")).
			Padding(0, 1)

	styleSpinner = lipgloss.NewStyle().
			Foreground(colorAccent)

	styleInputBox = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted).
			Padding(0, 1)

	styleInputBoxActive = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				Padding(0, 1)
)
