package session

import (
	"encoding/json"
	"os"
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

func TestAppendMessagePreservesProviderMetadata(t *testing.T) {
	s := newTestSession(t)
	msg := api.Message{
		Role:         "assistant",
		Content:      []api.ContentBlock{{Type: "text", Text: "local answer"}},
		ProviderKind: "mcp",
		Provider:     "local-router",
	}
	if err := s.AppendMessage(msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	msgs, err := s.LoadMessages()
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("LoadMessages len = %d, want 1", len(msgs))
	}
	if msgs[0].ProviderKind != "mcp" || msgs[0].Provider != "local-router" {
		t.Fatalf("provider metadata = %q/%q, want mcp/local-router", msgs[0].ProviderKind, msgs[0].Provider)
	}
	data, err := json.Marshal(msgs[0])
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	if strings.Contains(string(data), "provider") || strings.Contains(string(data), "local-router") {
		t.Fatalf("provider metadata leaked into API message JSON: %s", data)
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

func TestExtractTitle_ClaudeCodeStringContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	writeJSONL(t, path,
		`{"type":"summary","summary":"metadata only"}`,
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"Please fix /resume loading history"},"timestamp":"2026-05-04T16:50:29.444Z"}`,
	)

	title := ExtractTitle(path)
	if title != "Please fix /resume loading history" {
		t.Errorf("title = %q; want first Claude-style user message", title)
	}
}

func TestLoadMessages_ClaudeCodeTranscriptStringContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	writeJSONL(t, path,
		`{"type":"summary","summary":"metadata only"}`,
		`{"uuid":"u1","parentUuid":null,"type":"user","message":{"role":"user","content":"hello from Claude Code"},"timestamp":"2026-05-04T16:50:29.444Z"}`,
		`{"uuid":"a1","parentUuid":"u1","type":"assistant","message":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"history restored"}]},"timestamp":"2026-05-04T16:50:36.716Z"}`,
	)

	msgs, err := LoadMessages(path)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("LoadMessages returned %d messages; want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content[0].Text != "hello from Claude Code" {
		t.Fatalf("first message = %+v; want user text from string content", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content[0].Text != "history restored" {
		t.Fatalf("second message = %+v; want assistant text", msgs[1])
	}
}

func TestLoadMessages_ClaudeCodeTranscriptUsesLatestParentChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	writeJSONL(t, path,
		`{"uuid":"u1","parentUuid":null,"type":"user","message":{"role":"user","content":"root"},"timestamp":"2026-05-04T16:50:29.444Z"}`,
		`{"uuid":"a1","parentUuid":"u1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"root reply"}]},"timestamp":"2026-05-04T16:50:30.000Z"}`,
		`{"uuid":"u-branch","parentUuid":"a1","type":"user","message":{"role":"user","content":"stale branch"},"timestamp":"2026-05-04T16:50:31.000Z"}`,
		`{"uuid":"a-branch","parentUuid":"u-branch","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"stale reply"}]},"timestamp":"2026-05-04T16:50:32.000Z"}`,
		`{"uuid":"u2","parentUuid":"a1","type":"user","message":{"role":"user","content":"latest branch"},"timestamp":"2026-05-04T16:50:33.000Z"}`,
		`{"uuid":"a2","parentUuid":"u2","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"latest reply"}]},"timestamp":"2026-05-04T16:50:34.000Z"}`,
	)

	msgs, err := LoadMessages(path)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("LoadMessages returned %d messages; want latest 4-message chain", len(msgs))
	}
	got := []string{
		msgs[0].Content[0].Text,
		msgs[1].Content[0].Text,
		msgs[2].Content[0].Text,
		msgs[3].Content[0].Text,
	}
	want := []string{"root", "root reply", "latest branch", "latest reply"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("chain = %q; want %q", got, want)
	}
}

func TestLoadMessages_ClaudeCodeTranscriptBridgesSkippedThinking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	writeJSONL(t, path,
		`{"uuid":"u1","parentUuid":null,"type":"user","message":{"role":"user","content":"before thinking"},"timestamp":"2026-05-04T16:50:29.444Z"}`,
		`{"uuid":"think1","parentUuid":"u1","type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"sig"}]},"timestamp":"2026-05-04T16:50:30.000Z"}`,
		`{"uuid":"a1","parentUuid":"think1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"after thinking"}]},"timestamp":"2026-05-04T16:50:31.000Z"}`,
	)

	msgs, err := LoadMessages(path)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("LoadMessages returned %d messages; want chain bridged across skipped thinking entry", len(msgs))
	}
	if msgs[0].Content[0].Text != "before thinking" || msgs[1].Content[0].Text != "after thinking" {
		t.Fatalf("messages = %+v; want text before and after skipped thinking", msgs)
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

// --- tag persistence ---

func TestAppendTag_AndLoad(t *testing.T) {
	s := newTestSession(t)

	if err := s.AppendTag("refactor"); err != nil {
		t.Fatalf("AppendTag: %v", err)
	}

	tag, err := LoadTag(s.FilePath)
	if err != nil {
		t.Fatalf("LoadTag: %v", err)
	}
	if tag != "refactor" {
		t.Errorf("LoadTag = %q; want refactor", tag)
	}
}

func TestLoadTag_LastWins(t *testing.T) {
	s := newTestSession(t)
	_ = s.AppendTag("first")
	_ = s.AppendTag("second")

	tag, err := LoadTag(s.FilePath)
	if err != nil {
		t.Fatalf("LoadTag: %v", err)
	}
	if tag != "second" {
		t.Errorf("LoadTag = %q; want second (last entry wins)", tag)
	}
}

func TestLoadTag_EmptyClears(t *testing.T) {
	s := newTestSession(t)
	_ = s.AppendTag("removeme")
	_ = s.AppendTag("")

	tag, err := LoadTag(s.FilePath)
	if err != nil {
		t.Fatalf("LoadTag: %v", err)
	}
	if tag != "" {
		t.Errorf("LoadTag = %q; want empty (tag cleared)", tag)
	}
}

func TestLoadTag_NoTag(t *testing.T) {
	s := newTestSession(t)
	_ = s.AppendMessage(api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}})

	tag, err := LoadTag(s.FilePath)
	if err != nil {
		t.Fatalf("LoadTag: %v", err)
	}
	if tag != "" {
		t.Errorf("LoadTag = %q; want empty for untagged session", tag)
	}
}

// --- activity tracking ---

func TestActivity_FromTimestamps(t *testing.T) {
	s := newTestSession(t)

	_ = s.AppendMessage(api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "first"}}})
	_ = s.AppendMessage(api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: "reply"}}})

	act, err := LoadActivity(s.FilePath)
	if err != nil {
		t.Fatalf("LoadActivity: %v", err)
	}
	if act.FirstActivity.IsZero() {
		t.Error("FirstActivity should be set after appending a message")
	}
	if act.LastActivity.IsZero() {
		t.Error("LastActivity should be set after appending a message")
	}
	if act.LastActivity.Before(act.FirstActivity) {
		t.Errorf("LastActivity (%v) should be ≥ FirstActivity (%v)", act.LastActivity, act.FirstActivity)
	}
	if act.MessageCount != 2 {
		t.Errorf("MessageCount = %d; want 2", act.MessageCount)
	}
}

func TestActivity_EmptySession(t *testing.T) {
	s := newTestSession(t)

	act, err := LoadActivity(s.FilePath)
	if err != nil {
		t.Fatalf("LoadActivity: %v", err)
	}
	if !act.FirstActivity.IsZero() || !act.LastActivity.IsZero() {
		t.Errorf("expected zero times for empty session; got %+v", act)
	}
	if act.MessageCount != 0 {
		t.Errorf("MessageCount = %d; want 0", act.MessageCount)
	}
}

// --- conversation recovery ---

func TestFilterUnresolvedToolUses_DropsOrphanToolUse(t *testing.T) {
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "do X"}}},
		{Role: "assistant", Content: []api.ContentBlock{
			{Type: "text", Text: "I'll use the bash tool."},
			{Type: "tool_use", ID: "toolu_orphan", Name: "Bash", Input: map[string]any{"cmd": "ls"}},
		}},
		// stream errored before tool ran — no tool_result for toolu_orphan.
	}

	got := FilterUnresolvedToolUses(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages; got %d", len(got))
	}
	last := got[1]
	if len(last.Content) != 1 {
		t.Fatalf("expected orphan tool_use dropped; got %d blocks", len(last.Content))
	}
	if last.Content[0].Type != "text" {
		t.Errorf("expected only the text block to remain; got type %q", last.Content[0].Type)
	}
}

func TestFilterUnresolvedToolUses_KeepsResolvedToolUse(t *testing.T) {
	msgs := []api.Message{
		{Role: "assistant", Content: []api.ContentBlock{
			{Type: "tool_use", ID: "toolu_ok", Name: "Bash", Input: map[string]any{"cmd": "ls"}},
		}},
		{Role: "user", Content: []api.ContentBlock{
			{Type: "tool_result", ToolUseID: "toolu_ok", Text: "out"},
		}},
	}

	got := FilterUnresolvedToolUses(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages; got %d", len(got))
	}
	if got[0].Content[0].Type != "tool_use" {
		t.Errorf("resolved tool_use should be preserved")
	}
}

func TestFilterUnresolvedToolUses_DropsAssistantWithOnlyOrphanToolUse(t *testing.T) {
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: []api.ContentBlock{
			{Type: "tool_use", ID: "toolu_solo", Name: "Bash"},
		}},
	}

	got := FilterUnresolvedToolUses(msgs)
	if len(got) != 1 {
		t.Fatalf("assistant w/ only orphan tool_use should be dropped entirely; got %d msgs", len(got))
	}
	if got[0].Role != "user" {
		t.Errorf("expected the user msg to remain; got role %q", got[0].Role)
	}
}

// ensure json import is used
var _ = json.Marshal

func writeJSONL(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
