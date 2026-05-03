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

	// styleAppSurface is used by paintApp's outer wrap. Background is set
	// only when the active palette has a Background value (light themes).
	if p.Background != "" {
		styleAppSurface = lipgloss.NewStyle().
			Background(lipgloss.Color(p.Background)).
			Foreground(colorFg)
	} else {
		styleAppSurface = lipgloss.NewStyle().Foreground(colorFg)
	}
	styleModalSurface = lipgloss.NewStyle().Foreground(colorFg)

	// Foreground-only theming. Terminal bg shows through everywhere except
	// code blocks (which keep their own bg for visual differentiation).
	styleYouPrefix = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleClaudePrefix = lipgloss.NewStyle().Foreground(colorMuted)
	styleUserText = lipgloss.NewStyle().Foreground(colorFg)
	styleAssistantText = lipgloss.NewStyle().Foreground(colorFg)
	styleToolBadge = lipgloss.NewStyle().Foreground(colorTool).Bold(true)
	styleToolContent = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleErrorText = lipgloss.NewStyle().Foreground(colorError)
	styleSystemText = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleInlineCode = lipgloss.NewStyle().Foreground(lipgloss.Color("#79C0FF")).Background(colorCodeBg)
	styleCodeBorder = lipgloss.NewStyle().PaddingLeft(2).Background(colorCodeBg)
	styleCodeLang = lipgloss.NewStyle().Foreground(colorMuted).Background(colorCodeBg)

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
	// Bold-only highlight for matched query characters (no underline — too noisy in autocomplete).
	stylePickerHighlight = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
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

// registerThemeAwareWidgets wires the Model's stateful widgets (textarea,
// spinner) into the theme listener so theme switches re-apply their cached
// styles immediately — without these, bubbles widgets keep their stale
// colors until the process restarts.
//
// Implementation lives in model.go (where the bubbles imports are).
func registerThemeAwareWidgets(m *Model) {
	theme.OnChange(func() {
		applyTextareaTheme(&m.input)
		m.spinner.Style = styleSpinner
	})
}
