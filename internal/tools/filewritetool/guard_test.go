package filewritetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/readtracker"
)

func TestWriteReadGuard(t *testing.T) {
	tests := []struct {
		name     string
		existing bool
		prepare  func(tr *readtracker.Tracker, path string)
		wantErr  string
	}{
		{"new file needs no read", false, func(*readtracker.Tracker, string) {}, ""},
		{"existing unread", true, func(*readtracker.Tracker, string) {}, "use the Read tool"},
		{"existing read", true, func(tr *readtracker.Tracker, p string) { tr.RecordRead(p, []byte("old"), nil) }, ""},
		{"existing changed since read", true, func(tr *readtracker.Tracker, p string) { tr.RecordRead(p, []byte("older"), nil) }, "modified on disk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if tt.existing {
				if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			tr := readtracker.New()
			tt.prepare(tr, path)
			raw, _ := json.Marshal(map[string]any{"file_path": path, "content": "new"})
			res, err := New().WithTracker(tr).Execute(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			if tt.wantErr == "" {
				if res.IsError || string(got) != "new" {
					t.Fatalf("want success; IsError=%v content=%q", res.IsError, got)
				}
				return
			}
			if !res.IsError || !strings.Contains(res.Content[0].Text, tt.wantErr) {
				t.Fatalf("want error %q, got IsError=%v %q", tt.wantErr, res.IsError, res.Content[0].Text)
			}
			if string(got) != "old" {
				t.Fatalf("file clobbered: %q", got)
			}
		})
	}
}

func TestWriteThenWriteAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	w := New().WithTracker(readtracker.New())
	for _, c := range []string{"one", "two"} {
		raw, _ := json.Marshal(map[string]any{"file_path": path, "content": c})
		if res, _ := w.Execute(context.Background(), raw); res.IsError {
			t.Fatalf("write %q: %s", c, res.Content[0].Text)
		}
	}
}
