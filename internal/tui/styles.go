package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/theme"
)

// All styles derive from theme.Active(). RebuildStyles() reassigns them
// when the theme changes — wired via theme.OnChange in init().
//
// Color name aliases kept for back-compat with existing render code:
//
//	colorAccent → palette.Accent
//	colorMuted  → palette.Secondary
//	colorDim    → palette.Tertiary
//	colorFg     → palette.Primary
//	colorError  → palette.Danger
//	colorTool   → palette.Info
//	colorBorder → palette.Border
var (
	colorAccent color.Color
	colorMuted  color.Color
	colorDim    color.Color
	colorError  color.Color
	colorTool   color.Color
	colorFg     color.Color

	colorWindowBorder color.Color
	colorWindowTitle  color.Color
	colorWindowBg     color.Color
	colorSelectionFg  color.Color
)

const windowBgHex = "#1F1E26"

// styleAppSurface paints the entire TUI region with the theme background.
// View() wraps its top-level output in this so empty space and padding
// gaps fill with bg color instead of showing through to the terminal.
var styleAppSurface lipgloss.Style

var (
	styleYouPrefix          lipgloss.Style
	styleClaudePrefix       lipgloss.Style
	styleUserText           lipgloss.Style
	styleAssistantText      lipgloss.Style
	styleToolBadge          lipgloss.Style
	styleErrorText          lipgloss.Style
	styleSystemText         lipgloss.Style
	styleInlineCode         lipgloss.Style
	styleCodeBorder         lipgloss.Style
	styleCodeLang           lipgloss.Style
	styleInputBorder        lipgloss.Style
	styleInputBorderActive  lipgloss.Style
	styleStatus             lipgloss.Style
	styleStatusAccent       lipgloss.Style
	styleStatusFaded        lipgloss.Style
	styleModePurple         lipgloss.Style
	styleModeCyan           lipgloss.Style
	styleModeYellow         lipgloss.Style
	styleModeGreen          lipgloss.Style
	styleSep                lipgloss.Style
	stylePickerItem         lipgloss.Style
	stylePickerItemSelected lipgloss.Style
	stylePickerDesc         lipgloss.Style
	stylePickerHighlight    lipgloss.Style
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
	colorWindowBorder = lipgloss.Color(gradientPurple)
	colorWindowTitle = lipgloss.Color(gradientBlue)
	colorWindowBg = lipgloss.Color(windowBgHex)
	colorSelectionFg = lipgloss.Color("#FFFFFF")

	styleAppSurface = lipgloss.NewStyle().
		Background(colorWindowBg).
		Foreground(colorFg)

	// Foreground-only theming. Terminal bg shows through everywhere except
	// code blocks (which keep their own bg for visual differentiation).
	styleYouPrefix = lipgloss.NewStyle().Foreground(colorAccent).Background(colorWindowBg).Bold(true)
	styleClaudePrefix = lipgloss.NewStyle().Foreground(colorMuted).Background(colorWindowBg)
	styleUserText = lipgloss.NewStyle().Foreground(colorFg).Background(colorWindowBg)
	styleAssistantText = lipgloss.NewStyle().Foreground(colorFg).Background(colorWindowBg)
	styleToolBadge = lipgloss.NewStyle().Foreground(colorTool).Background(colorWindowBg).Bold(true)
	styleErrorText = lipgloss.NewStyle().Foreground(colorError).Background(colorWindowBg)
	styleSystemText = lipgloss.NewStyle().Foreground(colorMuted).Background(colorWindowBg).Italic(true)
	styleInlineCode = lipgloss.NewStyle().Foreground(lipgloss.Color("#79C0FF"))
	styleCodeBorder = lipgloss.NewStyle().Background(colorWindowBg).PaddingLeft(2)
	styleCodeLang = lipgloss.NewStyle().Foreground(colorMuted).Background(colorWindowBg)

	// Input border: paint bg when theme has Background set (light themes)
	// so the padding cells inside the border don't expose terminal default
	// bg. Dark themes leave bg unset (terminal bg is what we want anyway).
	inputBorder := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Background(colorWindowBg).
		BorderBackground(colorWindowBg).
		PaddingLeft(1).PaddingRight(1)
	inputBorderActive := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorWindowBorder).
		Background(colorWindowBg).
		BorderBackground(colorWindowBg).
		PaddingLeft(1).PaddingRight(1)
	styleInputBorder = inputBorder
	styleInputBorderActive = inputBorderActive

	styleStatus = lipgloss.NewStyle().Foreground(colorDim).Background(colorWindowBg)
	styleStatusAccent = lipgloss.NewStyle().Foreground(colorWindowTitle).Background(colorWindowBg).Bold(true)
	styleStatusFaded = lipgloss.NewStyle().Foreground(colorMuted).Background(colorWindowBg).Italic(true)

	styleModePurple = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ModeAcceptEdits)).Background(colorWindowBg).Bold(true)
	styleModeCyan = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ModePlan)).Background(colorWindowBg).Bold(true)
	styleModeYellow = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ModeAuto)).Background(colorWindowBg).Bold(true)
	styleModeGreen = lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Background(colorWindowBg).Bold(true)

	styleSep = lipgloss.NewStyle().Foreground(colorDim).Background(colorWindowBg)

	stylePickerItem = lipgloss.NewStyle().Foreground(colorFg).Background(colorWindowBg)
	stylePickerItemSelected = lipgloss.NewStyle().
		Foreground(colorSelectionFg).
		Background(colorWindowBg).
		Bold(true)
	stylePickerDesc = lipgloss.NewStyle().Foreground(colorMuted).Background(colorWindowBg)
	// Bold-only highlight for matched query characters (no underline — too noisy in autocomplete).
	stylePickerHighlight = lipgloss.NewStyle().
		Foreground(colorWindowTitle).
		Background(colorWindowBg).
		Bold(true)

	styleHeading1 = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFCB6B")).Background(colorWindowBg).Underline(true)
	styleHeading2 = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C792EA")).Background(colorWindowBg)
	styleHeading3 = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89DDFF")).Background(colorWindowBg)
	styleItalic = lipgloss.NewStyle().Background(colorWindowBg).Italic(true)
	styleStrike = lipgloss.NewStyle().Strikethrough(true).Foreground(lipgloss.Color("#546E7A")).Background(colorWindowBg)
	styleBQ = lipgloss.NewStyle().Foreground(lipgloss.Color("#546E7A")).Background(colorWindowBg).Italic(true)
	styleChecked = lipgloss.NewStyle().Foreground(lipgloss.Color("#89DDFF")).Background(colorWindowBg)
	styleUnchecked = lipgloss.NewStyle().Foreground(lipgloss.Color("#546E7A")).Background(colorWindowBg)

	cDiffAdd = lipgloss.NewStyle().Foreground(lipgloss.Color("#C3E88D")).Background(colorWindowBg)
	cDiffDel = lipgloss.NewStyle().Foreground(lipgloss.Color("#F07178")).Background(colorWindowBg)
	cDiffHunk = lipgloss.NewStyle().Foreground(lipgloss.Color("#89DDFF")).Background(colorWindowBg)
	cDiffHeader = lipgloss.NewStyle().Foreground(lipgloss.Color("#7986CB")).Background(colorWindowBg)
}

func init() {
	RebuildStyles()
	theme.OnChange(RebuildStyles)
}

// fgOnBg returns a foreground-colored style. When the active theme has a
// Background value (light themes), it also sets bg so embedded escapes
// don't punch holes through the painted surface. For dark themes, bg is
// left unset and the terminal default shows through.
func fgOnBg(c color.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c).Background(colorWindowBg)
}

// Widget theme refresh is handled in Model.View() (re-applies styles every
// render) rather than via theme.OnChange — Bubble Tea returns new Model
// values from Update so a captured pointer would go stale.
