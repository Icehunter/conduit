package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/lsp"
)

// The built registry wires one read tracker through every file tool, so a
// file must be read through Read before Edit, Write, HashEdit or NotebookEdit
// may touch it, and the same instances serve sub-agent views.
func TestBuildRegistryEnforcesReadBeforeWrite(t *testing.T) {
	reg := BuildRegistry(nil, nil, lsp.NewManager(), nil, nil)
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	call := func(name string, in map[string]any) (string, bool) {
		t.Helper()
		tl, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		raw, _ := json.Marshal(in)
		res, err := tl.Execute(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].Text, res.IsError
	}
	edit := map[string]any{"file_path": path, "old_string": "hello", "new_string": "bye"}
	if msg, isErr := call("Edit", edit); !isErr || !strings.Contains(msg, "Read tool") {
		t.Fatalf("Edit before Read: IsError=%v %q", isErr, msg)
	}
	if msg, isErr := call("Write", map[string]any{"file_path": path, "content": "x"}); !isErr || !strings.Contains(msg, "Read tool") {
		t.Fatalf("Write before Read: IsError=%v %q", isErr, msg)
	}
	if msg, isErr := call("Read", map[string]any{"file_path": path}); isErr {
		t.Fatalf("Read: %q", msg)
	}
	if msg, isErr := call("Edit", edit); isErr {
		t.Fatalf("Edit after Read: %q", msg)
	}
}
