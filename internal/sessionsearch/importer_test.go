package sessionsearch_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/icehunter/conduit/internal/sessionsearch"
)

// makeCodexJSONL writes a synthetic Codex rollout-*.jsonl file.
func makeCodexJSONL(t *testing.T, dir string, msgs []struct{ role, text string }) {
	t.Helper()
	name := "rollout-2026-01-02T10-00-00-aabbccdd-1234-5678-abcd-ef0123456789.jsonl"
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("makeCodexJSONL: create: %v", err)
	}
	defer f.Close()

	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type payload struct {
		Type    string         `json:"type"`
		Role    string         `json:"role"`
		Content []contentBlock `json:"content"`
	}
	type line struct {
		Type    string  `json:"type"`
		Payload payload `json:"payload"`
	}

	enc := json.NewEncoder(f)
	// Write a session_meta header (should be skipped).
	_ = enc.Encode(map[string]any{
		"type":    "session_meta",
		"payload": map[string]any{"id": "aabbccdd-1234-5678-abcd-ef0123456789"},
	})

	for _, m := range msgs {
		contentType := "input_text"
		if m.role == "assistant" {
			contentType = "output_text"
		}
		if err := enc.Encode(line{
			Type: "response_item",
			Payload: payload{
				Type:    "message",
				Role:    m.role,
				Content: []contentBlock{{Type: contentType, Text: m.text}},
			},
		}); err != nil {
			t.Fatalf("makeCodexJSONL: encode: %v", err)
		}
	}

	// Write a function_call line (should be skipped).
	_ = enc.Encode(map[string]any{
		"type":    "response_item",
		"payload": map[string]any{"type": "function_call", "name": "read_file", "arguments": "{}"},
	})
}

// makeCCFlatJSONL writes a synthetic CC JSONL file using the flat "message" format.
func makeCCFlatJSONL(t *testing.T, dir, sessionID string, msgs []struct{ role, text string }) string {
	t.Helper()
	path := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("makeCCFlatJSONL: create: %v", err)
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
	for _, m := range msgs {
		raw, err := json.Marshal(apiMsg{
			Role:    m.role,
			Content: []contentBlock{{Type: "text", Text: m.text}},
		})
		if err != nil {
			t.Fatalf("makeCCFlatJSONL: marshal: %v", err)
		}
		if err := enc.Encode(entry{Type: "message", Message: raw}); err != nil {
			t.Fatalf("makeCCFlatJSONL: encode: %v", err)
		}
	}
	return path
}

// makeCCBranchingJSONL writes a CC JSONL file using the branching uuid/parentUuid format.
func makeCCBranchingJSONL(t *testing.T, dir, sessionID string, msgs []struct{ role, text string }) string {
	t.Helper()
	path := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("makeCCBranchingJSONL: create: %v", err)
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
		UUID        string          `json:"uuid"`
		ParentUUID  string          `json:"parentUuid"`
		IsSidechain bool            `json:"isSidechain"`
		Type        string          `json:"type"`
		Message     json.RawMessage `json:"message"`
	}

	enc := json.NewEncoder(f)

	uuids := make([]string, len(msgs))
	for i := range msgs {
		uuids[i] = fmt.Sprintf("uuid-%04d-0000-0000-0000-000000000000", i+1)
	}

	for i, m := range msgs {
		raw, err := json.Marshal(apiMsg{
			Role:    m.role,
			Content: []contentBlock{{Type: "text", Text: m.text}},
		})
		if err != nil {
			t.Fatalf("makeCCBranchingJSONL: marshal: %v", err)
		}
		parent := ""
		if i > 0 {
			parent = uuids[i-1]
		}
		if err := enc.Encode(entry{
			UUID:        uuids[i],
			ParentUUID:  parent,
			IsSidechain: false,
			Type:        m.role,
			Message:     raw,
		}); err != nil {
			t.Fatalf("makeCCBranchingJSONL: encode: %v", err)
		}
	}

	// Write last-prompt metadata pointing to the final uuid.
	leafUUID := ""
	if len(uuids) > 0 {
		leafUUID = uuids[len(uuids)-1]
	}
	if err := enc.Encode(map[string]any{
		"type":      "last-prompt",
		"leafUuid":  leafUUID,
		"sessionId": sessionID,
	}); err != nil {
		t.Fatalf("makeCCBranchingJSONL: encode last-prompt: %v", err)
	}

	return path
}

func TestImportCodex(t *testing.T) {
	codexRoot := t.TempDir()
	// Create date-nested subdirectory.
	dayDir := filepath.Join(codexRoot, "2026", "01", "02")
	if err := os.MkdirAll(dayDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	makeCodexJSONL(t, dayDir, []struct{ role, text string }{
		{"user", "codex unique term xyzcodextoken hello"},
		{"assistant", "codex reply here"},
	})

	db := openMemDB(t)
	src := sessionsearch.ExternalSource{
		Name:    "codex",
		RootDir: codexRoot,
		Format:  "codex",
	}
	if err := db.ImportExternal(src); err != nil {
		t.Fatalf("ImportExternal codex: %v", err)
	}

	windows, err := db.Search("xyzcodextoken", "", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(windows) == 0 {
		t.Fatal("expected search hit for codex session, got none")
	}
	if !hasPrefixInSlug(windows, "codex:") {
		t.Errorf("expected project_slug to start with codex:, got %q", windows[0].ProjectSlug)
	}
}

func TestImportCodexMissingDir(t *testing.T) {
	db := openMemDB(t)
	src := sessionsearch.ExternalSource{
		Name:    "codex",
		RootDir: "/tmp/conduit-codex-does-not-exist-xyz",
		Format:  "codex",
	}
	if err := db.ImportExternal(src); err != nil {
		t.Fatalf("ImportExternal on missing dir should return nil, got: %v", err)
	}
}

func TestImportClaudeCodeFlat(t *testing.T) {
	ccRoot := t.TempDir()
	slugDir := filepath.Join(ccRoot, "-Volumes-TestProject")
	if err := os.MkdirAll(slugDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	makeCCFlatJSONL(t, slugDir, "sess-flat-001", []struct{ role, text string }{
		{"user", "cc flat unique term xyzccflattoken configure"},
		{"assistant", "flat cc reply content"},
	})

	db := openMemDB(t)
	src := sessionsearch.ExternalSource{
		Name:    "claude-code",
		RootDir: ccRoot,
		Format:  "claude-code",
	}
	if err := db.ImportExternal(src); err != nil {
		t.Fatalf("ImportExternal cc-flat: %v", err)
	}

	windows, err := db.Search("xyzccflattoken", "", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(windows) == 0 {
		t.Fatal("expected search hit for CC flat session, got none")
	}
	if !hasPrefixInSlug(windows, "cc:") {
		t.Errorf("expected project_slug to start with cc:, got %q", windows[0].ProjectSlug)
	}
}

func TestImportClaudeCodeBranching(t *testing.T) {
	ccRoot := t.TempDir()
	slugDir := filepath.Join(ccRoot, "-Volumes-TestProject")
	if err := os.MkdirAll(slugDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	makeCCBranchingJSONL(t, slugDir, "sess-branch-001", []struct{ role, text string }{
		{"user", "cc branching unique xyzccbranchtoken question"},
		{"assistant", "cc branching answer text"},
		{"user", "cc branching follow-up"},
	})

	db := openMemDB(t)
	src := sessionsearch.ExternalSource{
		Name:    "claude-code",
		RootDir: ccRoot,
		Format:  "claude-code",
	}
	if err := db.ImportExternal(src); err != nil {
		t.Fatalf("ImportExternal cc-branching: %v", err)
	}

	windows, err := db.Search("xyzccbranchtoken", "", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(windows) == 0 {
		t.Fatal("expected search hit for CC branching session, got none")
	}
}

func TestImportClaudeCodeMissingDir(t *testing.T) {
	db := openMemDB(t)
	src := sessionsearch.ExternalSource{
		Name:    "claude-code",
		RootDir: "/tmp/conduit-cc-does-not-exist-xyz",
		Format:  "claude-code",
	}
	if err := db.ImportExternal(src); err != nil {
		t.Fatalf("ImportExternal on missing cc dir should return nil, got: %v", err)
	}
}

func TestImportExternalUnknownFormat(t *testing.T) {
	db := openMemDB(t)
	src := sessionsearch.ExternalSource{
		Name:    "unknown",
		RootDir: t.TempDir(),
		Format:  "unknown-format",
	}
	if err := db.ImportExternal(src); err == nil {
		t.Fatal("expected error for unknown format, got nil")
	}
}

func TestDefaultSources(t *testing.T) {
	sources := sessionsearch.DefaultSources()
	if len(sources) == 0 {
		t.Fatal("DefaultSources returned empty slice")
	}
	names := make(map[string]bool)
	for _, s := range sources {
		names[s.Name] = true
		if s.RootDir == "" {
			t.Errorf("source %q has empty RootDir", s.Name)
		}
		if s.Format == "" {
			t.Errorf("source %q has empty Format", s.Name)
		}
	}
	if !names["claude-code"] {
		t.Error("expected claude-code in DefaultSources")
	}
	if !names["codex"] {
		t.Error("expected codex in DefaultSources")
	}
}

func TestImportCodexIncrementalSkip(t *testing.T) {
	codexRoot := t.TempDir()
	dayDir := filepath.Join(codexRoot, "2026", "01", "03")
	if err := os.MkdirAll(dayDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	makeCodexJSONL(t, dayDir, []struct{ role, text string }{
		{"user", "incremental codex test xyzincrcodex"},
	})

	db := openMemDB(t)
	src := sessionsearch.ExternalSource{
		Name:    "codex",
		RootDir: codexRoot,
		Format:  "codex",
	}

	// First import.
	if err := db.ImportExternal(src); err != nil {
		t.Fatalf("first import: %v", err)
	}
	w1, err := db.Search("xyzincrcodex", "", 5)
	if err != nil {
		t.Fatalf("search 1: %v", err)
	}
	if len(w1) == 0 {
		t.Fatal("expected result after first import")
	}

	// Second import without touching mtime — should skip.
	if err := db.ImportExternal(src); err != nil {
		t.Fatalf("second import: %v", err)
	}
	w2, err := db.Search("xyzincrcodex", "", 5)
	if err != nil {
		t.Fatalf("search 2: %v", err)
	}
	if len(w2) == 0 {
		t.Fatal("expected result to persist after second import")
	}
}

// hasPrefixInSlug returns true if any window's ProjectSlug starts with prefix.
func hasPrefixInSlug(windows []sessionsearch.MessageWindow, prefix string) bool {
	for _, w := range windows {
		if len(w.ProjectSlug) >= len(prefix) && w.ProjectSlug[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
