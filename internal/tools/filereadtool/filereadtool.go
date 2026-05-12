// Package filereadtool implements the FileRead tool — reads a file from the
// local filesystem and returns its contents with line numbers. Mirrors
// src/tools/FileReadTool/FileReadTool.ts.
//
// M2 scope: text files only (UTF-8), line-number prefix, offset/limit
// pagination, binary-file detection, max read size (MaxReadBytes). PDF,
// image, and skills side-effects land in later milestones.
package filereadtool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/truncate"
)

// MaxReadBytes is the maximum file size we'll read in one call.
const MaxReadBytes = 2 * 1024 * 1024

// MaxLines is the maximum number of lines we'll return when no limit is
// specified (avoids blowing up context on huge files).
const MaxLines = 2000

// MaxLineLength truncates individual lines to avoid token blowup on minified
// files. Matches crush's MaxLineLength.
const MaxLineLength = 2000

// Tool implements the Read tool.
type Tool struct{}

// New returns a fresh FileRead tool.
func New() *Tool { return &Tool{} }

// Name implements tool.Tool.
func (*Tool) Name() string { return "Read" }

// Description is the prompt text the model sees.
func (*Tool) Description() string {
	return "Reads a file from the local filesystem. You can access any file directly by using this tool.\n" +
		"Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid.\n" +
		"Results are returned using cat -n format, with line numbers starting at 1.\n" +
		"By default reads up to 2000 lines starting from the beginning of the file. When you already know which part of the file you need, only read that part."
}

// InputSchema is the JSON Schema sent to the model.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to read"
			},
			"offset": {
				"type": "number",
				"description": "The line number to start reading from (1-indexed). Only provide if the file is too large to read at once."
			},
			"limit": {
				"type": "number",
				"description": "The number of lines to read. Only provide if the file is too large to read at once."
			}
		},
		"required": ["file_path"]
	}`)
}

// IsReadOnly: reading a file is always read-only.
func (*Tool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe: reads are safe to run concurrently.
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Input is the typed view of the JSON input.
type Input struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"` // 1-indexed line number to start reading from
	Limit    int    `json:"limit,omitempty"`  // number of lines to read
}

// Execute reads the file and returns its contents with line numbers.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.FilePath) == "" {
		return tool.ErrorResult("`file_path` is required and cannot be empty"), nil
	}

	select {
	case <-ctx.Done():
		return tool.ErrorResult("cancelled"), nil
	default:
	}

	f, err := os.Open(in.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.ErrorResult(fmt.Sprintf("file not found: %s", in.FilePath)), nil
		}
		return tool.ErrorResult(fmt.Sprintf("cannot open file: %v", err)), nil
	}
	defer f.Close()

	// Sniff first 8KB to detect binary content.
	sniff := make([]byte, 8192)
	n, _ := f.Read(sniff)
	sniff = sniff[:n]
	for _, b := range sniff {
		if b == 0x00 {
			return tool.ErrorResult("file appears to be binary (contains null bytes)"), nil
		}
	}
	if n > 0 && !utf8.Valid(sniff) {
		return tool.ErrorResult("file appears to be binary (invalid UTF-8)"), nil
	}

	// Seek back to start before scanning.
	if _, err := f.Seek(0, 0); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot seek file: %v", err)), nil
	}

	// Determine effective offset and limit.
	startLine := in.Offset // 1-indexed; 0 means "start at 1"
	if startLine <= 0 {
		startLine = 1
	}
	limit := in.Limit
	if limit <= 0 {
		limit = MaxLines
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	// Increase scanner buffer for very long lines.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	lineNum := 0
	linesEmitted := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if linesEmitted >= limit {
			break
		}
		line := scanner.Text()
		// Truncate very long lines to avoid token blowup on minified files.
		if len(line) > MaxLineLength {
			truncated := len(line) - MaxLineLength
			line = line[:MaxLineLength] + fmt.Sprintf("... [%d chars truncated]", truncated)
		}
		fmt.Fprintf(&sb, "%6d\t%s\n", lineNum, line)
		linesEmitted++
	}
	if err := scanner.Err(); err != nil {
		return tool.ErrorResult(fmt.Sprintf("error reading file: %v", err)), nil
	}

	text := strings.TrimRight(sb.String(), "\n")

	// Apply truncate-to-disk if output is still large (e.g., many long lines).
	// FileRead already limits lines, but byte count can still blow up on minified files.
	maxLines, maxBytes := truncate.Limits()
	tr, _ := truncate.Apply(text, truncate.Options{
		MaxLines:  maxLines,
		MaxBytes:  maxBytes,
		Direction: "head", // file reads: beginning is usually most relevant
		HasTask:   false,  // TODO: wire up Task tool availability
	})
	return tool.TextResult(tr.Content), nil
}
