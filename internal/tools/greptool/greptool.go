// Package greptool implements the Grep tool — searches file contents using
// ripgrep. Mirrors src/tools/GrepTool/GrepTool.ts.
//
// M2 scope: files_with_matches mode (default), content mode, count mode;
// -i case-insensitive; -n line numbers; -B/-A/-C context lines; glob
// filter; type filter; head_limit/offset pagination; VCS dir exclusion
// (.git/.svn/.hg). Permissions integration lands in M5.
//
// ripgrep (rg) must be on PATH.
package greptool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	rg "github.com/icehunter/conduit/internal/ripgrep"
	"github.com/icehunter/conduit/internal/tool"
)

// DefaultHeadLimit matches the real tool: 250 lines when unspecified.
const DefaultHeadLimit = 250

// MaxColumns prevents base64/minified content from cluttering output.
const MaxColumns = 500

// VCSDirectories are excluded from every search to avoid noise.
var VCSDirectories = []string{".git", ".svn", ".hg", ".bzr", ".jj", ".sl"}

// Tool implements the Grep tool.
type Tool struct{}

// New returns a fresh Grep tool.
func New() *Tool { return &Tool{} }

// Name implements tool.Tool.
func (*Tool) Name() string { return "Grep" }

// Description is the prompt text the model sees.
func (*Tool) Description() string {
	return "Fast content search tool that works with any codebase size. " +
		"Searches file contents using regular expressions (ripgrep). " +
		"Supports files_with_matches (default), content, and count output modes."
}

// InputSchema is the JSON Schema sent to the model.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "The regular expression pattern to search for in file contents"
			},
			"path": {
				"type": "string",
				"description": "File or directory to search in. Defaults to current working directory."
			},
			"glob": {
				"type": "string",
				"description": "Glob pattern to filter files (e.g. \"*.js\", \"*.{ts,tsx}\")"
			},
			"output_mode": {
				"type": "string",
				"enum": ["content", "files_with_matches", "count"],
				"description": "Output mode. Defaults to files_with_matches."
			},
			"-B": {"type": "number", "description": "Lines before match"},
			"-A": {"type": "number", "description": "Lines after match"},
			"-C": {"type": "number", "description": "Lines before and after match"},
			"context": {"type": "number", "description": "Alias for -C"},
			"-n": {"type": "boolean", "description": "Show line numbers (content mode only, default true)"},
			"-i": {"type": "boolean", "description": "Case insensitive search"},
			"type": {"type": "string", "description": "File type filter (e.g. go, py, js)"},
			"head_limit": {"type": "number", "description": "Limit output to first N entries (default 250, 0=unlimited)"},
			"offset": {"type": "number", "description": "Skip first N entries"},
			"multiline": {"type": "boolean", "description": "Enable multiline mode"}
		},
		"required": ["pattern"]
	}`)
}

// IsReadOnly: grep only reads the filesystem.
func (*Tool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe: grep is safe to run concurrently.
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// rawInput is the raw JSON view — we need *int and *bool to detect omitted fields.
type rawInput struct {
	Pattern    string  `json:"pattern"`
	Path       string  `json:"path,omitempty"`
	Glob       string  `json:"glob,omitempty"`
	OutputMode string  `json:"output_mode,omitempty"`
	Before     *int    `json:"-B,omitempty"`
	After      *int    `json:"-A,omitempty"`
	ContextC   *int    `json:"-C,omitempty"`
	Context    *int    `json:"context,omitempty"`
	LineNums   *bool   `json:"-n,omitempty"`
	Insensitive bool   `json:"-i,omitempty"`
	Type       string  `json:"type,omitempty"`
	HeadLimit  *int    `json:"head_limit,omitempty"`
	Offset     int     `json:"offset,omitempty"`
	Multiline  bool    `json:"multiline,omitempty"`
}

// Execute searches files matching the input and returns results.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in rawInput
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

	outputMode := in.OutputMode
	if outputMode == "" {
		outputMode = "files_with_matches"
	}

	rgBin := rg.Find()
	if rgBin == "" {
		return tool.ErrorResult("ripgrep (rg) not found on PATH — install with: brew install ripgrep"), nil
	}

	// Build rg args.
	args := []string{"--hidden"}

	// Exclude VCS directories.
	for _, dir := range VCSDirectories {
		args = append(args, "--glob", "!"+dir)
	}

	args = append(args, "--max-columns", fmt.Sprintf("%d", MaxColumns))

	if in.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}
	if in.Insensitive {
		args = append(args, "-i")
	}

	switch outputMode {
	case "files_with_matches":
		args = append(args, "-l")
	case "count":
		args = append(args, "-c")
	}

	// Line numbers: default on for content mode.
	showLineNums := true
	if in.LineNums != nil {
		showLineNums = *in.LineNums
	}
	if showLineNums && outputMode == "content" {
		args = append(args, "-n")
	}

	// Context lines (-C/-context takes precedence over -B/-A).
	if outputMode == "content" {
		if in.Context != nil {
			args = append(args, "-C", fmt.Sprintf("%d", *in.Context))
		} else if in.ContextC != nil {
			args = append(args, "-C", fmt.Sprintf("%d", *in.ContextC))
		} else {
			if in.Before != nil {
				args = append(args, "-B", fmt.Sprintf("%d", *in.Before))
			}
			if in.After != nil {
				args = append(args, "-A", fmt.Sprintf("%d", *in.After))
			}
		}
	}

	// Pattern: use -e if it starts with a dash.
	if strings.HasPrefix(in.Pattern, "-") {
		args = append(args, "-e", in.Pattern)
	} else {
		args = append(args, in.Pattern)
	}

	if in.Type != "" {
		args = append(args, "--type", in.Type)
	}

	if in.Glob != "" {
		for _, g := range splitGlob(in.Glob) {
			args = append(args, "--glob", g)
		}
	}

	args = append(args, baseDir)

	cmd := exec.CommandContext(ctx, rgBin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	runErr := cmd.Run()
	// rg exit code 1 = no matches (not an error for us); 2+ = real error.
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// No matches.
			runErr = nil
		} else if ctx.Err() != nil {
			return tool.ErrorResult("cancelled"), nil
		} else if runErr != nil {
			return tool.ErrorResult(fmt.Sprintf("rg error: %v\n%s", runErr, out.String())), nil
		}
	}

	lines := splitLines(out.String())

	// Apply head limit.
	headLimit := DefaultHeadLimit
	if in.HeadLimit != nil {
		if *in.HeadLimit == 0 {
			headLimit = 0 // unlimited
		} else {
			headLimit = *in.HeadLimit
		}
	}
	offset := in.Offset

	switch outputMode {
	case "content":
		// Relativize paths in content lines.
		for i, line := range lines {
			lines[i] = relativizeLine(line, baseDir)
		}
		limited, _ := applyLimit(lines, offset, headLimit)
		if len(limited) == 0 {
			return tool.TextResult("No matches found"), nil
		}
		return tool.TextResult(strings.Join(limited, "\n")), nil

	case "count":
		for i, line := range lines {
			lines[i] = relativizePath(line, baseDir)
		}
		limited, _ := applyLimit(lines, offset, headLimit)
		totalMatches := 0
		fileCount := 0
		for _, line := range limited {
			if ci := strings.LastIndex(line, ":"); ci >= 0 {
				var n int
				if _, err := fmt.Sscanf(line[ci+1:], "%d", &n); err == nil {
					totalMatches += n
					fileCount++
				}
			}
		}
		content := strings.Join(limited, "\n")
		if content == "" {
			content = "No matches found"
		}
		summary := fmt.Sprintf("\n\nFound %d total %s across %d %s.",
			totalMatches, plural(totalMatches, "occurrence"),
			fileCount, plural(fileCount, "file"))
		return tool.TextResult(content + summary), nil

	default: // files_with_matches
		// Sort by mtime desc.
		type entry struct {
			path  string
			mtime int64
		}
		entries := make([]entry, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			st, err := os.Stat(line)
			mtime := int64(0)
			if err == nil {
				mtime = st.ModTime().UnixNano()
			}
			entries = append(entries, entry{path: line, mtime: mtime})
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].mtime != entries[j].mtime {
				return entries[i].mtime > entries[j].mtime
			}
			return entries[i].path < entries[j].path
		})

		paths := make([]string, len(entries))
		for i, e := range entries {
			paths[i] = relativizePath(e.path, baseDir)
		}
		limited, _ := applyLimit(paths, offset, headLimit)
		if len(limited) == 0 {
			return tool.TextResult("No files found"), nil
		}
		result := fmt.Sprintf("Found %d %s\n%s", len(limited), plural(len(limited), "file"), strings.Join(limited, "\n"))
		return tool.TextResult(result), nil
	}
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func applyLimit(items []string, offset, limit int) ([]string, bool) {
	if offset > len(items) {
		return nil, false
	}
	items = items[offset:]
	if limit == 0 {
		return items, false
	}
	if len(items) > limit {
		return items[:limit], true
	}
	return items, false
}

// relativizeLine handles lines with format "path:rest" from content mode.
func relativizeLine(line, baseDir string) string {
	// Find the first colon after any absolute path prefix.
	ci := strings.Index(line, ":")
	if ci <= 0 {
		return line
	}
	possible := line[:ci]
	if filepath.IsAbs(possible) {
		rel, err := filepath.Rel(baseDir, possible)
		if err == nil {
			return rel + line[ci:]
		}
	}
	return line
}

// relativizePath converts absolute paths to relative under baseDir.
func relativizePath(p, baseDir string) string {
	p = strings.TrimSpace(p)
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(baseDir, p)
		if err == nil {
			return rel
		}
	}
	return p
}

// splitGlob splits a glob string on commas/spaces, preserving brace expansions.
func splitGlob(g string) []string {
	var result []string
	for _, part := range strings.Fields(g) {
		if strings.Contains(part, "{") && strings.Contains(part, "}") {
			result = append(result, part)
		} else {
			for _, sub := range strings.Split(part, ",") {
				if sub = strings.TrimSpace(sub); sub != "" {
					result = append(result, sub)
				}
			}
		}
	}
	return result
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

