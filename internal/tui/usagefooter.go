package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/planusage"
	"github.com/lucasb-eyer/go-colorful"
)

func (m Model) renderUsageFooter(width int) string {
	if !m.usageStatusEnabled {
		return ""
	}
	width = max(width, 20)
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

	var dataPoints []string
	add := func(s string) {
		if s != "" {
			dataPoints = append(dataPoints, padStatusLine(s, width))
		}
	}
	add(contextLine)
	add(providerLine)
	add(current)
	add(weekly)

	return strings.Join(dataPoints, "\n")
}

func (m Model) renderProviderUsageWindow() string {
	name := m.activeModelDisplayName()
	if name == "" {
		return ""
	}
	return surfaceSpaces(1) +
		styleStatus.Width(8).Render("Provider") +
		surfaceSpaces(2) +
		styleStatus.Render(name)
}

func (m Model) renderContextUsageWindow() string {
	maxContextTokens := m.effectiveContextWindow()
	pct := 0
	if m.contextInputTokens > 0 {
		pct = clampInt(m.contextInputTokens*100/maxContextTokens, 0, 100)
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
	if m.contextInputTokens > 0 {
		tok := m.contextInputTokens
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

func parseColorOrFallback(hex string, fallback colorful.Color) colorful.Color {
	c, err := colorful.Hex(hex)
	if err != nil {
		return fallback
	}
	return c
}

func usageBar(pct, width int) string {
	fallbackColor := colorful.Color{R: 0.247, G: 0.725, B: 0.314} // roughly #3fb950

	width = max(width, 1)
	pct = clampInt(pct, 0, 100)

	filled := width * pct / 100
	empty := width - filled

	var b strings.Builder

	// Animate the wave over time.
	phase := float64(time.Now().UnixMilli()) / 350.0

	for i := 0; i < filled; i++ {
		t := float64(i) / float64(max(width-1, 1))

		// Wave value from 0..1
		wave := (math.Sin(t*math.Pi*2+phase) + 1) / 2

		// Pick colors based on pct, then wave between two nearby shades.
		start := parseColorOrFallback("#3fb950", fallbackColor)
		end := parseColorOrFallback("#58d68d", start)

		if pct >= 85 {
			start = parseColorOrFallback("#f85149", fallbackColor)
			end = parseColorOrFallback("#ff7b72", start)
		} else if pct >= 65 {
			start = parseColorOrFallback("#d29922", fallbackColor)
			end = parseColorOrFallback("#f2cc60", start)
		}

		c := start.BlendLab(end, wave)

		style := lipgloss.NewStyle().
			Foreground(lipgloss.Color(c.Hex())).
			Background(colorWindowBg)

		b.WriteString(style.Render("▰"))
	}

	b.WriteString(styleStatus.Render(strings.Repeat("▱", empty)))

	return b.String()
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
