package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func panelFrameStyle(width, height int) lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorWindowBorder).
		Background(colorWindowBg).
		BorderBackground(colorWindowBg).
		Width(width).
		Height(height).
		PaddingLeft(2).
		PaddingRight(2).
		PaddingTop(1).
		PaddingBottom(1)
}

func panelTitle(s string) string {
	return lipgloss.NewStyle().
		Foreground(colorWindowTitle).
		Background(colorWindowBg).
		Bold(true).
		Render(s)
}

// panelHeader renders the standard modal header: bold title left, gradient
// slash fill right. innerW is the usable content width (panel width - padding).
func panelHeader(title string, innerW int) string {
	rendered := panelTitle(title)
	ornW := innerW - lipgloss.Width(rendered) - 4
	if ornW < 1 {
		ornW = 1
	}
	return rendered + surfaceSpaces(2) + ornamentGradientText(renderSlashFill(ornW)) + surfaceSpaces(2)
}

func panelRule(width int) string {
	if width <= 0 {
		return ""
	}
	return ornamentGradientText(strings.Repeat("─", width))
}

func surfaceSpaces(width int) string {
	if width <= 0 {
		return ""
	}
	return lipgloss.NewStyle().
		Background(colorWindowBg).
		Render(strings.Repeat(" ", width))
}
