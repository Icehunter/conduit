package sessionsearch_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/sessionsearch"
)

// makeJSONL writes a synthetic *.jsonl file in dir and returns its path.
// lines is a slice of (role, text) pairs.
func makeJSONL(t *testing.T, dir, sessionID string, lines []struct{ role, text string }) string {
	t.Helper()
	path := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("makeJSONL: create: %v", err)
	}
	defer f.Close()

	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type apiMsg struct {
		Role    string         `json:"role"`
		Content []contentBlock `json:"content"`
	}
	type entry struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}

	enc := json.NewEncoder(f)
	for _, l := range lines {
		raw, err := json.Marshal(apiMsg{
			Role:    l.role,
			Content: []contentBlock{{Type: "text", Text: l.text}},
		})
		if err != nil {
			t.Fatalf("makeJSONL: marshal msg: %v", err)
		}
		if err := enc.Encode(entry{Type: "message", Message: raw}); err != nil {
			t.Fatalf("makeJSONL: encode entry: %v", err)
		}
	}
	return path
}

func openMemDB(t *testing.T) *sessionsearch.DB {
	t.Helper()
	db, err := sessionsearch.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenAndClose(t *testing.T) {
	db := openMemDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestIndexAndSearch(t *testing.T) {
	dir := t.TempDir()
	makeJSONL(t, dir, "sess-abc", []struct{ role, text string }{
		{"user", "How do I configure the widget?"},
		{"assistant", "Set the widget_config field in your settings file."},
	})

	db := openMemDB(t)
	if err := db.Index(dir); err != nil {
		t.Fatalf("index: %v", err)
	}

	windows, err := db.Search("widget_config", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(windows) == 0 {
		t.Fatal("expected at least one window, got none")
	}
	if windows[0].SessionID != "sess-abc" {
		t.Errorf("got session_id %q, want sess-abc", windows[0].SessionID)
	}

	// Verify IsMatch is set on the matching message.
	foundMatch := false
	for _, m := range windows[0].Messages {
		if m.IsMatch {
			foundMatch = true
		}
	}
	if !foundMatch {
		t.Error("no message marked IsMatch in window")
	}
}

func TestSearchNoResults(t *testing.T) {
	dir := t.TempDir()
	makeJSONL(t, dir, "sess-xyz", []struct{ role, text string }{
		{"user", "hello world"},
	})

	db := openMemDB(t)
	if err := db.Index(dir); err != nil {
		t.Fatalf("index: %v", err)
	}

	windows, err := db.Search("nonexistent_query_abc123", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(windows) != 0 {
		t.Errorf("expected no windows, got %d", len(windows))
	}
}

func TestScroll(t *testing.T) {
	dir := t.TempDir()
	msgs := []struct{ role, text string }{
		{"user", "first message"},
		{"assistant", "second message"},
		{"user", "third message"},
		{"assistant", "fourth message"},
		{"user", "fifth message"},
	}
	makeJSONL(t, dir, "sess-scroll", msgs)

	db := openMemDB(t)
	if err := db.Index(dir); err != nil {
		t.Fatalf("index: %v", err)
	}

	// Scroll around index 2 with window 1 on each side.
	w, err := db.Scroll("sess-scroll", 2, 1)
	if err != nil {
		t.Fatalf("scroll: %v", err)
	}
	if w == nil {
		t.Fatal("expected window, got nil")
	}
	if len(w.Messages) < 2 {
		t.Errorf("expected at least 2 messages, got %d", len(w.Messages))
	}
	// Index 2 must be present.
	found := false
	for _, m := range w.Messages {
		if m.Index == 2 {
			found = true
		}
	}
	if !found {
		t.Error("anchor message (index 2) not in window")
	}
}

func TestScrollUnknownSession(t *testing.T) {
	db := openMemDB(t)
	w, err := db.Scroll("nonexistent", 0, 3)
	if err != nil {
		t.Fatalf("scroll unknown: %v", err)
	}
	if w != nil {
		t.Errorf("expected nil window for unknown session, got %+v", w)
	}
}

func TestBrowse(t *testing.T) {
	dir := t.TempDir()
	makeJSONL(t, dir, "sess-a", []struct{ role, text string }{
		{"user", "alpha session content"},
	})
	makeJSONL(t, dir, "sess-b", []struct{ role, text string }{
		{"user", "beta session content"},
		{"assistant", "beta reply"},
	})

	db := openMemDB(t)
	if err := db.Index(dir); err != nil {
		t.Fatalf("index: %v", err)
	}

	summaries, err := db.Browse(10)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(summaries) != 2 {
		t.Errorf("expected 2 summaries, got %d", len(summaries))
	}
	for _, s := range summaries {
		if s.SessionID == "" {
			t.Error("summary with empty session ID")
		}
		if s.Date.IsZero() {
			t.Error("summary with zero date")
		}
		if s.MessageCount <= 0 {
			t.Errorf("session %s: message_count %d, want >0", s.SessionID, s.MessageCount)
		}
	}
}

func TestBrowseEmpty(t *testing.T) {
	db := openMemDB(t)
	summaries, err := db.Browse(10)
	if err != nil {
		t.Fatalf("browse empty: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("expected 0 summaries, got %d", len(summaries))
	}
}

func TestIncrementalIndex(t *testing.T) {
	dir := t.TempDir()
	path := makeJSONL(t, dir, "sess-incr", []struct{ role, text string }{
		{"user", "initial content"},
	})

	db := openMemDB(t)
	if err := db.Index(dir); err != nil {
		t.Fatalf("first index: %v", err)
	}

	// Verify we get a result.
	w, err := db.Search("initial", 5)
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	if len(w) == 0 {
		t.Fatal("expected result after first index")
	}

	// Touch the file with a newer mtime to trigger re-index.
	futureTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, futureTime, futureTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Append new content to the file.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type apiMsg struct {
		Role    string         `json:"role"`
		Content []contentBlock `json:"content"`
	}
	type entry struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	raw, _ := json.Marshal(apiMsg{Role: "assistant", Content: []contentBlock{{Type: "text", Text: "updated unique phrase xyz987"}}})
	enc := json.NewEncoder(f)
	_ = enc.Encode(entry{Type: "message", Message: raw})
	_ = f.Close()

	if err := db.Index(dir); err != nil {
		t.Fatalf("second index: %v", err)
	}

	w2, err := db.Search("xyz987", 5)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if len(w2) == 0 {
		t.Fatal("expected result for new content after re-index")
	}
}

func TestIndexEmptyDir(t *testing.T) {
	dir := t.TempDir()
	db := openMemDB(t)
	if err := db.Index(dir); err != nil {
		t.Fatalf("index empty dir: %v", err)
	}
}

func TestIndexNonExistentDir(t *testing.T) {
	db := openMemDB(t)
	if err := db.Index("/tmp/conduit-does-not-exist-xyz"); err != nil {
		t.Fatalf("index nonexistent dir should not error: %v", err)
	}
}
