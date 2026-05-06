package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/planusage"
)

func (m Model) renderUsageFooter(width int) string {
	if !m.usageStatusEnabled {
		return ""
	}
	if width < 20 {
		width = 20
	}
	providerLine := m.renderProviderUsageWindow()
	if _, ok := m.activeMCPProvider(); ok {
		return padStatusLine(m.renderContextUsageWindow(), width) + "\n" +
			padStatusLine(providerLine, width) + "\n" +
			padStatusLine("", width) + "\n" +
			padStatusLine("", width)
	}
	if _, ok := m.planUsageProviderSettings(); !ok {
		return padStatusLine(m.renderContextUsageWindow(), width) + "\n" +
			padStatusLine(providerLine, width) + "\n" +
			padStatusLine("", width) + "\n" +
			padStatusLine("", width)
	}
	rateLimited := !m.planUsageBackoff.IsZero() && time.Now().Before(m.planUsageBackoff)
	hasCachedData := !m.planUsage.FiveHour.ResetsAt.IsZero() || !m.planUsage.SevenDay.ResetsAt.IsZero()
	if m.planUsageErr != "" && !hasCachedData {
		var line string
		if rateLimited {
			line = styleModeYellow.Render(" Usage: rate limited") +
				styleStatus.Render(" · retry at "+m.planUsageBackoff.Local().Format("3:04pm"))
		} else {
			line = styleModeYellow.Render(" Usage: unavailable") + styleStatus.Render(" | "+m.planUsageErr)
		}
		return padStatusLine(line, width) + "\n" + padStatusLine(providerLine, width) + "\n" + padStatusLine("", width) + "\n" + padStatusLine(m.renderContextUsageWindow(), width)
	}
	if rateLimited && !hasCachedData {
		line := styleModeYellow.Render(" Usage: rate limited") +
			styleStatus.Render(" · retry at "+m.planUsageBackoff.Local().Format("3:04pm"))
		return padStatusLine(line, width) + "\n" + padStatusLine(providerLine, width) + "\n" + padStatusLine("", width) + "\n" + padStatusLine(m.renderContextUsageWindow(), width)
	}
	if !hasCachedData {
		line := styleStatus.Render(" Usage: loading...")
		return padStatusLine(line, width) + "\n" + padStatusLine(providerLine, width) + "\n" + padStatusLine("", width) + "\n" + padStatusLine(m.renderContextUsageWindow(), width)
	}
	current := renderUsageWindow("Current", m.planUsage.FiveHour)
	weekly := renderUsageWindow("Weekly ", m.planUsage.SevenDay)
	contextLine := m.renderContextUsageWindow()

	dataPoints := []string{
		padStatusLine(contextLine, width),
		padStatusLine(providerLine, width),
		padStatusLine(current, width),
		padStatusLine(weekly, width),
	}

	return strings.Join(dataPoints, "\n")
}

func (m Model) renderProviderUsageWindow() string {
	return surfaceSpaces(1) +
		styleStatus.Width(8).Render("Provider") +
		surfaceSpaces(2) +
		styleStatus.Render(m.activeModelDisplayName())
}

func (m Model) renderContextUsageWindow() string {
	const maxContextTokens = 200000
	pct := 0
	if m.totalInputTokens > 0 {
		pct = clampInt(m.totalInputTokens*100/maxContextTokens, 0, 100)
	}

	labelText := styleStatus.Width(8).Render("Context")
	bar := usageBar(pct, 18)

	pctText := fmt.Sprintf("%3d%%", pct)
	if pct >= 85 {
		pctText = styleErrorText.Render(pctText)
	} else if pct >= 65 {
		pctText = styleModeYellow.Render(pctText)
	} else {
		pctText = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3fb950")).
			Background(colorWindowBg).
			Render(pctText)
	}

	var tokText string
	if m.totalInputTokens > 0 {
		tok := m.totalInputTokens
		switch {
		case tok >= 1_000_000:
			tokText = fmt.Sprintf(" · %.1fM tok", float64(tok)/1_000_000)
		case tok >= 1_000:
			tokText = fmt.Sprintf(" · %.1fk tok", float64(tok)/1_000)
		default:
			tokText = fmt.Sprintf(" · %d tok", tok)
		}
		tokText = styleStatus.Render(tokText)
	}

	costText := ""
	if m.costUSD > 0 {
		costText = styleStatus.Render(fmt.Sprintf(" · $%.2f", m.costUSD))
	}

	return surfaceSpaces(1) + labelText + surfaceSpaces(2) + bar + surfaceSpaces(2) + pctText + tokText + costText
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func renderUsageWindow(label string, w planusage.Window) string {
	pct := clampInt(int(w.Utilization+0.5), 0, 100)

	reset := formatUsageReset(w.ResetsAt)

	labelText := styleStatus.Width(8).Render(label)
	bar := usageBar(pct, 18)

	pctText := fmt.Sprintf("%3d%%", pct)
	if pct >= 85 {
		pctText = styleErrorText.Render(pctText)
	} else if pct >= 65 {
		pctText = styleModeYellow.Render(pctText)
	} else {
		pctText = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3fb950")).
			Background(colorWindowBg).
			Render(pctText)
	}

	return surfaceSpaces(1) + labelText + surfaceSpaces(2) + bar + surfaceSpaces(2) + pctText + surfaceSpaces(2) + styleStatus.Render(reset)
}

func usageBar(pct, width int) string {
	if width < 1 {
		width = 1
	}

	pct = clampInt(pct, 0, 100)
	filled := width * pct / 100
	empty := width - filled

	style := lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Background(colorWindowBg)

	switch {
	case pct >= 85:
		style = styleErrorText
	case pct >= 65:
		style = styleModeYellow
	}

	return style.Render(strings.Repeat("▰", filled)) +
		styleStatus.Render(strings.Repeat("▱", empty))
}

func formatUsageReset(t time.Time) string {
	if t.IsZero() {
		return "↻ unknown"
	}

	local := t.Local()
	now := time.Now()

	localDay := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	days := int(localDay.Sub(nowDay).Hours() / 24)

	switch days {
	case 0:
		return "↻ today " + local.Format("3:04pm")
	case 1:
		return "↻ tomorrow " + local.Format("3:04pm")
	default:
		return "↻ " + local.Format("Jan 2 3:04pm")
	}
}

func padStatusLine(line string, width int) string {
	if lipgloss.Width(line) > width {
		return lipgloss.NewStyle().Background(colorWindowBg).Width(width).MaxWidth(width).Render(line)
	}
	return line + surfaceSpaces(width-lipgloss.Width(line))
}
