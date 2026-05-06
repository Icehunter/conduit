package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/guptarohit/asciigraph"
)

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
		c := legendColors[i%len(legendColors)]
		_ = modelColors // modelColors used in the breakdown below
		dot := fgOnBg(c).Render("●")
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
var _ = truncateStr
