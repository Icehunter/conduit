package filereadtool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/hashline"
	"github.com/icehunter/conduit/internal/readtracker"
)

func TestReadRecordsInTracker(t *testing.T) {
	const content = "a\nb\nc\nd\n"
	anchors := hashline.Compute([]byte(content))
	tests := []struct {
		name       string
		in         map[string]any
		wantShown  []int // line indexes whose anchors must be recorded
		wantHidden []int
	}{
		{"plain read records no anchors", map[string]any{}, nil, []int{0, 1, 2, 3}},
		{"anchored full read", map[string]any{"anchors": true}, []int{0, 1, 2, 3}, nil},
		{"anchored partial read", map[string]any{"anchors": true, "offset": 2, "limit": 2}, []int{1, 2}, []int{0, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			tr := readtracker.New()
			tt.in["file_path"] = path
			raw, _ := json.Marshal(tt.in)
			if res, err := New().WithTracker(tr).Execute(context.Background(), raw); err != nil || res.IsError {
				t.Fatalf("read: %v %+v", err, res)
			}
			if err := tr.Check(path, []byte(content)); err != nil {
				t.Fatalf("read file not accepted: %v", err)
			}
			for _, i := range tt.wantShown {
				if !tr.Shown(path, anchors[i].Hash) {
					t.Errorf("line %d anchor not recorded", i+1)
				}
			}
			for _, i := range tt.wantHidden {
				if tr.Shown(path, anchors[i].Hash) {
					t.Errorf("line %d anchor recorded but not shown", i+1)
				}
			}
		})
	}
}

func TestReadHidesBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bom.txt")
	if err := os.WriteFile(path, []byte(hashline.BOM+"first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"file_path": path})
	res, _ := New().Execute(context.Background(), raw)
	if strings.Contains(res.Content[0].Text, hashline.BOM) {
		t.Fatalf("BOM leaked into output: %q", res.Content[0].Text)
	}
}

func TestReadAcceptsUTF8SplitAtSniffBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	body := strings.Repeat("a", 8191) + "é" + "\nhello\n" // é straddles byte 8192
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"file_path": path})
	res, _ := New().Execute(context.Background(), raw)
	if res.IsError {
		t.Fatalf("valid UTF-8 rejected: %s", res.Content[0].Text)
	}
}

func TestReadStillRejectsInvalidUTF8ButRecordsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "latin1.txt")
	data := []byte("caf\xe9\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	tr := readtracker.New()
	raw, _ := json.Marshal(map[string]any{"file_path": path})
	res, _ := New().WithTracker(tr).Execute(context.Background(), raw)
	if !res.IsError {
		t.Fatal("invalid UTF-8 must still be refused")
	}
	if err := tr.Check(path, data); err != nil {
		t.Fatalf("Write-overwrite of a Latin-1 file should not be blocked: %v", err)
	}
}

func TestTrimIncompleteRune(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ascii", "abc", "abc"},
		{"complete rune", "abé", "abé"},
		{"cut 2-byte", "ab\xc3", "ab"},
		{"cut 3-byte after one", "ab\xe2\x82", "ab"},
		{"cut 4-byte after two", "ab\xf0\x9f\x98", "ab"},
		{"invalid kept", "ab\xff", "ab\xff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(trimIncompleteRune([]byte(tt.in))); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
