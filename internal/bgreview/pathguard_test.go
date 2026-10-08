package bgreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/icehunter/conduit/internal/tool"
)

type recordingTool struct{ calls int }

func (*recordingTool) Name() string                 { return "Write" }
func (*recordingTool) Description() string          { return "w" }
func (*recordingTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (r *recordingTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	r.calls++
	return tool.TextResult("ok"), nil
}
func (*recordingTool) IsReadOnly(json.RawMessage) bool        { return false }
func (*recordingTool) IsConcurrencySafe(json.RawMessage) bool { return false }

func TestRestrictWrites(t *testing.T) {
	root := t.TempDir()
	mem := filepath.Join(root, "memory")
	outside := filepath.Join(root, "elsewhere")
	for _, d := range []string{mem, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(mem, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tests := []struct {
		name   string
		input  map[string]string
		wantOK bool
	}{
		{"file in the directory", map[string]string{"file_path": filepath.Join(mem, "user.md")}, true},
		{"new file in a new subdirectory", map[string]string{"file_path": filepath.Join(mem, "sub", "deep", "a.md")}, true},
		{"the path form", map[string]string{"path": filepath.Join(mem, "a.md")}, true},
		{"outside the directory", map[string]string{"file_path": filepath.Join(outside, "x")}, false},
		{"sibling with the same prefix", map[string]string{"file_path": mem + "-evil/x"}, false},
		{"dot-dot escape", map[string]string{"file_path": filepath.Join(mem, "..", "elsewhere", "x")}, false},
		{"symlink escape", map[string]string{"file_path": filepath.Join(mem, "escape", "x")}, false},
		{"relative path", map[string]string{"file_path": "memory/x.md"}, false},
		{"no path", map[string]string{}, false},
		{"the directory itself", map[string]string{"file_path": mem}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingTool{}
			g := RestrictWrites(rec, mem)
			raw, _ := json.Marshal(tt.input)
			res, err := g.Execute(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := rec.calls == 1; got != tt.wantOK {
				t.Errorf("underlying tool ran = %v, want %v (result error %v)", got, tt.wantOK, res.IsError)
			}
			if !tt.wantOK && !res.IsError {
				t.Error("a denied write should come back as an error result")
			}
		})
	}
	if g := RestrictWrites(&recordingTool{}, mem); g.Name() != "Write" {
		t.Errorf("wrapper changed the name to %q", g.Name())
	}
}
