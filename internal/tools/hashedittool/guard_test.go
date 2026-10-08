package hashedittool_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/hashline"
	"github.com/icehunter/conduit/internal/readtracker"
	"github.com/icehunter/conduit/internal/tools/hashedittool"
)

// shownAnchors returns the anchor hashes of every line of content.
func shownAnchors(content string) []string {
	anchors := hashline.Compute([]byte(content))
	out := make([]string, 0, len(anchors))
	for _, a := range anchors {
		out = append(out, a.Hash)
	}
	return out
}

func TestHashEdit_readGuard(t *testing.T) {
	const content = "alpha\nbeta\ngamma\n"
	tests := []struct {
		name    string
		prepare func(tr *readtracker.Tracker, path string)
		wantErr string
	}{
		{"unread", func(*readtracker.Tracker, string) {}, "use the Read tool"},
		{"read without anchors", func(tr *readtracker.Tracker, p string) { tr.RecordRead(p, []byte(content), nil) }, "never shown"},
		{"read with anchors", func(tr *readtracker.Tracker, p string) { tr.RecordRead(p, []byte(content), shownAnchors(content)) }, ""},
		{"file changed after read", func(tr *readtracker.Tracker, p string) {
			tr.RecordRead(p, []byte("other\n"), shownAnchors(content))
		}, "modified on disk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			tr := readtracker.New()
			tt.prepare(tr, path)
			a := hashline.Compute([]byte(content))
			res := execTool(t, hashedittool.New().WithTracker(tr), map[string]any{
				"file_path": path,
				"edits":     []map[string]any{{"anchor": a[1].Hash, "new_lines": "BETA"}},
			})
			if tt.wantErr == "" {
				if res.IsError {
					t.Fatalf("unexpected error: %s", res.Content[0].Text)
				}
				return
			}
			if !res.IsError || !strings.Contains(res.Content[0].Text, tt.wantErr) {
				t.Fatalf("want %q, got %v %q", tt.wantErr, res.IsError, res.Content[0].Text)
			}
			if b, _ := os.ReadFile(path); string(b) != content {
				t.Fatalf("file modified despite refusal: %q", b)
			}
		})
	}
}

func TestHashEdit_rangeAndInsert(t *testing.T) {
	const content = "l1\nl2\nl3\nl4\nl5\n"
	tests := []struct {
		name    string
		edits   func(h []hashline.Anchor) []map[string]any
		want    string
		wantErr string
	}{
		{"range replace", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[1].Hash, "end_anchor": h[3].Hash, "new_lines": "X\nY"}}
		}, "l1\nX\nY\nl5\n", ""},
		{"range delete", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[1].Hash, "end_anchor": h[2].Hash, "delete": true}}
		}, "l1\nl4\nl5\n", ""},
		{"single-line range", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[2].Hash, "end_anchor": h[2].Hash, "new_lines": "Z"}}
		}, "l1\nl2\nZ\nl4\nl5\n", ""},
		{"insert before", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[2].Hash, "insert": "before", "new_lines": "NEW"}}
		}, "l1\nl2\nNEW\nl3\nl4\nl5\n", ""},
		{"insert after", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[2].Hash, "insert": "after", "new_lines": "NEW"}}
		}, "l1\nl2\nl3\nNEW\nl4\nl5\n", ""},
		{"insert after last line", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[4].Hash, "insert": "after", "new_lines": "END"}}
		}, "l1\nl2\nl3\nl4\nl5\nEND\n", ""},
		{"insert before first line", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[0].Hash, "insert": "before", "new_lines": "TOP"}}
		}, "TOP\nl1\nl2\nl3\nl4\nl5\n", ""},
		{"two inserts at one spot keep request order", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{
				{"anchor": h[1].Hash, "insert": "after", "new_lines": "A"},
				{"anchor": h[1].Hash, "insert": "after", "new_lines": "B"},
			}
		}, "l1\nl2\nA\nB\nl3\nl4\nl5\n", ""},
		{"replace plus insert before same line", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{
				{"anchor": h[2].Hash, "new_lines": "R"},
				{"anchor": h[2].Hash, "insert": "before", "new_lines": "I"},
			}
		}, "l1\nl2\nI\nR\nl4\nl5\n", ""},
		{"mixed ops in one call", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{
				{"anchor": h[0].Hash, "insert": "before", "new_lines": "TOP"},
				{"anchor": h[2].Hash, "end_anchor": h[3].Hash, "new_lines": "MID"},
				{"anchor": h[4].Hash, "delete": true},
			}
		}, "TOP\nl1\nl2\nMID\n", ""},
		{"overlapping ranges", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{
				{"anchor": h[1].Hash, "end_anchor": h[3].Hash, "new_lines": "X"},
				{"anchor": h[2].Hash, "new_lines": "Y"},
			}
		}, "", "overlap"},
		{"insert inside range", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{
				{"anchor": h[1].Hash, "end_anchor": h[3].Hash, "new_lines": "X"},
				{"anchor": h[2].Hash, "insert": "after", "new_lines": "Y"},
			}
		}, "", "overlap"},
		{"reversed range", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[3].Hash, "end_anchor": h[1].Hash, "new_lines": "X"}}
		}, "", "above anchor"},
		{"insert with delete", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[1].Hash, "insert": "after", "delete": true}}
		}, "", "cannot be combined"},
		{"bad insert value", func(h []hashline.Anchor) []map[string]any {
			return []map[string]any{{"anchor": h[1].Hash, "insert": "middle"}}
		}, "", "invalid insert"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			h := hashline.Compute([]byte(content))
			res := execTool(t, hashedittool.New(), map[string]any{"file_path": path, "edits": tt.edits(h)})
			got, _ := os.ReadFile(path)
			if tt.wantErr != "" {
				if !res.IsError || !strings.Contains(res.Content[0].Text, tt.wantErr) {
					t.Fatalf("want error %q, got IsError=%v %q", tt.wantErr, res.IsError, res.Content[0].Text)
				}
				if string(got) != content {
					t.Fatalf("file modified despite refusal: %q", got)
				}
				return
			}
			if res.IsError {
				t.Fatalf("unexpected error: %s", res.Content[0].Text)
			}
			if string(got) != tt.want {
				t.Fatalf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHashEdit_rangeRejectsUnshownInteriorLine(t *testing.T) {
	const content = "l1\nl2\nl3\nl4\n"
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	h := hashline.Compute([]byte(content))
	tr := readtracker.New()
	// Only the range endpoints were shown (for example two partial reads).
	tr.RecordRead(path, []byte(content), []string{h[0].Hash, h[3].Hash})
	res := execTool(t, hashedittool.New().WithTracker(tr), map[string]any{
		"file_path": path,
		"edits":     []map[string]any{{"anchor": h[0].Hash, "end_anchor": h[3].Hash, "delete": true}},
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "line 2") {
		t.Fatalf("want rejection naming line 2, got %v %q", res.IsError, res.Content[0].Text)
	}
}

func TestHashEdit_BOMPreserved(t *testing.T) {
	const body = "first\nsecond\n"
	path := filepath.Join(t.TempDir(), "bom.txt")
	if err := os.WriteFile(path, []byte(hashline.BOM+body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Anchors computed on the BOM-less text must address the BOM'd file.
	h := hashline.Compute([]byte(body))
	res := execTool(t, hashedittool.New(), map[string]any{
		"file_path": path,
		"edits":     []map[string]any{{"anchor": h[0].Hash, "new_lines": "FIRST"}},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content[0].Text)
	}
	got, _ := os.ReadFile(path)
	if want := hashline.BOM + "FIRST\nsecond\n"; string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestHashEdit_staleAfterOwnEditNeedsFreshAnchorsOnlyForTouchedLines(t *testing.T) {
	const content = "a\nb\nc\nd\ne\n"
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := readtracker.New()
	tr.RecordRead(path, []byte(content), shownAnchors(content))
	h := hashedittool.New().WithTracker(tr)
	a := hashline.Compute([]byte(content))
	if res := execTool(t, h, map[string]any{"file_path": path, "edits": []map[string]any{{"anchor": a[0].Hash, "new_lines": "A"}}}); res.IsError {
		t.Fatalf("first edit: %s", res.Content[0].Text)
	}
	// The model never saw anchors for the rewritten text, but the file was
	// written by this session, so a further edit of far-away lines still works.
	if res := execTool(t, h, map[string]any{"file_path": path, "edits": []map[string]any{{"anchor": a[4].Hash, "new_lines": "E"}}}); res.IsError {
		t.Fatalf("second edit: %s", res.Content[0].Text)
	}
}

func TestHashEdit_CRLFAndEmptyResult(t *testing.T) {
	tests := []struct {
		name string
		in   string
		edit func(a []hashline.Anchor) map[string]any
		want string
	}{
		{"CRLF preserved on replace", "a\r\nb\r\nc\r\n", func(a []hashline.Anchor) map[string]any {
			return map[string]any{"anchor": a[1].Hash, "new_lines": "B\nB2"}
		}, "a\r\nB\r\nB2\r\nc\r\n"},
		{"CRLF preserved on insert", "a\r\nb\r\n", func(a []hashline.Anchor) map[string]any {
			return map[string]any{"anchor": a[1].Hash, "insert": "after", "new_lines": "z"}
		}, "a\r\nb\r\nz\r\n"},
		{"LF file stays LF", "a\nb\n", func(a []hashline.Anchor) map[string]any {
			return map[string]any{"anchor": a[0].Hash, "new_lines": "A"}
		}, "A\nb\n"},
		{"full-range delete empties file", "a\nb\n", func(a []hashline.Anchor) map[string]any {
			return map[string]any{"anchor": a[0].Hash, "end_anchor": a[1].Hash, "delete": true}
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(tt.in), 0o644); err != nil {
				t.Fatal(err)
			}
			a := hashline.Compute([]byte(tt.in))
			res := execTool(t, hashedittool.New(), map[string]any{"file_path": path, "edits": []map[string]any{tt.edit(a)}})
			if res.IsError {
				t.Fatalf("unexpected error: %s", res.Content[0].Text)
			}
			if got, _ := os.ReadFile(path); string(got) != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
