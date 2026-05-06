package sessionstats

import (
	"testing"
	"time"
)

func TestStatsFromCacheAllTime(t *testing.T) {
	cache := &statsCacheFile{
		TotalSessions:    2,
		TotalMessages:    5,
		FirstSessionDate: "2026-05-01T12:00:00Z",
		DailyActivity: []struct {
			Date         string `json:"date"`
			MessageCount int    `json:"messageCount"`
			SessionCount int    `json:"sessionCount"`
		}{
			{Date: "2026-05-02", MessageCount: 2, SessionCount: 1},
			{Date: "2026-05-01", MessageCount: 3, SessionCount: 1},
		},
		DailyModelTokens: []struct {
			Date          string         `json:"date"`
			TokensByModel map[string]int `json:"tokensByModel"`
		}{
			{Date: "2026-05-02", TokensByModel: map[string]int{"claude-opus": 20}},
			{Date: "2026-05-01", TokensByModel: map[string]int{"claude-sonnet": 10}},
		},
		ModelUsage: map[string]struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
		}{
			"claude-opus":   {InputTokens: 12, OutputTokens: 8},
			"claude-sonnet": {InputTokens: 7, OutputTokens: 3},
		},
	}
	cache.LongestSession.Duration = int64((90 * time.Minute) / time.Millisecond)

	got := statsFromCache(cache, 0)
	if got.TotalSessions != 2 || got.TotalMessages != 5 {
		t.Fatalf("totals = (%d, %d), want (2, 5)", got.TotalSessions, got.TotalMessages)
	}
	if got.TotalInputTok != 19 || got.TotalOutputTok != 11 {
		t.Fatalf("tokens = (%d, %d), want (19, 11)", got.TotalInputTok, got.TotalOutputTok)
	}
	if got.DailyCounts["2026-05-01"] != 3 || got.DailyTokens["2026-05-02"] != 20 {
		t.Fatalf("daily maps not populated correctly: counts=%v tokens=%v", got.DailyCounts, got.DailyTokens)
	}
	if len(got.DailyModelTokens) != 2 || got.DailyModelTokens[0].Date != "2026-05-01" {
		t.Fatalf("daily model tokens not sorted: %+v", got.DailyModelTokens)
	}
	if got.ModelUsage["claude-opus"].InputTokens != 12 {
		t.Fatalf("model usage not exported/populated: %+v", got.ModelUsage)
	}
	if got.LongestSession != 90*time.Minute {
		t.Fatalf("longest session = %s, want 1h30m", got.LongestSession)
	}
}
