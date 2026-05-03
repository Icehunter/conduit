package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent = lipgloss.Color("#DA7756") // coral — Claude brand
	colorMuted  = lipgloss.Color("#636D7E") // gray for secondary text
	colorDim    = lipgloss.Color("#3D4554") // very dim for separators/chrome
	colorError  = lipgloss.Color("#F87171") // red
	colorTool   = lipgloss.Color("#60A5FA") // blue
	colorFg     = lipgloss.Color("#CDD6E0") // primary text (slightly cooler)
	colorCodeBg = lipgloss.Color("#0D1117") // code bg — GitHub dark
	colorBorder = lipgloss.Color("#30363D") // visible but subtle border
)

var (
	styleYouPrefix = lipgloss.NewStyle().
			Foreground(colorAccent).Bold(true)

	styleClaudePrefix = lipgloss.NewStyle().
				Foreground(colorMuted)

	styleUserText = lipgloss.NewStyle().
			Foreground(colorFg)

	styleAssistantText = lipgloss.NewStyle().
				Foreground(colorFg)

	styleToolBadge = lipgloss.NewStyle().
			Foreground(colorTool).Bold(true)

	styleToolContent = lipgloss.NewStyle().
				Foreground(colorMuted).Italic(true)

	styleErrorText = lipgloss.NewStyle().
			Foreground(colorError)

	styleSystemText = lipgloss.NewStyle().
			Foreground(colorMuted).Italic(true)

	styleInlineCode = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#79C0FF")) // soft blue for inline code

	// Code block — no border, no background, just a left indent to visually
	// separate it from prose. Syntax colors do the work.
	styleCodeBorder = lipgloss.NewStyle().
				PaddingLeft(2)

	// Language label — explicit NoColor background so it's transparent on the viewport.
	styleCodeLang = lipgloss.NewStyle().
			Foreground(colorMuted).
			Background(lipgloss.NoColor{})

	// Input borders
	styleInputBorder = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorDim).
				PaddingLeft(1).PaddingRight(1)

	styleInputBorderActive = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				PaddingLeft(1).PaddingRight(1)

	// Status bar segments
	styleStatus = lipgloss.NewStyle().
			Foreground(colorDim)

	styleStatusModel = lipgloss.NewStyle().
				Foreground(colorMuted)

	styleStatusAccent = lipgloss.NewStyle().
				Foreground(colorAccent).Bold(true)

	// Permission mode badges — each mode has its own color.
	styleModePurple = lipgloss.NewStyle().Foreground(lipgloss.Color("#C084FC")).Bold(true) // acceptEdits
	styleModeCyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true) // plan
	styleModeYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("#FDE047")).Bold(true) // auto/bypassPermissions

	styleSpinner = lipgloss.NewStyle().
			Foreground(colorAccent)

	styleSep = lipgloss.NewStyle().
			Foreground(colorDim)

	// Command picker
	stylePickerBorder = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				PaddingLeft(1).PaddingRight(1)

	stylePickerItem = lipgloss.NewStyle().
				Foreground(colorFg)

	stylePickerItemSelected = lipgloss.NewStyle().
					Foreground(colorAccent).Bold(true)

	stylePickerDesc = lipgloss.NewStyle().
				Foreground(colorMuted)

	stylePickerHighlight = lipgloss.NewStyle().
					Foreground(colorAccent).Bold(true).Underline(true)
)
