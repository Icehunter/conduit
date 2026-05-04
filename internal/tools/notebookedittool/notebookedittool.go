// Package notebookedittool implements the NotebookEdit tool — reads and
// edits Jupyter notebook (.ipynb) cell source code.
//
// Mirrors src/tools/NotebookEditTool/NotebookEditTool.ts.
// Supports three edit modes: replace (default), insert (after cell_id),
// and delete.
package notebookedittool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/tool"
)

// Tool implements the NotebookEdit tool.
type Tool struct{}

// New returns a fresh NotebookEdit tool.
func New() *Tool { return &Tool{} }

func (*Tool) Name() string { return "NotebookEdit" }

func (*Tool) Description() string {
	return "Edits a Jupyter notebook cell. " +
		"Supports replace (default), insert (after cell_id), and delete modes. " +
		"The notebook_path must be absolute."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"notebook_path": {
				"type": "string",
				"description": "Absolute path to the .ipynb file"
			},
			"cell_id": {
				"type": "string",
				"description": "ID of the cell to edit. For insert, new cell goes after this cell (or at beginning if omitted)."
			},
			"new_source": {
				"type": "string",
				"description": "New source for the cell"
			},
			"cell_type": {
				"type": "string",
				"enum": ["code", "markdown"],
				"description": "Cell type. Defaults to existing type. Required for insert."
			},
			"edit_mode": {
				"type": "string",
				"enum": ["replace", "insert", "delete"],
				"description": "Edit operation. Defaults to replace."
			}
		},
		"required": ["notebook_path", "new_source"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Input is the typed view of the JSON input.
type Input struct {
	NotebookPath string `json:"notebook_path"`
	CellID       string `json:"cell_id"`
	NewSource    string `json:"new_source"`
	CellType     string `json:"cell_type"`
	EditMode     string `json:"edit_mode"`
}

// notebookFile is the minimal JSON structure of a .ipynb file.
type notebookFile struct {
	NBFormat      int            `json:"nbformat"`
	NBFormatMinor int            `json:"nbformat_minor"`
	Metadata      map[string]any `json:"metadata"`
	Cells         []notebookCell `json:"cells"`
}

// notebookCell represents one cell in a notebook.
type notebookCell struct {
	CellType       string         `json:"cell_type"`
	ID             string         `json:"id,omitempty"`
	Source         []string       `json:"source"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Outputs        []any          `json:"outputs,omitempty"`
	ExecutionCount *int           `json:"execution_count,omitempty"`
}

// Execute edits a Jupyter notebook cell.
func (*Tool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}

	if strings.TrimSpace(in.NotebookPath) == "" {
		return tool.ErrorResult("`notebook_path` is required"), nil
	}
	if !filepath.IsAbs(in.NotebookPath) {
		return tool.ErrorResult("`notebook_path` must be absolute"), nil
	}
	if !strings.HasSuffix(in.NotebookPath, ".ipynb") {
		return tool.ErrorResult("`notebook_path` must end in .ipynb"), nil
	}

	editMode := in.EditMode
	if editMode == "" {
		editMode = "replace"
	}
	switch editMode {
	case "replace", "insert", "delete":
	default:
		return tool.ErrorResult(fmt.Sprintf("invalid edit_mode %q; must be replace, insert, or delete", editMode)), nil
	}

	// Read existing notebook.
	data, err := os.ReadFile(in.NotebookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.ErrorResult(fmt.Sprintf("notebook not found: %s", in.NotebookPath)), nil
		}
		return tool.ErrorResult(fmt.Sprintf("cannot read notebook: %v", err)), nil
	}

	var nb notebookFile
	if err := json.Unmarshal(data, &nb); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot parse notebook JSON: %v", err)), nil
	}

	switch editMode {
	case "replace":
		return replaceCell(&nb, in)
	case "insert":
		return insertCell(&nb, in)
	case "delete":
		return deleteCell(&nb, in)
	}
	panic("unreachable")
}

func replaceCell(nb *notebookFile, in Input) (tool.Result, error) {
	if in.CellID == "" {
		return tool.ErrorResult("`cell_id` is required for replace mode"), nil
	}
	idx := findCell(nb, in.CellID)
	if idx < 0 {
		return tool.ErrorResult(fmt.Sprintf("cell %q not found", in.CellID)), nil
	}
	cell := &nb.Cells[idx]
	cell.Source = splitSource(in.NewSource)
	if in.CellType != "" {
		cell.CellType = in.CellType
	}
	if err := writeNotebook(in.NotebookPath, nb); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot write notebook: %v", err)), nil
	}
	return tool.TextResult(fmt.Sprintf("Replaced cell %q in %s.", in.CellID, in.NotebookPath)), nil
}

func insertCell(nb *notebookFile, in Input) (tool.Result, error) {
	cellType := in.CellType
	if cellType == "" {
		cellType = "code"
	}
	newCell := notebookCell{
		CellType: cellType,
		ID:       fmt.Sprintf("cell-%d", len(nb.Cells)),
		Source:   splitSource(in.NewSource),
		Metadata: map[string]any{},
	}
	if cellType == "code" {
		newCell.Outputs = []any{}
	}

	if in.CellID == "" {
		// Insert at beginning.
		nb.Cells = append([]notebookCell{newCell}, nb.Cells...)
	} else {
		idx := findCell(nb, in.CellID)
		if idx < 0 {
			return tool.ErrorResult(fmt.Sprintf("cell %q not found", in.CellID)), nil
		}
		nb.Cells = append(nb.Cells[:idx+1], append([]notebookCell{newCell}, nb.Cells[idx+1:]...)...)
	}

	if err := writeNotebook(in.NotebookPath, nb); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot write notebook: %v", err)), nil
	}
	return tool.TextResult(fmt.Sprintf("Inserted %s cell %q in %s.", cellType, newCell.ID, in.NotebookPath)), nil
}

func deleteCell(nb *notebookFile, in Input) (tool.Result, error) {
	if in.CellID == "" {
		return tool.ErrorResult("`cell_id` is required for delete mode"), nil
	}
	idx := findCell(nb, in.CellID)
	if idx < 0 {
		return tool.ErrorResult(fmt.Sprintf("cell %q not found", in.CellID)), nil
	}
	nb.Cells = append(nb.Cells[:idx], nb.Cells[idx+1:]...)
	if err := writeNotebook(in.NotebookPath, nb); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot write notebook: %v", err)), nil
	}
	return tool.TextResult(fmt.Sprintf("Deleted cell %q from %s.", in.CellID, in.NotebookPath)), nil
}

func findCell(nb *notebookFile, id string) int {
	for i, c := range nb.Cells {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// splitSource converts a source string into Jupyter's line-array format.
func splitSource(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := strings.Split(s, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		if i < len(lines)-1 {
			out[i] = line + "\n"
		} else {
			out[i] = line
		}
	}
	return out
}

func writeNotebook(path string, nb *notebookFile) error {
	data, err := json.MarshalIndent(nb, "", " ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
