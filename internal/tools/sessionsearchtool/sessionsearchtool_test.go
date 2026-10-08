package sessionsearchtool

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/icehunter/conduit/internal/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/sessionsearch"
)

func TestExecute_ReindexesAtMostEvery30s(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CONDUIT_CONFIG_DIR", dir+"/.conduit")
	if err := os.MkdirAll(dir+"/.conduit", 0o700); err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	tl := New()
	tl.now = func() time.Time { return clock }
	tl.indexAll = func(*sessionsearch.DB, string) error { calls++; return nil }

	run := func() {
		t.Helper()
		res, err := tl.Execute(context.Background(), json.RawMessage(`{}`))
		if err != nil || res.IsError {
			t.Fatalf("Execute: %v %+v", err, res)
		}
	}

	steps := []struct {
		name    string
		advance time.Duration
		want    int
	}{
		{"first call opens and indexes once", 0, 1},
		{"immediate second call is throttled", time.Second, 1},
		{"just under 30s still throttled", 28 * time.Second, 1},
		{"30s after last index refreshes", 2 * time.Second, 2},
		{"throttle restarts from the refresh", 10 * time.Second, 2},
		{"another 30s refreshes again", 25 * time.Second, 3},
	}
	for _, st := range steps {
		clock = clock.Add(st.advance)
		run()
		if calls != st.want {
			t.Fatalf("%s: indexAll calls = %d, want %d", st.name, calls, st.want)
		}
	}
}

func TestParseBound(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"2026-06-01", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), false},
		{"2026-06-01T09:30:00Z", time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC), false},
		{"2026-06-01T09:30:00+02:00", time.Date(2026, 6, 1, 7, 30, 0, 0, time.UTC), false},
		{"7d", now.Add(-7 * 24 * time.Hour), false},
		{"24H", now.Add(-24 * time.Hour), false},
		{"2w", now.Add(-14 * 24 * time.Hour), false},
		{" 3 d ", now.Add(-3 * 24 * time.Hour), false},
		{"yesterday", time.Time{}, true},
		{"2026-13-01", time.Time{}, true},
		{"", time.Time{}, true},
		{"999999999d", time.Time{}, true},
		{"9223372036854775807w", time.Time{}, true},
		{"99999999999999999999h", time.Time{}, true},
		{"876000h", now.Add(-876000 * time.Hour), false},
		{"876001h", time.Time{}, true},
		{"-1d", time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseBound(tt.in, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterFromInput(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	cur := func() string { return "live" }
	tests := []struct {
		name        string
		tool        *Tool
		in          Input
		wantExclude []string
		wantAfter   time.Time
		wantErr     bool
	}{
		{"empty", New(), Input{}, nil, time.Time{}, false},
		{"explicit ids deduped and trimmed", New(), Input{ExcludeSessionIDs: []string{" a ", "a", "", "b"}}, []string{"a", "b"}, time.Time{}, false},
		{"current session excluded", New(WithCurrentSession(cur)), Input{ExcludeCurrentSession: true, ExcludeSessionIDs: []string{"a"}}, []string{"live", "a"}, time.Time{}, false},
		{"current flag off", New(WithCurrentSession(cur)), Input{ExcludeSessionIDs: []string{"a"}}, []string{"a"}, time.Time{}, false},
		{"current flag without provider errors", New(), Input{ExcludeCurrentSession: true}, nil, time.Time{}, true},
		{"current flag with empty id errors", New(WithCurrentSession(func() string { return "" })), Input{ExcludeCurrentSession: true}, nil, time.Time{}, true},
		{"after parsed", New(), Input{After: "2026-06-01"}, nil, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), false},
		{"bad after", New(), Input{After: "soon"}, nil, time.Time{}, true},
		{"bad before", New(), Input{Before: "soon"}, nil, time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.tool.now = func() time.Time { return now }
			f, err := tt.tool.filter(context.Background(), tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if fmt.Sprint(f.ExcludeSessions) != fmt.Sprint(tt.wantExclude) {
				t.Errorf("exclude = %v, want %v", f.ExcludeSessions, tt.wantExclude)
			}
			if !f.After.Equal(tt.wantAfter) {
				t.Errorf("after = %v, want %v", f.After, tt.wantAfter)
			}
		})
	}
}

func TestFilter_ExcludeCap(t *testing.T) {
	var ids []string
	for i := 0; i < 50; i++ {
		ids = append(ids, fmt.Sprintf("s%d", i))
	}
	f, err := New().filter(context.Background(), Input{ExcludeSessionIDs: ids})
	if err != nil || len(f.ExcludeSessions) != sessionsearch.MaxExcludedSessions {
		t.Fatalf("len=%d err=%v", len(f.ExcludeSessions), err)
	}
}

func TestExecute_FiltersEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	conduit := filepath.Join(home, ".conduit")
	t.Setenv("CONDUIT_CONFIG_DIR", conduit)
	proj := filepath.Join(conduit, "projects", "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id string, mt time.Time) {
		t.Helper()
		line := `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"marmot question ` + id + `"}]}}` + "\n"
		p := filepath.Join(proj, id+".jsonl")
		if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	write("old-one", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	write("new-one", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	write("live-one", time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC))

	tl := New(WithCurrentSession(func() string { return "live-one" }))
	tl.now = func() time.Time { return time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name     string
		input    string
		contains []string
		absent   []string
		wantErr  bool
	}{
		{"no filter", `{"query":"marmot"}`, []string{"old-one", "new-one", "live-one"}, nil, false},
		{"after", `{"query":"marmot","after":"2026-05-01"}`, []string{"new-one", "live-one"}, []string{"old-one"}, false},
		{"before", `{"query":"marmot","before":"2026-05-01"}`, []string{"old-one"}, []string{"new-one", "live-one"}, false},
		{"relative after", `{"query":"marmot","after":"30d"}`, []string{"new-one", "live-one"}, []string{"old-one"}, false},
		{"exclude ids", `{"query":"marmot","exclude_session_ids":["new-one"]}`, []string{"old-one", "live-one"}, []string{"new-one"}, false},
		{"exclude current", `{"query":"marmot","exclude_current_session":true}`, []string{"old-one", "new-one"}, []string{"live-one"}, false},
		{"browse with filter", `{"after":"2026-05-01","exclude_current_session":true}`, []string{"new-one"}, []string{"old-one", "live-one"}, false},
		{"bad date", `{"query":"marmot","after":"nonsense"}`, []string{"after"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tl.Execute(context.Background(), json.RawMessage(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			for _, b := range res.Content {
				sb.WriteString(b.Text)
			}
			out := sb.String()
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError=%v out=%s", res.IsError, out)
			}
			for _, c := range tt.contains {
				if !strings.Contains(out, c) {
					t.Errorf("missing %q in:\n%s", c, out)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(out, a) {
					t.Errorf("unexpected %q in:\n%s", a, out)
				}
			}
		})
	}
}

func TestFilter_ContextSessionIDWinsOverStartupGetter(t *testing.T) {
	tl := New(WithCurrentSession(func() string { return "startup" }))
	ctx := tool.WithSessionID(context.Background(), "resumed")
	f, err := tl.filter(ctx, Input{ExcludeCurrentSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.ExcludeSessions) != 1 || f.ExcludeSessions[0] != "resumed" {
		t.Errorf("ExcludeSessions = %v, want [resumed]", f.ExcludeSessions)
	}
	// With no provider at all, the context id alone suffices.
	f, err = New().filter(ctx, Input{ExcludeCurrentSession: true})
	if err != nil || len(f.ExcludeSessions) != 1 || f.ExcludeSessions[0] != "resumed" {
		t.Errorf("no getter: %v, %v", f.ExcludeSessions, err)
	}
}
