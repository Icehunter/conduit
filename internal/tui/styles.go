package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent = lipgloss.Color("#DA7756") // coral — Claude brand
	colorMuted  = lipgloss.Color("#555F6E") // gray for secondary text
	colorDim    = lipgloss.Color("#3D4554") // very dim for separators/status
	colorError  = lipgloss.Color("#F87171") // red
	colorTool   = lipgloss.Color("#60A5FA") // blue
	colorFg     = lipgloss.Color("#D4D8E0") // primary text
	colorCodeBg = lipgloss.Color("#141820") // code block background (darker)
)

var (
	// Message prefixes
	styleYouPrefix = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	styleClaudePrefix = lipgloss.NewStyle().
				Foreground(colorMuted)

	// Body text
	styleUserText = lipgloss.NewStyle().
			Foreground(colorFg)

	styleAssistantText = lipgloss.NewStyle().
				Foreground(colorFg)

	styleToolBadge = lipgloss.NewStyle().
			Foreground(colorTool).
			Bold(true)

	styleToolContent = lipgloss.NewStyle().
				Foreground(colorMuted).
				Italic(true)

	styleErrorText = lipgloss.NewStyle().
			Foreground(colorError)

	styleSystemText = lipgloss.NewStyle().
			Foreground(colorDim).
			Italic(true)

	// Inline code
	styleInlineCode = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A3E635"))

	// Code block: rounded border matching input, dark bg, 1-col inner padding
	styleCodeBorder = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorDim).
				Background(colorCodeBg).
				PaddingLeft(1).
				PaddingRight(1).
				PaddingTop(0).
				PaddingBottom(0)

	// Language label inside the top border
	styleCodeLang = lipgloss.NewStyle().
			Foreground(colorMuted).
			Background(colorCodeBg)

	// Input box borders
	styleInputBorder = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorDim).
				PaddingLeft(1).
				PaddingRight(1)

	styleInputBorderActive = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				PaddingLeft(1).
				PaddingRight(1)

	// Status bar
	styleStatus = lipgloss.NewStyle().
			Foreground(colorDim)

	styleStatusModel = lipgloss.NewStyle().
				Foreground(colorMuted)

	styleStatusAccent = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	// Spinner
	styleSpinner = lipgloss.NewStyle().
			Foreground(colorAccent)

	// Separator between messages
	styleSep = lipgloss.NewStyle().
			Foreground(colorDim)
)
