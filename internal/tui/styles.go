package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent  = lipgloss.Color("#DA7756") // coral — Claude brand
	colorMuted   = lipgloss.Color("#555F6E") // darker gray for secondary text
	colorDim     = lipgloss.Color("#3D4554") // very dim for separators
	colorError   = lipgloss.Color("#F87171") // red
	colorTool    = lipgloss.Color("#60A5FA") // blue
	colorCode    = lipgloss.Color("#A3E635") // green
	colorFg      = lipgloss.Color("#D4D8E0") // primary text
	colorCodeBg  = lipgloss.Color("#1A1E2A") // code block background
)

var (
	// Message prefixes — compact, single-line
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

	// Code
	styleCodeBlock = lipgloss.NewStyle().
			Foreground(colorCode).
			Background(colorCodeBg).
			PaddingLeft(2).
			PaddingRight(2).
			PaddingTop(0).
			PaddingBottom(0)

	styleInlineCode = lipgloss.NewStyle().
				Foreground(colorCode)

	// Input box — no padding inside, border does the framing
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

	// Spinner
	styleSpinner = lipgloss.NewStyle().
			Foreground(colorAccent)

	// Separator line between messages
	styleSep = lipgloss.NewStyle().
			Foreground(colorDim)
)
