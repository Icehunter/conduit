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
	colorAccent  lipgloss.Color
	colorMuted   lipgloss.Color
	colorDim     lipgloss.Color
	colorError   lipgloss.Color
	colorTool    lipgloss.Color
	colorFg      lipgloss.Color
	colorBg      lipgloss.Color
	colorModalBg lipgloss.Color
	colorCodeBg  lipgloss.Color
	colorBorder  lipgloss.Color
)

// styleAppSurface paints the entire TUI region with the theme background.
// View() wraps its top-level output in this so empty space and padding
// gaps fill with bg color instead of showing through to the terminal.
var styleAppSurface lipgloss.Style

// styleModalSurface paints panel interiors with a slightly distinct bg.
var styleModalSurface lipgloss.Style

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
	colorBg = lipgloss.Color(p.Background)
	colorModalBg = lipgloss.Color(p.ModalBg)
	colorCodeBg = lipgloss.Color(p.CodeBg)
	colorBorder = lipgloss.Color(p.Border)

	styleAppSurface = lipgloss.NewStyle().Background(colorBg).Foreground(colorFg)
	styleModalSurface = lipgloss.NewStyle().Background(colorModalBg).Foreground(colorFg)

	// Every foreground style chains .Background(colorBg) so it inherits
	// the app surface and doesn't punch holes through to terminal default.
	styleYouPrefix = lipgloss.NewStyle().Foreground(colorAccent).Background(colorBg).Bold(true)
	styleClaudePrefix = lipgloss.NewStyle().Foreground(colorMuted).Background(colorBg)
	styleUserText = lipgloss.NewStyle().Foreground(colorFg).Background(colorBg)
	styleAssistantText = lipgloss.NewStyle().Foreground(colorFg).Background(colorBg)
	styleToolBadge = lipgloss.NewStyle().Foreground(colorTool).Background(colorBg).Bold(true)
	styleToolContent = lipgloss.NewStyle().Foreground(colorMuted).Background(colorBg).Italic(true)
	styleErrorText = lipgloss.NewStyle().Foreground(colorError).Background(colorBg)
	styleSystemText = lipgloss.NewStyle().Foreground(colorMuted).Background(colorBg).Italic(true)
	styleInlineCode = lipgloss.NewStyle().Foreground(lipgloss.Color("#79C0FF")).Background(colorCodeBg)
	styleCodeBorder = lipgloss.NewStyle().PaddingLeft(2).Background(colorCodeBg)
	styleCodeLang = lipgloss.NewStyle().Foreground(colorMuted).Background(colorCodeBg)

	styleInputBorder = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Background(colorBg).
		BorderBackground(colorBg).
		PaddingLeft(1).PaddingRight(1)
	styleInputBorderActive = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Background(colorBg).
		BorderBackground(colorBg).
		PaddingLeft(1).PaddingRight(1)

	styleStatus = lipgloss.NewStyle().Foreground(colorDim).Background(colorBg)
	styleStatusModel = lipgloss.NewStyle().Foreground(colorMuted).Background(colorBg)
	styleStatusAccent = lipgloss.NewStyle().Foreground(colorAccent).Background(colorBg).Bold(true)

	styleModePurple = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ModeAcceptEdits)).Background(colorBg).Bold(true)
	styleModeCyan = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ModePlan)).Background(colorBg).Bold(true)
	styleModeYellow = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ModeAuto)).Background(colorBg).Bold(true)

	styleSpinner = lipgloss.NewStyle().Foreground(colorAccent).Background(colorBg)
	styleSep = lipgloss.NewStyle().Foreground(colorDim).Background(colorBg)

	// Pickers float as modals — use ModalBg for visual contrast.
	stylePickerBorder = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Background(colorModalBg).
		BorderBackground(colorBg).
		PaddingLeft(1).PaddingRight(1)
	stylePickerItem = lipgloss.NewStyle().Foreground(colorFg).Background(colorModalBg)
	stylePickerItemSelected = lipgloss.NewStyle().Foreground(colorAccent).Background(colorModalBg).Bold(true)
	stylePickerDesc = lipgloss.NewStyle().Foreground(colorMuted).Background(colorModalBg)
	stylePickerHighlight = lipgloss.NewStyle().Foreground(colorAccent).Background(colorModalBg).Bold(true).Underline(true)
}

func init() {
	RebuildStyles()
	theme.OnChange(RebuildStyles)
}

// fgOnBg returns a foreground-colored lipgloss style with the app bg set,
// so embedded escapes don't punch holes through the painted theme bg.
// Use this anywhere code currently builds an inline lipgloss.NewStyle().Foreground(c).
func fgOnBg(c lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c).Background(colorBg)
}

// fgOnModal is the same as fgOnBg but uses ModalBg — for content rendered
// inside settings/plugin panel boxes.
func fgOnModal(c lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c).Background(colorModalBg)
}

// fgOnCode is the same but uses CodeBg — for tokens inside code blocks.
func fgOnCode(c lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c).Background(colorCodeBg)
}
