package astgreptool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/icehunter/conduit/internal/readtracker"
)

func TestAstGrepRewriteKeepsTrackerCurrent(t *testing.T) {
	tests := []struct {
		name     string
		wasRead  bool
		staged   bool
		wantKept bool
	}{
		{"previously read file stays editable", true, false, true},
		{"unread file stays unread", false, false, false},
		{"staged rewrite of read file accepts flushed content", true, true, true},
		{"staged rewrite of unread file stays unread", false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "foo.go")
			orig, updated := []byte("old\n"), []byte("new\n")
			if err := os.WriteFile(file, orig, 0o644); err != nil {
				t.Fatal(err)
			}
			search := fmt.Sprintf("{\"file\":%q,\"range\":{\"start\":{\"line\":0,\"column\":0},\"end\":{\"line\":0,\"column\":3}},\"text\":\"old\"}\n", file)
			origLook, origRun := lookAstGrepFunc, runCmdFunc
			t.Cleanup(func() { lookAstGrepFunc, runCmdFunc = origLook, origRun })
			lookAstGrepFunc = func() (string, error) { return "/fake", nil }
			calls := 0
			runCmdFunc = func(context.Context, string, ...string) ([]byte, error) {
				calls++
				if calls == 1 {
					return []byte(search), nil
				}
				return nil, os.WriteFile(file, updated, 0o644)
			}
			tr := readtracker.New()
			if tt.wasRead {
				tr.RecordRead(file, orig, nil)
			}
			var tl *Tool
			var stager *fakeStager
			if tt.staged {
				stager = &fakeStager{}
				tl = NewWithStager(stager).WithTracker(tr)
			} else {
				tl = New().WithTracker(tr)
			}
			res, err := tl.Execute(context.Background(),
				marshalInput(t, map[string]any{"pattern": "old", "rewrite": "new", "dry_run": false}))
			if err != nil || res.IsError {
				t.Fatalf("rewrite failed: %v %+v", err, res)
			}
			if tt.staged {
				// A staged rewrite leaves the original on disk until flushed.
				if err := os.WriteFile(file, orig, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := tr.Check(file, updated) == nil; got != tt.wantKept {
				t.Fatalf("Check(updated) ok = %v, want %v", got, tt.wantKept)
			}
		})
	}
}
