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

func chromeTab(label string, active bool) string {
	if active {
		return lipgloss.NewStyle().
			Foreground(colorWindowTitle).
			Background(colorWindowBg).
			Bold(true).
			Render(label)
	}
	return lipgloss.NewStyle().
		Foreground(colorMuted).
		Background(colorWindowBg).
		Render(label)
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
