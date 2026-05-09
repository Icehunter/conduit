package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/sessionstats"
)

// ── Stats tab ─────────────────────────────────────────────────────────────────

type dailyModelEntry = sessionstats.DailyModelEntry
type sessionStats = sessionstats.Stats
type modelUsageStats = sessionstats.ModelUsage

// modelRow is a model name + usage pair used for chart and breakdown rendering.
type modelRow struct {
	name string
	u    modelUsageStats
}

func (m Model) renderSettingsStats(sb *strings.Builder, p *settingsPanelState, innerW, _ int) {
	sb.WriteString(surfaceSpaces(2) + settingsColorTabs(statsSubTabNames, int(p.statsSubTab)) + "\n\n")

	if !p.statsLoaded {
		sb.WriteString(stylePickerDesc.Render("  Loading stats…"))
		return
	}

	stats := p.statsData

	// Date range selector.
	var rangeLabels []string
	for i, label := range statsDateRangeLabels {
		if statsDateRange(i) == p.statsRange {
			rangeLabels = append(rangeLabels, styleStatusAccent.Render(label))
		} else {
			rangeLabels = append(rangeLabels, stylePickerDesc.Render(label))
		}
	}
	sb.WriteString(surfaceSpaces(2) + strings.Join(rangeLabels, stylePickerDesc.Render(" · ")) +
		stylePickerDesc.Render("  (r to cycle)") + "\n\n")

	switch p.statsSubTab {
	case statsSubOverview:
		m.renderStatsOverview(sb, &stats, innerW)
	case statsSubModels:
		m.renderStatsModels(sb, &stats, innerW)
	}
}

func (m Model) renderStatsOverview(sb *strings.Builder, stats *sessionStats, innerW int) {
	if stats.TotalSessions == 0 {
		sb.WriteString(stylePickerDesc.Render("  No sessions found."))
		return
	}

	// 7-row GitHub-style heatmap.
	buildHeatmap(sb, stats.DailyCounts, innerW)
	sb.WriteByte('\n')

	dim := stylePickerDesc
	acc := styleStatusAccent.Render

	// Favorite model by output tokens.
	favModel := ""
	favTok := 0
	for model, u := range stats.ModelUsage {
		if u.OutputTokens > favTok {
			favTok = u.OutputTokens
			favModel = model
		}
	}
	totalTok := stats.TotalInputTok + stats.TotalOutputTok

	// Layout matches screenshot: label left-aligned, value accent, 2 columns per row.
	type col struct{ label, value string }
	rows := [][2]col{
		{{"Favorite model", shortModelName(favModel)}, {"Total tokens", formatNum(totalTok)}},
		{{"Sessions", fmt.Sprintf("%d", stats.TotalSessions)}, {"Longest session", formatDur(stats.LongestSession)}},
		{{"Active days", activeDaysLabel(stats)}, {"Longest streak", fmt.Sprintf("%d days", stats.LongestStreak)}},
		{{"Most active day", stats.MostActiveDay}, {"Current streak", fmt.Sprintf("%d days", stats.CurrentStreak)}},
	}
	// Fixed column widths: left col = 38 visible chars, right col fills the rest.
	// Using a fixed left-column width ensures right column values align regardless
	// of value length. lipgloss.Width() gives the visible width past ANSI escapes.
	const leftColW = 38
	for _, row := range rows {
		l := row[0]
		r := row[1]
		lPart := dim.Render(fmt.Sprintf("%-16s", l.label+":")) + surfaceSpaces(1) + acc(l.value)
		rPart := dim.Render(fmt.Sprintf("%-18s", r.label+":")) + surfaceSpaces(1) + acc(r.value)
		lVis := lipgloss.Width(lPart)
		pad := leftColW - lVis
		pad = max(pad, 2)
		sb.WriteString(surfaceSpaces(2) + lPart + surfaceSpaces(pad) + rPart + "\n")
	}

	sb.WriteByte('\n')
	if f := buildFactoid(stats); f != "" {
		sb.WriteString(fgOnBg(colorTool).Render("  "+f) + "\n")
	}
}

func (m Model) renderStatsModels(sb *strings.Builder, stats *sessionStats, innerW int) {
	if len(stats.ModelUsage) == 0 {
		sb.WriteString(stylePickerDesc.Render("  No model usage data found."))
		return
	}

	var rows []modelRow
	total := 0
	for k, v := range stats.ModelUsage {
		if k == "<synthetic>" {
			continue
		}
		rows = append(rows, modelRow{k, v})
		total += v.InputTokens + v.OutputTokens
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].u.InputTokens+rows[i].u.OutputTokens > rows[j].u.InputTokens+rows[j].u.OutputTokens
	})

	// Model colors — shared by chart legend and 2-column breakdown below.
	modelColors := []color.Color{
		lipgloss.Color("#74C69D"), // green
		lipgloss.Color("#ADB5BD"), // gray
		lipgloss.Color("#FFD166"), // yellow
		lipgloss.Color("#EF476F"), // red
		lipgloss.Color("#118AB2"), // blue
		lipgloss.Color("#9B5DE5"), // purple
	}

	// Tokens per Day chart using asciigraph (top 3 models as separate colored series).
	sb.WriteString(fgOnBg(colorFg).Bold(true).Render("  Tokens per Day") + "\n")
	buildTokensLineChart(sb, stats.DailyModelTokens, rows, modelColors, innerW)
	sb.WriteByte('\n')

	colW := (innerW - 2) / 2
	renderModelEntry := func(idx int, r modelRow) (line1, line2 string) {
		tot := r.u.InputTokens + r.u.OutputTokens
		pct := 0
		if total > 0 {
			pct = tot * 100 / total
		}
		c := modelColors[idx%len(modelColors)]
		dot := fgOnBg(c).Render("●")
		name := fgOnBg(colorFg).Bold(true).Render(shortModelName(r.name))
		line1 = dot + surfaceSpaces(1) + name + surfaceSpaces(1) + stylePickerDesc.Render(fmt.Sprintf("(%d%%)", pct))
		line2 = stylePickerDesc.Render(fmt.Sprintf("    In: %s · Out: %s",
			formatNum(r.u.InputTokens), formatNum(r.u.OutputTokens)))
		return
	}

	for i := 0; i < len(rows); i += 2 {
		l1, l2 := renderModelEntry(i, rows[i])
		if i+1 < len(rows) {
			r1, r2 := renderModelEntry(i+1, rows[i+1])
			// Pad left column to colW visible chars using spaces.
			l1vis := lipgloss.Width(l1)
			l2vis := lipgloss.Width(l2)
			pad1 := colW - l1vis
			pad2 := colW - l2vis
			pad1 = max(pad1, 1)
			pad2 = max(pad2, 1)
			sb.WriteString(surfaceSpaces(2) + l1 + surfaceSpaces(pad1) + r1 + "\n")
			sb.WriteString(surfaceSpaces(2) + l2 + surfaceSpaces(pad2) + r2 + "\n")
		} else {
			sb.WriteString(surfaceSpaces(2) + l1 + "\n")
			sb.WriteString(surfaceSpaces(2) + l2 + "\n")
		}
		sb.WriteByte('\n')
	}
	if len(rows) > 4 {
		sb.WriteString(stylePickerDesc.Render(fmt.Sprintf("  ↓ 1–4 of %d models (↑/↓ to scroll)", len(rows))) + "\n")
	}
}

// ── Usage tab ─────────────────────────────────────────────────────────────────

func (m Model) renderSettingsUsage(sb *strings.Builder, p *settingsPanelState, innerW, _ int) {
	snap := statusSnapshot{}
	if p.getStatus != nil {
		snap = p.getStatus()
	}

	bold := fgOnBg(colorFg).Bold(true)
	dim := stylePickerDesc

	sb.WriteString(bold.Render("Session") + "\n\n")

	row := func(label, value string) {
		sb.WriteString(fgOnBg(colorFg).Render(fmt.Sprintf("  %-22s %s", label, value)) + "\n")
	}

	if snap.costUSD <= 0 && snap.inputTokens <= 0 {
		sb.WriteString(dim.Render("  No API calls made this session.") + "\n")
	} else {
		if snap.costUSD > 0 {
			row("Total cost:", fmt.Sprintf("$%.4f", snap.costUSD))
		}
		if snap.apiDurSec > 0 {
			row("API duration:", formatDurSec(snap.apiDurSec))
		}
		if snap.wallDurSec > 0 {
			row("Wall duration:", formatDurSec(snap.wallDurSec))
		}
		if snap.linesAdded > 0 || snap.linesRemoved > 0 {
			row("Code changes:", fmt.Sprintf("+%d / -%d lines", snap.linesAdded, snap.linesRemoved))
		}
		sb.WriteByte('\n')
		if snap.inputTokens > 0 {
			row("Tokens in:", formatNum(snap.inputTokens))
		}
		if snap.outputTokens > 0 {
			row("Tokens out:", formatNum(snap.outputTokens))
		}
		if snap.cacheReadTok > 0 {
			row("Cache read:", formatNum(snap.cacheReadTok))
		}
		if snap.cacheWriteTok > 0 {
			row("Cache write:", formatNum(snap.cacheWriteTok))
		}
		if snap.inputTokens > 0 {
			pct := min(snap.inputTokens*100/200000, 100)
			barW := max(innerW-28, 8)
			filled := barW * pct / 100
			bar := styleStatusAccent.Render(strings.Repeat("█", filled)) + dim.Render(strings.Repeat("░", barW-filled))
			fmt.Fprintf(sb, "\n  %-22s %s %d%%\n", "Context:", bar, pct)
		}
	}

	rl := p.rateLimitInfo
	if rl.HasData() {
		sb.WriteString("\n" + bold.Render("Rate Limits") + "\n\n")
		if rl.RequestsLimit > 0 {
			pct := 100 - (rl.RequestsRemaining * 100 / rl.RequestsLimit)
			sb.WriteString(renderLimitBar("Requests", pct, rl.RequestsRemaining, rl.RequestsLimit, innerW))
		}
		if rl.TokensLimit > 0 {
			pct := 100 - (rl.TokensRemaining * 100 / rl.TokensLimit)
			sb.WriteString(renderLimitBar("Tokens", pct, rl.TokensRemaining, rl.TokensLimit, innerW))
		}
		if snap.rateLimitWarn != "" {
			sb.WriteString("\n  " + styleModeYellow.Render("⚠ "+snap.rateLimitWarn) + "\n")
		}
	}
}

func renderLimitBar(label string, pctUsed, remaining, limit, innerW int) string {
	barW := innerW - 24
	barW = max(barW, 8)
	pctUsed = min(pctUsed, 100)
	filled := barW * pctUsed / 100
	style := styleStatusAccent
	if pctUsed >= 80 {
		style = styleModeYellow
	}
	bar := style.Render(strings.Repeat("█", filled)) +
		stylePickerDesc.Render(strings.Repeat("░", barW-filled))
	return fmt.Sprintf("  %-14s %s %d%%  (%d / %d)\n", label+":", bar, 100-pctUsed, remaining, limit)
}
