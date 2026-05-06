package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/guptarohit/asciigraph"

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
		if pad < 2 {
			pad = 2
		}
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
		color := modelColors[idx%len(modelColors)]
		dot := fgOnBg(color).Render("●")
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
			if pad1 < 1 {
				pad1 = 1
			}
			if pad2 < 1 {
				pad2 = 1
			}
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
			pct := snap.inputTokens * 100 / 200000
			if pct > 100 {
				pct = 100
			}
			barW := innerW - 28
			if barW < 8 {
				barW = 8
			}
			filled := barW * pct / 100
			bar := styleStatusAccent.Render(strings.Repeat("█", filled)) +
				dim.Render(strings.Repeat("░", barW-filled))
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
	if barW < 8 {
		barW = 8
	}
	if pctUsed > 100 {
		pctUsed = 100
	}
	filled := barW * pctUsed / 100
	style := styleStatusAccent
	if pctUsed >= 80 {
		style = styleModeYellow
	}
	bar := style.Render(strings.Repeat("█", filled)) +
		stylePickerDesc.Render(strings.Repeat("░", barW-filled))
	return fmt.Sprintf("  %-14s %s %d%%  (%d / %d)\n", label+":", bar, 100-pctUsed, remaining, limit)
}

// buildHeatmap writes a 7-row × N-week GitHub-style activity heatmap.
func buildHeatmap(sb *strings.Builder, dailyCounts map[string]int, innerW int) {
	const leftPad = 5 // "Mon  " = 5

	weeks := (innerW - leftPad) / 2
	if weeks < 8 {
		weeks = 8
	}
	if weeks > 26 {
		weeks = 26
	}

	now := time.Now()

	// Start on Sunday so columns are weeks and rows are weekdays.
	todaySunday := int(now.Weekday()) // Sun=0, Mon=1..Sat=6
	startDay := now.AddDate(0, 0, -(weeks*7 - 1 + todaySunday))

	maxCount := 0
	for _, c := range dailyCounts {
		if c > maxCount {
			maxCount = c
		}
	}

	heatColors := []color.Color{
		lipgloss.Color("#123524"),
		lipgloss.Color("#1f6f43"),
		lipgloss.Color("#2ea043"),
		lipgloss.Color("#56d364"),
	}

	emptyChar := "·"
	heatChars := []string{"∘", "●", "◉", "⬤"}

	emptyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#2a2f36")).
		Background(colorWindowBg)

	heatStyles := make([]lipgloss.Style, len(heatColors))
	for i, c := range heatColors {
		heatStyles[i] = fgOnBg(c)
	}

	levelFor := func(count int) int {
		if count <= 0 || maxCount <= 0 {
			return -1
		}

		// Map 1..maxCount onto 0..len(heatChars)-1.
		level := (count - 1) * len(heatChars) / maxCount
		if level < 0 {
			level = 0
		}
		if level >= len(heatChars) {
			level = len(heatChars) - 1
		}
		return level
	}

	cell := func(count int) string {
		level := levelFor(count)
		if level < 0 {
			return emptyStyle.Render(emptyChar)
		}
		return heatStyles[level].Render(heatChars[level])
	}

	grid := make([][]int, 7)
	for i := range grid {
		grid[i] = make([]int, weeks)
	}

	weekStarts := make([]time.Time, weeks)
	for w := 0; w < weeks; w++ {
		ws := startDay.AddDate(0, 0, w*7)
		weekStarts[w] = ws

		for d := 0; d < 7; d++ {
			day := ws.AddDate(0, 0, d).Format("2006-01-02")
			grid[d][w] = dailyCounts[day]
		}
	}

	renderHeatmapMonths(sb, weekStarts, leftPad)
	renderHeatmapRows(sb, grid, weeks, cell)
	renderHeatmapLegend(sb, leftPad, emptyStyle, emptyChar, heatStyles, heatChars)
}

func renderHeatmapMonths(sb *strings.Builder, weekStarts []time.Time, leftPad int) {
	weeks := len(weekStarts)

	monthRow := make([]byte, weeks*2+4)
	for i := range monthRow {
		monthRow[i] = ' '
	}

	prevMonth := -1
	lastLabelEnd := -1

	for w, ws := range weekStarts {
		m := int(ws.Month())
		pos := w * 2

		if m == prevMonth {
			continue
		}

		// Need enough space to draw "Jan".
		if pos < lastLabelEnd+1 {
			prevMonth = m
			continue
		}

		label := ws.Format("Jan")
		for i, c := range []byte(label) {
			if pos+i < len(monthRow) {
				monthRow[pos+i] = c
			}
		}

		prevMonth = m
		lastLabelEnd = pos + len(label)
	}

	monthStr := strings.TrimRight(string(monthRow), " ")

	sb.WriteString(surfaceSpaces(leftPad))
	sb.WriteString(stylePickerDesc.Render(monthStr))
	sb.WriteByte('\n')
}

func renderHeatmapRows(
	sb *strings.Builder,
	grid [][]int,
	weeks int,
	cell func(count int) string,
) {
	rowLabels := [7]string{"   ", "Mon", "   ", "Wed", "   ", "Fri", "   "}

	for d := 0; d < 7; d++ {
		sb.WriteString(stylePickerDesc.Render(rowLabels[d]))
		sb.WriteString(surfaceSpaces(2))

		for w := 0; w < weeks; w++ {
			sb.WriteString(cell(grid[d][w]))

			if w < weeks-1 {
				sb.WriteString(surfaceSpaces(1))
			}
		}

		sb.WriteByte('\n')
	}
}

func renderHeatmapLegend(
	sb *strings.Builder,
	leftPad int,
	emptyStyle lipgloss.Style,
	emptyChar string,
	heatStyles []lipgloss.Style,
	heatChars []string,
) {
	sb.WriteString(surfaceSpaces(leftPad))
	sb.WriteString(stylePickerDesc.Render("Less  "))
	sb.WriteString(emptyStyle.Render(emptyChar))

	for i := range heatChars {
		sb.WriteString(surfaceSpaces(1))
		sb.WriteString(heatStyles[i].Render(heatChars[i]))
	}

	sb.WriteString(stylePickerDesc.Render("  More"))
	sb.WriteByte('\n')
}

// buildTokensLineChart renders a per-model step-line chart using asciigraph,
// matching Claude Code's "Tokens per Day" chart exactly.
// Top 3 models are drawn as separate colored lines; x-axis labels show dates.
func buildTokensLineChart(sb *strings.Builder, dailyModelTokens []dailyModelEntry, rows []modelRow, modelColors []color.Color, innerW int) {
	if len(dailyModelTokens) < 2 {
		sb.WriteString(stylePickerDesc.Render("  Not enough data for chart.\n"))
		return
	}

	// CC caps chart width at 52, aligned with heatmap. Y-axis label width is 7.
	const yAxisWidth = 7
	availW := innerW - yAxisWidth - 2 // -2 for indent
	chartW := availW
	if chartW > 52 {
		chartW = 52
	}
	if chartW < 10 {
		chartW = 10
	}

	// Distribute data across chartW: if fewer points than width, repeat each;
	// if more, take the most recent chartW entries. Mirrors CC's generateTokenChart.
	var recentData []dailyModelEntry
	if len(dailyModelTokens) >= chartW {
		recentData = dailyModelTokens[len(dailyModelTokens)-chartW:]
	} else {
		repeatCount := chartW / len(dailyModelTokens)
		for _, day := range dailyModelTokens {
			for i := 0; i < repeatCount; i++ {
				recentData = append(recentData, day)
			}
		}
	}

	// Top 3 models only (already sorted by total tokens descending).
	topModels := rows
	if len(topModels) > 3 {
		topModels = topModels[:3]
	}

	// asciigraph color constants matching CC theme (suggestion=green, success=yellow, warning=red).
	agColors := []asciigraph.AnsiColor{asciigraph.Green, asciigraph.Yellow, asciigraph.Red}
	// Lipgloss colors for legend bullets (match the asciigraph ANSI colors visually).
	legendColors := []color.Color{lipgloss.Color("#22C55E"), lipgloss.Color("#EAB308"), lipgloss.Color("#EF4444")}

	var series [][]float64
	var legendParts []string
	for i, r := range topModels {
		data := make([]float64, len(recentData))
		hasData := false
		for j, day := range recentData {
			v := day.TokensByModel[r.name]
			data[j] = float64(v)
			if v > 0 {
				hasData = true
			}
		}
		if !hasData {
			continue
		}
		series = append(series, data)
		color := legendColors[i%len(legendColors)]
		_ = modelColors // modelColors used in the breakdown below
		dot := fgOnBg(color).Render("●")
		legendParts = append(legendParts, dot+" "+stylePickerDesc.Render(shortModelName(r.name)))
	}

	if len(series) == 0 {
		sb.WriteString(stylePickerDesc.Render("  No token data.\n"))
		return
	}

	// Render chart with asciigraph.
	nSeries := len(series)
	if nSeries > len(agColors) {
		nSeries = len(agColors)
		series = series[:nSeries]
	}
	opts := []asciigraph.Option{
		asciigraph.Height(8),
		asciigraph.SeriesColors(agColors[:nSeries]...),
	}
	var chart string
	if len(series) == 1 {
		chart = asciigraph.Plot(series[0], opts...)
	} else {
		chart = asciigraph.PlotMany(series, opts...)
	}

	// Indent each chart line by 2 spaces.
	indent := "  "
	for _, line := range strings.Split(chart, "\n") {
		sb.WriteString(indent + line + "\n")
	}

	// X-axis date labels.
	xLabels := generateChartXLabels(recentData, yAxisWidth)
	sb.WriteString(indent + xLabels + "\n")

	// Legend: "● Sonnet 4.6 · ● Opus 4.7"
	if len(legendParts) > 0 {
		sb.WriteString(indent + strings.Join(legendParts, stylePickerDesc.Render(" · ")) + "\n")
	}
}

// generateChartXLabels produces evenly-spaced date labels for the chart x-axis.
func generateChartXLabels(data []dailyModelEntry, yAxisOffset int) string {
	if len(data) == 0 {
		return ""
	}
	numLabels := 4
	if len(data) < 16 {
		numLabels = 2
	}
	usableLength := len(data) - 6 // reserve space for last label
	if usableLength < 1 {
		usableLength = 1
	}
	step := usableLength / (numLabels - 1)
	if step < 1 {
		step = 1
	}

	result := strings.Repeat(" ", yAxisOffset)
	currentPos := 0
	for i := 0; i < numLabels; i++ {
		idx := i * step
		if idx >= len(data) {
			idx = len(data) - 1
		}
		t, err := time.Parse("2006-01-02", data[idx].Date)
		if err != nil {
			continue
		}
		label := t.Format("Jan 2")
		spaces := idx - currentPos
		if spaces < 1 {
			spaces = 1
		}
		result += strings.Repeat(" ", spaces) + label
		currentPos = idx + len(label)
	}
	return result
}

// literaryTokenCounts maps famous books to approximate word/token counts.
// Claude Code uses these for the "~Nx more tokens than <book>" factoid.
var literaryTokenCounts = []struct {
	title  string
	tokens int
}{
	{"War and Peace", 580_000},
	{"Les Misérables", 530_000},
	{"Don Quixote", 430_000},
	{"Ulysses", 265_000},
	{"Moby Dick", 210_000},
	{"Anna Karenina", 350_000},
	{"The Brothers Karamazov", 360_000},
	{"Crime and Punishment", 211_000},
	{"Great Expectations", 185_000},
	{"Jane Eyre", 183_000},
	{"Hamlet", 30_000},
	{"Slaughterhouse-Five", 49_000},
	{"The Great Gatsby", 47_000},
	{"Of Mice and Men", 30_000},
	{"The Catcher in the Rye", 73_000},
	{"To Kill a Mockingbird", 100_000},
	{"1984", 88_000},
	{"Brave New World", 64_000},
	{"Fahrenheit 451", 46_000},
	{"Lord of the Flies", 59_000},
}

func buildFactoid(stats *sessionStats) string {
	totalTok := stats.TotalInputTok + stats.TotalOutputTok

	// Literary comparison: find the best-fit book.
	if totalTok > 5_000 {
		bestTitle := ""
		bestMult := 0
		for _, b := range literaryTokenCounts {
			if b.tokens <= 0 {
				continue
			}
			mult := totalTok / b.tokens
			if mult >= 1 && mult > bestMult {
				bestMult = mult
				bestTitle = b.title
			}
		}
		if bestTitle != "" && bestMult >= 2 {
			return fmt.Sprintf("You've used ~%dx more tokens than %s", bestMult, bestTitle)
		}
	}

	switch {
	case stats.CurrentStreak >= 7:
		return fmt.Sprintf("You're on a %d-day streak! Keep it up!", stats.CurrentStreak)
	case stats.TotalSessions >= 100:
		return fmt.Sprintf("Over %d sessions — you're a power user!", stats.TotalSessions)
	case stats.LongestStreak >= 5:
		return fmt.Sprintf("Your longest streak was %d days.", stats.LongestStreak)
	default:
		return ""
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func activeDaysLabel(stats *sessionStats) string {
	active := len(stats.DailyCounts)
	if stats.TotalDaysRange > 0 {
		return fmt.Sprintf("%d/%d", active, stats.TotalDaysRange)
	}
	return fmt.Sprintf("%d", active)
}

func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

func formatNum(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatDur(d time.Duration) string {
	if d < time.Minute {
		return "< 1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func formatDurSec(sec float64) string {
	return formatDur(time.Duration(sec * float64(time.Second)))
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

var _ = formatDuration
