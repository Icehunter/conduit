// Package globtool implements the Glob tool — finds files matching a glob
// pattern. Mirrors src/tools/GlobTool/GlobTool.ts.
//
// M2 scope: standard doublestar glob via github.com/bmatcuk/doublestar/v4,
// 100-result cap, relative-path output, optional base directory. Permissions
// integration and plugin-cache exclusions land in M5.
package globtool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/icehunter/conduit/internal/tool"
)

// MaxResults caps the number of returned paths to keep tool_result blocks
// under context budgets. Matches the real tool's limit of 100.
const MaxResults = 100

// Tool implements the Glob tool.
type Tool struct{}

// New returns a fresh Glob tool.
func New() *Tool { return &Tool{} }

// Name implements tool.Tool.
func (*Tool) Name() string { return "Glob" }

// Description is the prompt text the model sees.
func (*Tool) Description() string {
	return "Fast file pattern matching tool that works with any codebase size. " +
		"Supports glob patterns like \"**/*.js\", \"src/**/*.{ts,tsx}\", \"*.go\". " +
		"Returns up to 100 files sorted by modification time (most recent first). " +
		"Use the path parameter to search within a specific directory."
}

// InputSchema is the JSON Schema sent to the model.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "The glob pattern to match files against"
			},
			"path": {
				"type": "string",
				"description": "The directory to search in. If not specified, the current working directory will be used."
			}
		},
		"required": ["pattern"]
	}`)
}

// IsReadOnly: glob only reads the filesystem.
func (*Tool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe: glob is safe to run concurrently.
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Input is the typed view of the JSON input.
type Input struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

// Execute finds files matching the pattern and returns their paths.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return tool.ErrorResult("`pattern` is required and cannot be empty"), nil
	}

	baseDir := in.Path
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("cannot get working directory: %v", err)), nil
		}
	}

	// Validate that base directory exists.
	info, err := os.Stat(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.ErrorResult(fmt.Sprintf("directory does not exist: %s", baseDir)), nil
		}
		return tool.ErrorResult(fmt.Sprintf("cannot stat directory: %v", err)), nil
	}
	if !info.IsDir() {
		return tool.ErrorResult(fmt.Sprintf("path is not a directory: %s", baseDir)), nil
	}

	select {
	case <-ctx.Done():
		return tool.ErrorResult("cancelled"), nil
	default:
	}

	// Walk the FS and collect matches.
	fsys := os.DirFS(baseDir)
	// doublestar.Glob doesn't support `**` at root without a leading **.
	matches, err := doublestar.Glob(fsys, in.Pattern)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid glob pattern: %v", err)), nil
	}

	// Sort by modification time (most recent first), then name as tiebreaker.
	type entry struct {
		rel   string
		mtime int64
	}
	entries := make([]entry, 0, len(matches))
	for _, rel := range matches {
		abs := filepath.Join(baseDir, rel)
		st, err := os.Lstat(abs)
		if err != nil {
			continue
		}
		if st.IsDir() {
			continue // only files
		}
		entries = append(entries, entry{rel: rel, mtime: st.ModTime().UnixNano()})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].mtime != entries[j].mtime {
			return entries[i].mtime > entries[j].mtime
		}
		return entries[i].rel < entries[j].rel
	})

	truncated := false
	if len(entries) > MaxResults {
		entries = entries[:MaxResults]
		truncated = true
	}

	if len(entries) == 0 {
		return tool.TextResult("No files found"), nil
	}

	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(e.rel)
		sb.WriteByte('\n')
	}
	if truncated {
		sb.WriteString("(Results are truncated. Consider using a more specific path or pattern.)\n")
	}

	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}
