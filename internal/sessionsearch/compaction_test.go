package sessionsearch_test

import (
	"path/filepath"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/session"
)

func tm(role, text string) api.Message {
	return api.Message{Role: role, Content: []api.ContentBlock{{Type: "text", Text: text}}}
}

func TestIndexSkipsCompactionCopies(t *testing.T) {
	dir := t.TempDir()
	s := session.FromFile(filepath.Join(dir, "sess-c.jsonl"))
	for _, m := range []api.Message{tm("user", "alpha"), tm("assistant", "beta"), tm("user", "gamma"), tm("assistant", "delta")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// History after compaction: summary message + verbatim tail (gamma, delta).
	hist := []api.Message{tm("user", "<summary>SUMMARYTEXT</summary>"), tm("user", "gamma"), tm("assistant", "delta")}
	if err := s.AppendCompaction("SUMMARYTEXT", hist, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(tm("user", "epsilon")); err != nil {
		t.Fatal(err)
	}

	db := openMemDB(t)
	if err := db.Index(dir); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"gamma", "delta", "SUMMARYTEXT"} {
		ws, err := db.Search(q, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, w := range ws {
			for _, m := range w.Messages {
				if m.IsMatch {
					n++
				}
			}
		}
		want := 1
		if q == "SUMMARYTEXT" {
			want = 0
		}
		if n != want {
			t.Errorf("%q matched %d messages, want %d", q, n, want)
		}
	}
	w, err := db.Scroll("sess-c", 0, 20)
	if err != nil || w == nil {
		t.Fatalf("scroll: %v", err)
	}
	if len(w.Messages) != 5 { // alpha beta gamma delta epsilon
		t.Errorf("indexed %d messages, want 5", len(w.Messages))
	}
}
