package notebookedittool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func input(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, _ := json.Marshal(v)
	return b
}

func makeNotebook(t *testing.T, cells []notebookCell) string {
	t.Helper()
	nb := notebookFile{
		NBFormat:      4,
		NBFormatMinor: 5,
		Metadata:      map[string]any{},
		Cells:         cells,
	}
	data, _ := json.MarshalIndent(nb, "", " ")
	path := filepath.Join(t.TempDir(), "test.ipynb")
	_ = os.WriteFile(path, data, 0644)
	return path
}

func readNotebook(t *testing.T, path string) *notebookFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var nb notebookFile
	if err := json.Unmarshal(data, &nb); err != nil {
		t.Fatal(err)
	}
	return &nb
}

func TestNotebookEdit_ReplaceCell(t *testing.T) {
	path := makeNotebook(t, []notebookCell{
		{CellType: "code", ID: "cell-0", Source: []string{"print('old')\n"}},
	})
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"notebook_path": path,
		"cell_id":       "cell-0",
		"new_source":    "print('new')",
		"edit_mode":     "replace",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	nb := readNotebook(t, path)
	if len(nb.Cells) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(nb.Cells))
	}
	source := strings.Join(nb.Cells[0].Source, "")
	if !strings.Contains(source, "new") {
		t.Errorf("source not updated; got: %q", source)
	}
}

func TestNotebookEdit_InsertCell(t *testing.T) {
	path := makeNotebook(t, []notebookCell{
		{CellType: "code", ID: "cell-0", Source: []string{"x = 1\n"}},
	})
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"notebook_path": path,
		"cell_id":       "cell-0",
		"new_source":    "y = 2",
		"cell_type":     "code",
		"edit_mode":     "insert",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	nb := readNotebook(t, path)
	if len(nb.Cells) != 2 {
		t.Fatalf("expected 2 cells, got %d", len(nb.Cells))
	}
	if nb.Cells[0].ID != "cell-0" {
		t.Errorf("first cell should be cell-0, got %q", nb.Cells[0].ID)
	}
}

func TestNotebookEdit_InsertAtBeginning(t *testing.T) {
	path := makeNotebook(t, []notebookCell{
		{CellType: "code", ID: "cell-0", Source: []string{"x = 1\n"}},
	})
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"notebook_path": path,
		"new_source":    "# header",
		"cell_type":     "markdown",
		"edit_mode":     "insert",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	nb := readNotebook(t, path)
	if len(nb.Cells) != 2 {
		t.Fatalf("expected 2 cells, got %d", len(nb.Cells))
	}
	if nb.Cells[0].CellType != "markdown" {
		t.Errorf("first cell should be markdown, got %q", nb.Cells[0].CellType)
	}
}

func TestNotebookEdit_DeleteCell(t *testing.T) {
	path := makeNotebook(t, []notebookCell{
		{CellType: "code", ID: "cell-0", Source: []string{"x = 1\n"}},
		{CellType: "code", ID: "cell-1", Source: []string{"y = 2\n"}},
	})
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"notebook_path": path,
		"cell_id":       "cell-0",
		"new_source":    "",
		"edit_mode":     "delete",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	nb := readNotebook(t, path)
	if len(nb.Cells) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(nb.Cells))
	}
	if nb.Cells[0].ID != "cell-1" {
		t.Errorf("remaining cell should be cell-1, got %q", nb.Cells[0].ID)
	}
}

func TestNotebookEdit_NotFound(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"notebook_path": "/nonexistent/path.ipynb",
		"new_source":    "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("nonexistent path should IsError=true")
	}
}

func TestNotebookEdit_RelativePath(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"notebook_path": "relative/path.ipynb",
		"new_source":    "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("relative path should IsError=true")
	}
}

func TestNotebookEdit_CellNotFound(t *testing.T) {
	path := makeNotebook(t, []notebookCell{
		{CellType: "code", ID: "cell-0", Source: []string{"x = 1\n"}},
	})
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"notebook_path": path,
		"cell_id":       "nonexistent",
		"new_source":    "x",
		"edit_mode":     "replace",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("missing cell_id should IsError=true")
	}
}

func TestNotebookEdit_InvalidJSON(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), json.RawMessage(`{bad`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should IsError=true")
	}
}

func TestNotebookEdit_StaticMetadata(t *testing.T) {
	tt := New()
	if tt.Name() != "NotebookEdit" {
		t.Errorf("Name = %q", tt.Name())
	}
	if tt.IsReadOnly(nil) {
		t.Error("IsReadOnly should be false")
	}
	var schema map[string]any
	if err := json.Unmarshal(tt.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
}
