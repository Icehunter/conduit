// Package filewritetool implements the Write tool — writes a file to the
// local filesystem. Mirrors src/tools/FileWriteTool/FileWriteTool.ts.
//
// M2 scope: create new files and overwrite existing ones (UTF-8, LF line
// endings). Parent directory creation, LSP notification, and the
// file-history side-effects land in later milestones.
package filewritetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/tool"
)

// Tool implements the Write tool.
type Tool struct{}

// New returns a fresh FileWrite tool.
func New() *Tool { return &Tool{} }

// Name implements tool.Tool.
func (*Tool) Name() string { return "Write" }

// Description is the prompt text the model sees.
func (*Tool) Description() string {
	return "Write a file to the local filesystem. " +
		"This tool will overwrite the existing file if there is one at the provided path. " +
		"The file_path parameter must be an absolute path, not a relative path."
}

// InputSchema is the JSON Schema sent to the model.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to write (must be absolute, not relative)"
			},
			"content": {
				"type": "string",
				"description": "The content to write to the file"
			}
		},
		"required": ["file_path", "content"]
	}`)
}

// IsReadOnly: writing a file is never read-only.
func (*Tool) IsReadOnly(json.RawMessage) bool { return false }

// IsConcurrencySafe: concurrent writes to the same path are unsafe.
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Input is the typed view of the JSON input.
type Input struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// Execute writes the given content to the given file path.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.FilePath) == "" {
		return tool.ErrorResult("`file_path` is required and cannot be empty"), nil
	}
	if !filepath.IsAbs(in.FilePath) {
		return tool.ErrorResult(fmt.Sprintf("`file_path` must be absolute, got: %s", in.FilePath)), nil
	}

	select {
	case <-ctx.Done():
		return tool.ErrorResult("cancelled"), nil
	default:
	}

	// Create parent directories as needed.
	dir := filepath.Dir(in.FilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot create directory %s: %v", dir, err)), nil
	}

	// Atomic write via temp file + rename to avoid partial writes.
	tmpFile, err := os.CreateTemp(dir, ".write-*.tmp")
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot create temp file: %v", err)), nil
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op if rename succeeded

	if _, err := tmpFile.WriteString(in.Content); err != nil {
		_ = tmpFile.Close()
		return tool.ErrorResult(fmt.Sprintf("cannot write to temp file: %v", err)), nil
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return tool.ErrorResult(fmt.Sprintf("cannot sync temp file: %v", err)), nil
	}
	if err := tmpFile.Close(); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot close temp file: %v", err)), nil
	}
	if err := os.Rename(tmpPath, in.FilePath); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot rename temp file to %s: %v", in.FilePath, err)), nil
	}

	return tool.TextResult(fmt.Sprintf("File written successfully at: %s", in.FilePath)), nil
}
