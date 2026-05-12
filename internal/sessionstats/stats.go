package sessionstats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// DailyModelEntry mirrors DailyModelTokens from the cache — one entry per active day.
type DailyModelEntry struct {
	Date          string
	TokensByModel map[string]int
}

type Stats struct {
	TotalSessions    int
	TotalMessages    int
	TotalInputTok    int
	TotalOutputTok   int
	TotalCostUSD     float64
	ModelUsage       map[string]ModelUsage
	DailyCounts      map[string]int    // day → message count
	DailyTokens      map[string]int    // day → total tokens (all models)
	DailyModelTokens []DailyModelEntry // ordered by date — for per-model chart
	LongestStreak    int
	CurrentStreak    int
	MostActiveDay    string
	LongestSession   time.Duration
	RangeStart       time.Time // earliest date in the loaded range
	TotalDaysRange   int       // calendar days from rangeStart to today

	// Token savings metrics (added by conduit)
	TokenSavings TokenSavingsStats
}

// TokenSavingsStats tracks token savings from various optimization features.
type TokenSavingsStats struct {
	// RTK (command output filtering)
	RTKBytesSaved int
	RTKCallCount  int

	// Microcompact (clearing old tool_results)
	MicrocompactTokensSaved int
	MicrocompactCallCount   int

	// Truncate-to-disk (large outputs saved to disk)
	TruncateBytesSaved int
	TruncateCallCount  int

	// Full compaction
	CompactCallCount int
}

type ModelUsage struct {
	InputTokens  int
	OutputTokens int
	Sessions     int
}

// ──────────────────────────────────────────────────────────────────────────────
// Stats loading — reads Conduit's stats cache first, falls back to Claude's
// legacy stats cache, then scans JSONL files when both caches are absent.
// ──────────────────────────────────────────────────────────────────────────────

// statsCacheFile is the stats-cache.json shape inherited from Claude Code.
type statsCacheFile struct {
	Version          int    `json:"version"`
	LastComputedDate string `json:"lastComputedDate"`
	TotalSessions    int    `json:"totalSessions"`
	TotalMessages    int    `json:"totalMessages"`
	FirstSessionDate string `json:"firstSessionDate"`
	LongestSession   struct {
		Duration int64 `json:"duration"` // milliseconds
	} `json:"longestSession"`
	DailyActivity []struct {
		Date         string `json:"date"`
		MessageCount int    `json:"messageCount"`
		SessionCount int    `json:"sessionCount"`
	} `json:"dailyActivity"`
	DailyModelTokens []struct {
		Date          string         `json:"date"`
		TokensByModel map[string]int `json:"tokensByModel"`
	} `json:"dailyModelTokens"`
	ModelUsage map[string]struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"modelUsage"`
	HourCounts map[string]int `json:"hourCounts"`
}

func LoadAll(days int) Stats {
	// Try the Conduit-owned stats cache first, then fall back to Claude's
	// legacy cache for users with existing history.
	cachePaths := []string{
		filepath.Join(settings.ConduitDir(), "stats-cache.json"),
		filepath.Join(settings.ClaudeDir(), "stats-cache.json"),
	}
	for _, cachePath := range cachePaths {
		if data, err := os.ReadFile(cachePath); err == nil {
			var cache statsCacheFile
			if json.Unmarshal(data, &cache) == nil && cache.TotalSessions > 0 {
				return statsFromCache(&cache, days)
			}
		}
	}

	// Fallback: scan JSONL files.
	return scanAllJSONL(days)
}

// statsFromCache converts a statsCacheFile into Stats, optionally filtering
// to the most recent `days` days (0 = all time).
func statsFromCache(cache *statsCacheFile, days int) Stats {
	cutoff := time.Time{}
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}

	out := Stats{
		ModelUsage:  map[string]ModelUsage{},
		DailyCounts: map[string]int{},
		DailyTokens: map[string]int{},
	}

	// Pre-sort DailyModelTokens by date for ordered chart series.
	sortedDMT := make([]struct {
		Date          string
		TokensByModel map[string]int
	}, len(cache.DailyModelTokens))
	for i, e := range cache.DailyModelTokens {
		sortedDMT[i].Date = e.Date
		sortedDMT[i].TokensByModel = e.TokensByModel
	}
	sort.Slice(sortedDMT, func(i, j int) bool {
		return sortedDMT[i].Date < sortedDMT[j].Date
	})

	if cache.LongestSession.Duration > 0 {
		out.LongestSession = time.Duration(cache.LongestSession.Duration) * time.Millisecond
	}

	if cutoff.IsZero() {
		out.TotalSessions = cache.TotalSessions
		out.TotalMessages = cache.TotalMessages
	}

	// DailyActivity → dailyCounts + filtered totals.
	for _, da := range cache.DailyActivity {
		t, err := time.Parse("2006-01-02", da.Date)
		if err != nil {
			continue
		}
		if !cutoff.IsZero() && t.Before(cutoff) {
			continue
		}
		out.DailyCounts[da.Date] = da.MessageCount
		if !cutoff.IsZero() {
			out.TotalSessions += da.SessionCount
			out.TotalMessages += da.MessageCount
		}
	}

	// DailyModelTokens → dailyTokens + per-model filtered combined totals + chart series.
	filteredModelCombined := map[string]int{} // model → combined tok in filtered range
	for _, dmt := range sortedDMT {
		t, err := time.Parse("2006-01-02", dmt.Date)
		if err != nil {
			continue
		}
		if !cutoff.IsZero() && t.Before(cutoff) {
			continue
		}
		dayTotal := 0
		for model, tok := range dmt.TokensByModel {
			dayTotal += tok
			if !cutoff.IsZero() {
				filteredModelCombined[model] += tok
			}
		}
		out.DailyTokens[dmt.Date] = dayTotal
		out.DailyModelTokens = append(out.DailyModelTokens, DailyModelEntry{
			Date:          dmt.Date,
			TokensByModel: dmt.TokensByModel,
		})
	}

	// All-time: use modelUsage directly (has input+output split).
	if cutoff.IsZero() {
		for model, u := range cache.ModelUsage {
			out.ModelUsage[model] = ModelUsage{
				InputTokens:  u.InputTokens,
				OutputTokens: u.OutputTokens,
			}
			out.TotalInputTok += u.InputTokens
			out.TotalOutputTok += u.OutputTokens
		}
	} else {
		// Filtered range: dailyModelTokens has combined totals only.
		// Derive input/output split using the all-time ratio from modelUsage.
		for model, combined := range filteredModelCombined {
			var inTok, outTok int
			if u, ok := cache.ModelUsage[model]; ok {
				allTotal := u.InputTokens + u.OutputTokens
				if allTotal > 0 {
					// Apply same in/out ratio as all-time.
					inTok = combined * u.InputTokens / allTotal
					outTok = combined - inTok
				} else {
					outTok = combined
				}
			} else {
				outTok = combined
			}
			out.ModelUsage[model] = ModelUsage{
				InputTokens:  inTok,
				OutputTokens: outTok,
			}
			out.TotalInputTok += inTok
			out.TotalOutputTok += outTok
		}
	}

	// Set rangeStart: earliest date in scope.
	if cutoff.IsZero() {
		// All time: use firstSessionDate from cache.
		if cache.FirstSessionDate != "" {
			if t, err := time.Parse(time.RFC3339Nano, cache.FirstSessionDate); err == nil {
				out.RangeStart = t.UTC().Truncate(24 * time.Hour)
			}
		}
	} else {
		out.RangeStart = cutoff.UTC().Truncate(24 * time.Hour)
	}

	out.LongestStreak, out.CurrentStreak = computeStreaks(out.DailyCounts)
	out.MostActiveDay = mostActiveDay(out.DailyCounts)
	if !out.RangeStart.IsZero() {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		out.TotalDaysRange = int(today.Sub(out.RangeStart).Hours()/24) + 1
	}
	return out
}

// scanAllJSONL is the fallback when no stats cache exists.
func scanAllJSONL(days int) Stats {
	projectsBase := filepath.Join(settings.ConduitDir(), "projects")
	if !hasProjectSessions(projectsBase) {
		projectsBase = filepath.Join(settings.ClaudeDir(), "projects")
	}
	projectDirs, err := os.ReadDir(projectsBase)
	if err != nil {
		return Stats{}
	}

	cutoff := time.Time{}
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}

	out := Stats{
		ModelUsage:  map[string]ModelUsage{},
		DailyCounts: map[string]int{},
		DailyTokens: map[string]int{},
	}

	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}
		dirPath := filepath.Join(projectsBase, pd.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, e := range files {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if days > 0 {
				info, err2 := e.Info()
				if err2 != nil || info.ModTime().Before(cutoff) {
					continue
				}
			}
			scanJSONL(filepath.Join(dirPath, e.Name()), &out, cutoff)
		}
	}

	// Set rangeStart for the JSONL fallback.
	if !cutoff.IsZero() {
		out.RangeStart = cutoff.UTC().Truncate(24 * time.Hour)
	} else if len(out.DailyCounts) > 0 {
		var earliest string
		for d := range out.DailyCounts {
			if earliest == "" || d < earliest {
				earliest = d
			}
		}
		if t, err := time.Parse("2006-01-02", earliest); err == nil {
			out.RangeStart = t.UTC()
		}
	}

	out.LongestStreak, out.CurrentStreak = computeStreaks(out.DailyCounts)
	out.MostActiveDay = mostActiveDay(out.DailyCounts)
	if !out.RangeStart.IsZero() {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		out.TotalDaysRange = int(today.Sub(out.RangeStart).Hours()/24) + 1
	}
	return out
}

func hasProjectSessions(projectsBase string) bool {
	projectDirs, err := os.ReadDir(projectsBase)
	if err != nil {
		return false
	}
	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(projectsBase, pd.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".jsonl") {
				return true
			}
		}
	}
	return false
}

func scanJSONL(path string, out *Stats, cutoff time.Time) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	out.TotalSessions++
	sessionStart := time.Time{}
	sessionEnd := time.Time{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Ts        int64           `json:"ts"`
			Message   json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		ts := time.Time{}
		if entry.Ts > 0 {
			ts = time.UnixMilli(entry.Ts)
		} else if entry.Timestamp != "" {
			ts, _ = time.Parse(time.RFC3339Nano, entry.Timestamp)
		}

		if !cutoff.IsZero() && !ts.IsZero() && ts.Before(cutoff) {
			continue
		}

		if !ts.IsZero() {
			if sessionStart.IsZero() {
				sessionStart = ts
			}
			sessionEnd = ts
		}

		if entry.Type == "cost" && len(entry.Message) > 0 {
			var cost struct {
				InputTokens  int     `json:"inputTokens"`
				OutputTokens int     `json:"outputTokens"`
				CostUSD      float64 `json:"costUSD"`
			}
			if json.Unmarshal(entry.Message, &cost) == nil {
				out.TotalInputTok += cost.InputTokens
				out.TotalOutputTok += cost.OutputTokens
				out.TotalCostUSD += cost.CostUSD
			}
			continue
		}

		var (
			role   string
			model  string
			inTok  int
			outTok int
		)

		parseMsg := func(raw json.RawMessage) {
			var msg struct {
				Role  string `json:"role"`
				Model string `json:"model"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(raw, &msg) == nil {
				if msg.Role != "" {
					role = msg.Role
				}
				if msg.Model != "" {
					model = msg.Model
				}
				inTok = msg.Usage.InputTokens
				outTok = msg.Usage.OutputTokens
			}
		}

		switch entry.Type {
		case "user":
			role = "user"
		case "assistant":
			role = "assistant"
			if len(entry.Message) > 0 {
				parseMsg(entry.Message)
				role = "assistant"
			}
		case "message":
			if len(entry.Message) > 0 {
				parseMsg(entry.Message)
			}
		default:
			continue
		}

		if role != "user" && role != "assistant" {
			continue
		}

		out.TotalMessages++
		if !ts.IsZero() {
			out.DailyCounts[ts.Format("2006-01-02")]++
		}

		if role == "assistant" && model != "" && model != "<synthetic>" && (inTok > 0 || outTok > 0) {
			mu := out.ModelUsage[model]
			mu.InputTokens += inTok
			mu.OutputTokens += outTok
			mu.Sessions++
			out.ModelUsage[model] = mu
			out.TotalInputTok += inTok
			out.TotalOutputTok += outTok
			if !ts.IsZero() {
				out.DailyTokens[ts.Format("2006-01-02")] += inTok + outTok
			}
		}
	}

	if !sessionStart.IsZero() && !sessionEnd.IsZero() {
		dur := sessionEnd.Sub(sessionStart)
		if dur > out.LongestSession {
			out.LongestSession = dur
		}
	}
}

func computeStreaks(dailyCounts map[string]int) (longest, current int) {
	if len(dailyCounts) == 0 {
		return
	}
	var days []string
	for d := range dailyCounts {
		days = append(days, d)
	}
	sort.Strings(days)

	streak := 1
	for i := 1; i < len(days); i++ {
		prev, _ := time.Parse("2006-01-02", days[i-1])
		curr, _ := time.Parse("2006-01-02", days[i])
		if curr.Sub(prev) == 24*time.Hour {
			streak++
		} else {
			if streak > longest {
				longest = streak
			}
			streak = 1
		}
	}
	if streak > longest {
		longest = streak
	}

	// Current streak counting back from today or yesterday.
	for _, startOffset := range []int{0, 1} {
		start := time.Now().AddDate(0, 0, -startOffset).Format("2006-01-02")
		if _, ok := dailyCounts[start]; !ok {
			continue
		}
		cur := 1
		for i := startOffset + 1; ; i++ {
			day := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
			if _, ok := dailyCounts[day]; ok {
				cur++
			} else {
				break
			}
		}
		current = cur
		break
	}
	return longest, current
}

func mostActiveDay(dailyCounts map[string]int) string {
	best, bestCount := "", 0
	for d, c := range dailyCounts {
		if c > bestCount {
			bestCount = c
			best = d
		}
	}
	if best == "" {
		return "—"
	}
	t, err := time.Parse("2006-01-02", best)
	if err != nil {
		return best
	}
	return t.Format("Jan 2")
}
