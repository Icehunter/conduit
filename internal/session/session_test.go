package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
)

func newTestSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	s := &Session{
		ID:         "test-id",
		ProjectDir: dir,
		FilePath:   filepath.Join(dir, "test-id.jsonl"),
	}
	return s
}

// --- cost persistence ---

func TestAppendCost_AndLoad(t *testing.T) {
	s := newTestSession(t)

	if err := s.AppendCost(1234, 5678, 0.0123); err != nil {
		t.Fatalf("AppendCost: %v", err)
	}

	cost, err := LoadCost(s.FilePath)
	if err != nil {
		t.Fatalf("LoadCost: %v", err)
	}
	if cost.InputTokens != 1234 {
		t.Errorf("InputTokens = %d; want 1234", cost.InputTokens)
	}
	if cost.OutputTokens != 5678 {
		t.Errorf("OutputTokens = %d; want 5678", cost.OutputTokens)
	}
	if cost.CostUSD < 0.012 || cost.CostUSD > 0.013 {
		t.Errorf("CostUSD = %f; want ~0.0123", cost.CostUSD)
	}
}

func TestLoadCost_NoFile(t *testing.T) {
	cost, err := LoadCost("/nonexistent/path.jsonl")
	if err != nil {
		t.Fatalf("LoadCost on missing file: %v", err)
	}
	if cost.InputTokens != 0 || cost.CostUSD != 0 {
		t.Errorf("expected zero cost for missing file; got %+v", cost)
	}
}

func TestLoadCost_AccumulatesMultipleEntries(t *testing.T) {
	s := newTestSession(t)

	_ = s.AppendCost(100, 50, 0.01)
	_ = s.AppendCost(200, 80, 0.02)

	cost, err := LoadCost(s.FilePath)
	if err != nil {
		t.Fatalf("LoadCost: %v", err)
	}
	// LoadCost returns the last entry (most recent snapshot), not accumulated.
	if cost.InputTokens != 200 {
		t.Errorf("InputTokens = %d; want 200 (last entry)", cost.InputTokens)
	}
}

// --- session title ---

func TestExtractTitle_FromFirstUserMessage(t *testing.T) {
	s := newTestSession(t)

	msg := api.Message{
		Role: "user",
		Content: []api.ContentBlock{{
			Type: "text",
			Text: "Can you help me refactor this function?",
		}},
	}
	_ = s.AppendMessage(msg)

	title := ExtractTitle(s.FilePath)
	if !strings.Contains(title, "refactor") {
		t.Errorf("title = %q; should contain first user message text", title)
	}
}

func TestExtractTitle_Truncated(t *testing.T) {
	s := newTestSession(t)

	msg := api.Message{
		Role: "user",
		Content: []api.ContentBlock{{
			Type: "text",
			Text: strings.Repeat("a very long message that goes on and on ", 10),
		}},
	}
	_ = s.AppendMessage(msg)

	title := ExtractTitle(s.FilePath)
	if len([]rune(title)) > 60 {
		t.Errorf("title too long: %d runes; want ≤60", len([]rune(title)))
	}
}

func TestExtractTitle_CustomTitle(t *testing.T) {
	s := newTestSession(t)
	_ = s.SetTitle("My Custom Title")

	title := ExtractTitle(s.FilePath)
	if title != "My Custom Title" {
		t.Errorf("title = %q; want custom title", title)
	}
}

func TestExtractTitle_NoMessages(t *testing.T) {
	s := newTestSession(t)
	title := ExtractTitle(s.FilePath)
	if title != "" {
		t.Errorf("title = %q; want empty for empty session", title)
	}
}

// --- transcript search ---

func TestSearch_FindsMatch(t *testing.T) {
	s := newTestSession(t)

	msg1 := api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello world"}}}
	msg2 := api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: "hi there"}}}
	_ = s.AppendMessage(msg1)
	_ = s.AppendMessage(msg2)

	results, err := Search(s.FilePath, "hello")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result for 'hello'")
	}
	if !strings.Contains(results[0].Text, "hello") {
		t.Errorf("result text = %q; should contain 'hello'", results[0].Text)
	}
}

func TestSearch_NoMatch(t *testing.T) {
	s := newTestSession(t)
	_ = s.AppendMessage(api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "nothing here"}}})

	results, err := Search(s.FilePath, "zzznomatch")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results; got %d", len(results))
	}
}

func TestSearch_CaseInsensitive(t *testing.T) {
	s := newTestSession(t)
	_ = s.AppendMessage(api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "UPPERCASE query"}}})

	results, err := Search(s.FilePath, "uppercase")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("search should be case-insensitive")
	}
}

// --- file access tracking ---

func TestAppendFileAccess(t *testing.T) {
	s := newTestSession(t)

	_ = s.AppendFileAccess("read", "/path/to/file.go")
	_ = s.AppendFileAccess("write", "/path/to/other.go")

	files, err := LoadFileAccess(s.FilePath)
	if err != nil {
		t.Fatalf("LoadFileAccess: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 file access entries; got %d", len(files))
	}
	if files[0].Path != "/path/to/file.go" || files[0].Op != "read" {
		t.Errorf("files[0] = %+v; want read /path/to/file.go", files[0])
	}
}

func TestLoadFileAccess_Empty(t *testing.T) {
	s := newTestSession(t)
	files, err := LoadFileAccess(s.FilePath)
	if err != nil {
		t.Fatalf("LoadFileAccess: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty for new session; got %d", len(files))
	}
}

// ensure json import is used
var _ = json.Marshal
