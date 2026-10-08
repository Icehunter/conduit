package fileedittool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/readtracker"
	"github.com/icehunter/conduit/internal/tools/filereadtool"
)

func TestEditReadGuard(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, path string, rd *filereadtool.Tool)
		wantErr string // substring; empty means success
	}{
		{"never read", func(*testing.T, string, *filereadtool.Tool) {}, "use the Read tool"},
		{"read then edit", func(t *testing.T, p string, rd *filereadtool.Tool) { readViaTool(t, rd, p) }, ""},
		{"changed after read", func(t *testing.T, p string, rd *filereadtool.Tool) {
			readViaTool(t, rd, p)
			if err := os.WriteFile(p, []byte("hello changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "modified on disk"},
		{"touched but identical", func(t *testing.T, p string, rd *filereadtool.Tool) {
			readViaTool(t, rd, p)
			if err := os.WriteFile(p, []byte("hello world\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, ""},
		{"partial read counts", func(t *testing.T, p string, rd *filereadtool.Tool) {
			raw, _ := json.Marshal(map[string]any{"file_path": p, "limit": 1})
			if _, err := rd.Execute(context.Background(), raw); err != nil {
				t.Fatal(err)
			}
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			tr := readtracker.New()
			rd := filereadtool.New().WithTracker(tr)
			ed := New().WithTracker(tr)
			tt.prepare(t, path, rd)

			raw, _ := json.Marshal(map[string]any{"file_path": path, "old_string": "world", "new_string": "there"})
			res, err := ed.Execute(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			text := res.Content[0].Text
			if tt.wantErr == "" {
				if res.IsError {
					t.Fatalf("unexpected error: %s", text)
				}
				return
			}
			if !res.IsError || !strings.Contains(text, tt.wantErr) {
				t.Fatalf("want error containing %q, got IsError=%v %q", tt.wantErr, res.IsError, text)
			}
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "there") {
				t.Fatal("file was modified despite refusal")
			}
		})
	}
}

func TestEditAfterOwnEditNeedsNoReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("a b c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := readtracker.New()
	readViaTool(t, filereadtool.New().WithTracker(tr), path)
	ed := New().WithTracker(tr)
	for _, pair := range [][2]string{{"a", "x"}, {"b", "y"}} {
		raw, _ := json.Marshal(map[string]any{"file_path": path, "old_string": pair[0], "new_string": pair[1]})
		res, _ := ed.Execute(context.Background(), raw)
		if res.IsError {
			t.Fatalf("edit %v: %s", pair, res.Content[0].Text)
		}
	}
}

func TestEditCreateNeedsNoRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.txt")
	ed := New().WithTracker(readtracker.New())
	raw, _ := json.Marshal(map[string]any{"file_path": path, "old_string": "", "new_string": "x\n"})
	if res, _ := ed.Execute(context.Background(), raw); res.IsError {
		t.Fatalf("create refused: %s", res.Content[0].Text)
	}
	// The creator knows the content, so a follow-up edit is allowed.
	raw, _ = json.Marshal(map[string]any{"file_path": path, "old_string": "x", "new_string": "y"})
	if res, _ := ed.Execute(context.Background(), raw); res.IsError {
		t.Fatalf("edit after create refused: %s", res.Content[0].Text)
	}
}

func readViaTool(t *testing.T, rd *filereadtool.Tool, path string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"file_path": path})
	if res, err := rd.Execute(context.Background(), raw); err != nil || res.IsError {
		t.Fatalf("read failed: %v %v", err, res)
	}
}
