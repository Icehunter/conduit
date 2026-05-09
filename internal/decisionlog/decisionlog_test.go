package decisionlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// withHome relocates HOME for the test so Append/Recent stay sandboxed.
// Returns a cleanup that restores the previous HOME.
func withHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
}

func TestAppendAndRecent(t *testing.T) {
	withHome(t)
	cwd, _ := os.Getwd()

	now := time.Now().UTC()
	entries := []Entry{
		{Timestamp: now.Add(-2 * time.Hour), Kind: KindChose, Scope: "auth", Summary: "use middleware B"},
		{Timestamp: now.Add(-1 * time.Hour), Kind: KindRuledOut, Scope: "auth", Summary: "pattern A — fails compliance"},
		{Timestamp: now, Kind: KindConstraint, Scope: "session-tokens", Summary: "must encrypt at rest"},
	}
	for _, e := range entries {
		if err := Append(cwd, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := Recent(cwd, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	// Newest first.
	if got[0].Scope != "session-tokens" {
		t.Errorf("want newest first; got %q", got[0].Scope)
	}
	if got[2].Summary != "use middleware B" {
		t.Errorf("want oldest last; got %q", got[2].Summary)
	}
}

func TestAppend_RejectsEmptySummary(t *testing.T) {
	withHome(t)
	cwd, _ := os.Getwd()
	err := Append(cwd, Entry{Kind: KindChose, Scope: "x"})
	if !errors.Is(err, ErrEmptySummary) {
		t.Fatalf("want ErrEmptySummary, got %v", err)
	}
}

func TestAppend_TruncatesOverlongFields(t *testing.T) {
	withHome(t)
	cwd, _ := os.Getwd()
	long := strings.Repeat("x", maxSummaryLen*3)
	if err := Append(cwd, Entry{Kind: KindChose, Scope: "x", Summary: long}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := Recent(cwd, 1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 entry")
	}
	if len([]rune(got[0].Summary)) > maxSummaryLen {
		t.Errorf("summary not truncated: %d runes", len([]rune(got[0].Summary)))
	}
}

func TestAppend_DefaultsKindAndTimestamp(t *testing.T) {
	withHome(t)
	cwd, _ := os.Getwd()
	if err := Append(cwd, Entry{Summary: "no kind, no ts"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, _ := Recent(cwd, 1)
	if got[0].Kind != KindChose {
		t.Errorf("want default Kind=chose, got %q", got[0].Kind)
	}
	if got[0].Timestamp.IsZero() {
		t.Errorf("Timestamp should default to now")
	}
}

func TestRecent_TolerantOfBadLines(t *testing.T) {
	withHome(t)
	cwd, _ := os.Getwd()
	// Plant a malformed line alongside a good one.
	dir := Dir(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	contents := `{not valid json
{"ts":"2024-01-01T00:00:00Z","kind":"chose","scope":"x","summary":"good"}
also-not-json
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(cwd, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "good" {
		t.Errorf("want 1 good entry; got %+v", got)
	}
}

func TestRelevantToScope_KeywordMatch(t *testing.T) {
	withHome(t)
	cwd, _ := os.Getwd()
	mustAppend := func(e Entry) {
		t.Helper()
		if err := Append(cwd, e); err != nil {
			t.Fatal(err)
		}
	}
	mustAppend(Entry{Scope: "authentication", Summary: "middleware B"})
	mustAppend(Entry{Scope: "billing", Summary: "use Stripe webhooks"})
	mustAppend(Entry{Scope: "logging", Summary: "structured zerolog"})

	got, err := RelevantToScope(cwd, "authentication middleware patterns", 10)
	if err != nil {
		t.Fatalf("RelevantToScope: %v", err)
	}
	if len(got) != 1 || got[0].Scope != "authentication" {
		t.Errorf("want 1 auth match; got %+v", got)
	}
}

func TestRelevantToScope_EmptyQueryReturnsRecent(t *testing.T) {
	withHome(t)
	cwd, _ := os.Getwd()
	if err := Append(cwd, Entry{Summary: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(cwd, Entry{Summary: "second"}); err != nil {
		t.Fatal(err)
	}
	got, err := RelevantToScope(cwd, "", 10)
	if err != nil {
		t.Fatalf("RelevantToScope: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2; got %d", len(got))
	}
}

func TestAppend_ConcurrentSafe(t *testing.T) {
	withHome(t)
	cwd, _ := os.Getwd()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for range n {
		go func() {
			defer wg.Done()
			if err := Append(cwd, Entry{Kind: KindChose, Scope: "x", Summary: "concurrent"}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Append: %v", err)
	}
	got, err := Recent(cwd, n+10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != n {
		t.Errorf("want %d entries, got %d (no torn writes)", n, len(got))
	}
}

func TestFormatPromptBlock(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		entries  []Entry
		byteCap  int
		wantSubs []string
		wantSkip []string
	}{
		{
			name:     "empty returns empty string",
			entries:  nil,
			wantSubs: nil,
		},
		{
			name: "renders kind/scope/summary/why",
			entries: []Entry{
				{Timestamp: now.Add(-1 * time.Hour), Kind: KindChose, Scope: "auth", Summary: "middleware B", Why: "compliance"},
			},
			wantSubs: []string{"## Recent project decisions", "[chose] auth: middleware B", "compliance", "1h ago"},
		},
		{
			name: "byteCap elides remaining entries",
			entries: []Entry{
				{Timestamp: now, Kind: KindChose, Scope: "a", Summary: "first decision text"},
				{Timestamp: now, Kind: KindChose, Scope: "b", Summary: "second decision text"},
			},
			byteCap:  280, // fits header + first entry only (header≈270 + first line≈44)
			wantSubs: []string{"first decision", "older entries elided"},
			wantSkip: []string{"second decision"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatPromptBlock(tt.entries, tt.byteCap)
			if len(tt.entries) == 0 {
				if got != "" {
					t.Errorf("want empty; got %q", got)
				}
				return
			}
			for _, sub := range tt.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("want substring %q in:\n%s", sub, got)
				}
			}
			for _, sub := range tt.wantSkip {
				if strings.Contains(got, sub) {
					t.Errorf("did not want substring %q in:\n%s", sub, got)
				}
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a b c", nil}, // all too short
		{"Authentication MIDDLEWARE", []string{"authentication", "middleware"}},
		{"foo-bar/baz_qux", []string{"foo", "bar", "baz", "qux"}},
	}
	for _, tt := range tests {
		got := tokenize(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}
