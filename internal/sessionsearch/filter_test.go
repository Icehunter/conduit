package sessionsearch_test

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/sessionsearch"
)

// indexDatedSessions writes one session per (id, mtime) all containing the
// word "pelican", pins each file's mtime, and indexes them.
func indexDatedSessions(t *testing.T, sessions map[string]time.Time) *sessionsearch.DB {
	t.Helper()
	dir := t.TempDir()
	for id, mt := range sessions {
		p := makeJSONL(t, dir, id, []struct{ role, text string }{{"user", "pelican question for " + id}})
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	db := openMemDB(t)
	if err := db.Index(dir); err != nil {
		t.Fatal(err)
	}
	return db
}

func windowIDs(ws []sessionsearch.MessageWindow) []string {
	ids := make([]string, 0, len(ws))
	for _, w := range ws {
		ids = append(ids, w.SessionID)
	}
	sort.Strings(ids)
	return ids
}

func summaryIDs(ss []sessionsearch.SessionSummary) []string {
	ids := make([]string, 0, len(ss))
	for _, s := range ss {
		ids = append(ids, s.SessionID)
	}
	sort.Strings(ids)
	return ids
}

func day(d int) time.Time { return time.Date(2026, 6, d, 12, 0, 0, 0, time.UTC) }

func TestSearchFiltered(t *testing.T) {
	db := indexDatedSessions(t, map[string]time.Time{"s1": day(1), "s2": day(10), "s3": day(20)})
	tests := []struct {
		name string
		f    sessionsearch.Filter
		want []string
	}{
		{"no filter", sessionsearch.Filter{}, []string{"s1", "s2", "s3"}},
		{"after inclusive", sessionsearch.Filter{After: day(10)}, []string{"s2", "s3"}},
		{"after just past", sessionsearch.Filter{After: day(10).Add(time.Millisecond)}, []string{"s3"}},
		{"before exclusive", sessionsearch.Filter{Before: day(10)}, []string{"s1"}},
		{"before just past", sessionsearch.Filter{Before: day(10).Add(time.Millisecond)}, []string{"s1", "s2"}},
		{"range", sessionsearch.Filter{After: day(5), Before: day(15)}, []string{"s2"}},
		{"empty range", sessionsearch.Filter{After: day(15), Before: day(5)}, nil},
		{"exclude one", sessionsearch.Filter{ExcludeSessions: []string{"s2"}}, []string{"s1", "s3"}},
		{"exclude many", sessionsearch.Filter{ExcludeSessions: []string{"s1", "s3", "nope"}}, []string{"s2"}},
		{"exclude all", sessionsearch.Filter{ExcludeSessions: []string{"s1", "s2", "s3"}}, nil},
		{"range and exclude", sessionsearch.Filter{After: day(5), ExcludeSessions: []string{"s3"}}, []string{"s2"}},
		{"unknown project", sessionsearch.Filter{Project: "nope"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, err := db.SearchFiltered("pelican", tt.f, 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := windowIDs(ws); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("search = %v, want %v", got, tt.want)
			}
			ss, err := db.BrowseFiltered(tt.f, 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := summaryIDs(ss); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("browse = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSearchFiltered_ExcludeCapped(t *testing.T) {
	db := indexDatedSessions(t, map[string]time.Time{"keep": day(1)})
	var ids []string
	for i := 0; i < sessionsearch.MaxExcludedSessions; i++ {
		ids = append(ids, fmt.Sprintf("filler-%d", i))
	}
	ids = append(ids, "keep") // beyond the cap: ignored
	ws, err := db.SearchFiltered("pelican", sessionsearch.Filter{ExcludeSessions: ids}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 {
		t.Errorf("exclusions past the cap must be ignored, got %v", windowIDs(ws))
	}
}

func TestSearchFiltered_DelegationKeepsProjectFilter(t *testing.T) {
	db := indexDatedSessions(t, map[string]time.Time{"s1": day(1)})
	if ws, err := db.Search("pelican", "", 5); err != nil || len(ws) != 1 {
		t.Fatalf("Search = %v, %v", ws, err)
	}
	if ws, err := db.Search("pelican", "missing-project", 5); err != nil || len(ws) != 0 {
		t.Fatalf("Search with project = %v, %v", ws, err)
	}
}
