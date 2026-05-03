package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/icehunter/conduit/internal/theme"
)

// All styles derive from theme.Active(). RebuildStyles() reassigns them
// when the theme changes — wired via theme.OnChange in init().
//
// Color name aliases kept for back-compat with existing render code:
//   colorAccent → palette.Accent
//   colorMuted  → palette.Secondary
//   colorDim    → palette.Tertiary
//   colorFg     → palette.Primary
//   colorError  → palette.Danger
//   colorTool   → palette.Info
//   colorBorder → palette.Border
//   colorCodeBg → palette.CodeBg
var (
	colorAccent lipgloss.Color
	colorMuted  lipgloss.Color
	colorDim    lipgloss.Color
	colorError  lipgloss.Color
	colorTool   lipgloss.Color
	colorFg     lipgloss.Color
	colorCodeBg lipgloss.Color
	colorBorder lipgloss.Color
)

var (
	styleYouPrefix         lipgloss.Style
	styleClaudePrefix      lipgloss.Style
	styleUserText          lipgloss.Style
	styleAssistantText     lipgloss.Style
	styleToolBadge         lipgloss.Style
	styleToolContent       lipgloss.Style
	styleErrorText         lipgloss.Style
	styleSystemText        lipgloss.Style
	styleInlineCode        lipgloss.Style
	styleCodeBorder        lipgloss.Style
	styleCodeLang          lipgloss.Style
	styleInputBorder       lipgloss.Style
	styleInputBorderActive lipgloss.Style
	styleStatus            lipgloss.Style
	styleStatusModel       lipgloss.Style
	styleStatusAccent      lipgloss.Style
	styleModePurple        lipgloss.Style
	styleModeCyan          lipgloss.Style
	styleModeYellow        lipgloss.Style
	styleSpinner           lipgloss.Style
	styleSep               lipgloss.Style
	stylePickerBorder      lipgloss.Style
	stylePickerItem        lipgloss.Style
	stylePickerItemSelected lipgloss.Style
	stylePickerDesc        lipgloss.Style
	stylePickerHighlight   lipgloss.Style
)

// RebuildStyles regenerates every package-level style from theme.Active().
// Called at init() and after every theme switch via theme.OnChange.
func RebuildStyles() {
	p := theme.Active()

	colorAccent = lipgloss.Color(p.Accent)
	colorMuted = lipgloss.Color(p.Secondary)
	colorDim = lipgloss.Color(p.Tertiary)
	colorError = lipgloss.Color(p.Danger)
	colorTool = lipgloss.Color(p.Info)
	colorFg = lipgloss.Color(p.Primary)
	colorCodeBg = lipgloss.Color(p.CodeBg)
	colorBorder = lipgloss.Color(p.Border)

	styleYouPrefix = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleClaudePrefix = lipgloss.NewStyle().Foreground(colorMuted)
	styleUserText = lipgloss.NewStyle().Foreground(colorFg)
	styleAssistantText = lipgloss.NewStyle().Foreground(colorFg)
	styleToolBadge = lipgloss.NewStyle().Foreground(colorTool).Bold(true)
	styleToolContent = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleErrorText = lipgloss.NewStyle().Foreground(colorError)
	styleSystemText = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleInlineCode = lipgloss.NewStyle().Foreground(lipgloss.Color("#79C0FF"))
	styleCodeBorder = lipgloss.NewStyle().PaddingLeft(2)
	styleCodeLang = lipgloss.NewStyle().Foreground(colorMuted).Background(lipgloss.NoColor{})

	styleInputBorder = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		PaddingLeft(1).PaddingRight(1)
	styleInputBorderActive = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(1).PaddingRight(1)

	styleStatus = lipgloss.NewStyle().Foreground(colorDim)
	styleStatusModel = lipgloss.NewStyle().Foreground(colorMuted)
	styleStatusAccent = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	styleModePurple = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ModeAcceptEdits)).Bold(true)
	styleModeCyan = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ModePlan)).Bold(true)
	styleModeYellow = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ModeAuto)).Bold(true)

	styleSpinner = lipgloss.NewStyle().Foreground(colorAccent)
	styleSep = lipgloss.NewStyle().Foreground(colorDim)

	stylePickerBorder = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(1).PaddingRight(1)
	stylePickerItem = lipgloss.NewStyle().Foreground(colorFg)
	stylePickerItemSelected = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	stylePickerDesc = lipgloss.NewStyle().Foreground(colorMuted)
	stylePickerHighlight = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Underline(true)
}

func init() {
	RebuildStyles()
	theme.OnChange(RebuildStyles)
}
