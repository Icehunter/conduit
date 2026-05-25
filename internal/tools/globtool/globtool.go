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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/icehunter/conduit/internal/tool"
)

// MaxResults caps the number of returned paths to keep tool_result blocks
// under context budgets. Matches the real tool's limit of 100.
const MaxResults = 100

// maxScan caps the number of raw matches the walker collects before sorting
// and truncating to MaxResults. Pathological patterns like **/* over giant
// trees (node_modules, mounted /Volumes, symlink loops) can produce millions
// of entries — without a hard cap the walk runs for minutes. The cap is set
// generously above MaxResults so mtime-sort still has plenty of candidates.
const maxScan = MaxResults * 50

// defaultTimeout bounds a single Glob.Execute call. The walk is otherwise
// uncancellable mid-segment; without this a sub-agent that issued a wide
// pattern could pin its parent (e.g. a council member round) for many
// minutes regardless of the caller's own deadline.
const defaultTimeout = 25 * time.Second

// errScanCapReached is the sentinel returned from the GlobWalk callback when
// maxScan entries have been collected. It stops the walk without bubbling up
// as an error.
var errScanCapReached = errors.New("glob: scan cap reached")

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

	// Derive a bounded context so a pathological pattern can't pin the caller
	// (e.g. a council member sub-agent) indefinitely. We still respect the
	// caller's earlier deadline if any.
	walkCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	// Walk the FS with a cancellable callback. doublestar.Glob is synchronous
	// and uncancellable; GlobWalk invokes the callback per match so we can
	// abort on ctx cancellation or once we've collected enough candidates.
	type entry struct {
		rel   string
		mtime int64
	}
	entries := make([]entry, 0, MaxResults)
	timedOut := false
	scanCapped := false

	fsys := os.DirFS(baseDir)
	walkErr := doublestar.GlobWalk(fsys, in.Pattern, func(rel string, d fs.DirEntry) error {
		if err := walkCtx.Err(); err != nil {
			return err
		}
		if d != nil && d.IsDir() {
			return nil
		}
		abs := filepath.Join(baseDir, rel)
		st, err := os.Lstat(abs)
		if err != nil {
			return nil
		}
		if st.IsDir() {
			return nil
		}
		entries = append(entries, entry{rel: rel, mtime: st.ModTime().UnixNano()})
		if len(entries) >= maxScan {
			scanCapped = true
			return errScanCapReached
		}
		return nil
	})
	if walkErr != nil {
		switch {
		case errors.Is(walkErr, errScanCapReached):
			// expected sentinel — proceed with what we have
		case errors.Is(walkErr, context.DeadlineExceeded):
			timedOut = true
		case errors.Is(walkErr, context.Canceled):
			return tool.ErrorResult("cancelled"), nil
		default:
			return tool.ErrorResult(fmt.Sprintf("invalid glob pattern: %v", walkErr)), nil
		}
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
		if timedOut {
			return tool.ErrorResult(fmt.Sprintf("glob timed out after %s — pattern too broad; narrow the path or use a more specific pattern", defaultTimeout)), nil
		}
		return tool.TextResult("No files found"), nil
	}

	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(e.rel)
		sb.WriteByte('\n')
	}
	switch {
	case timedOut:
		sb.WriteString(fmt.Sprintf("(Results are partial — glob timed out after %s. Narrow the path or use a more specific pattern.)\n", defaultTimeout))
	case scanCapped:
		sb.WriteString("(Results are truncated — scan cap reached. Narrow the path or use a more specific pattern.)\n")
	case truncated:
		sb.WriteString("(Results are truncated. Consider using a more specific path or pattern.)\n")
	}

	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}
