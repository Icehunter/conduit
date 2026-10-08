package notebookedittool

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/readtracker"
)

func TestNotebookReadGuard(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(tr *readtracker.Tracker, path string)
		wantErr string
	}{
		{"unread", func(*readtracker.Tracker, string) {}, "use the Read tool"},
		{"read", func(tr *readtracker.Tracker, p string) {
			b, _ := os.ReadFile(p)
			tr.RecordRead(p, b, nil)
		}, ""},
		{"changed", func(tr *readtracker.Tracker, p string) { tr.RecordRead(p, []byte("stale"), nil) }, "modified on disk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := makeNotebook(t, []notebookCell{{CellType: "code", ID: "c1", Source: []string{"x"}}})
			tr := readtracker.New()
			tt.prepare(tr, path)
			res, err := New().WithTracker(tr).Execute(context.Background(),
				input(t, map[string]any{"notebook_path": path, "cell_id": "c1", "new_source": "y = 2"}))
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantErr == "" {
				if res.IsError {
					t.Fatalf("unexpected error: %s", res.Content[0].Text)
				}
				return
			}
			if !res.IsError || !strings.Contains(res.Content[0].Text, tt.wantErr) {
				t.Fatalf("want %q, got %v %q", tt.wantErr, res.IsError, res.Content[0].Text)
			}
			if got := readNotebook(t, path).Cells[0].Source; len(got) != 1 || got[0] != "x" {
				t.Fatalf("notebook modified despite refusal: %v", got)
			}
		})
	}
}

func TestNotebookSecondEditNeedsNoReread(t *testing.T) {
	path := makeNotebook(t, []notebookCell{{CellType: "code", ID: "c1", Source: []string{"x"}}})
	tr := readtracker.New()
	b, _ := os.ReadFile(path)
	tr.RecordRead(path, b, nil)
	nt := New().WithTracker(tr)
	for _, src := range []string{"a", "b"} {
		res, _ := nt.Execute(context.Background(), input(t, map[string]any{"notebook_path": path, "cell_id": "c1", "new_source": src}))
		if res.IsError {
			t.Fatalf("edit %q: %s", src, res.Content[0].Text)
		}
	}
}
