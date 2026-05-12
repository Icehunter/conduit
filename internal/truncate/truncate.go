// Package truncate saves large tool outputs to disk and returns a preview with
// a hint to use Grep/Read with offset/limit. This prevents context blowup from
// massive bash output, grep results, or file reads while preserving full output
// for inspection.
//
// Inspired by opencode's src/tool/truncate.ts.
package truncate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/sessionstats"
	"github.com/icehunter/conduit/internal/settings"
)

// Default limits matching opencode. Can be overridden via config.
const (
	DefaultMaxLines = 2000
	DefaultMaxBytes = 50 * 1024 // 50KB
	Retention       = 7 * 24 * time.Hour
)

// Dir returns the truncation storage directory.
func Dir() string {
	return filepath.Join(settings.ConduitDir(), "truncated")
}

// Limits returns the configured truncation limits, falling back to defaults.
func Limits() (maxLines, maxBytes int) {
	maxLines = DefaultMaxLines
	maxBytes = DefaultMaxBytes

	cfg, err := settings.LoadConduitConfig()
	if err != nil || cfg.ToolOutput == nil {
		return
	}
	if cfg.ToolOutput.MaxLines > 0 {
		maxLines = cfg.ToolOutput.MaxLines
	}
	if cfg.ToolOutput.MaxBytes > 0 {
		maxBytes = cfg.ToolOutput.MaxBytes
	}
	return
}

// Result describes the outcome of a truncation attempt.
type Result struct {
	Content    string // The (possibly truncated) content to return
	Truncated  bool   // Whether truncation occurred
	OutputPath string // Path to the full output file (only set if Truncated)
}

// Options configures truncation behavior.
type Options struct {
	MaxLines  int
	MaxBytes  int
	Direction string // "head" (default) or "tail"
	HasTask   bool   // Whether the agent has access to the Task tool
}

// Apply truncates text if it exceeds limits, saving the full output to disk.
// Returns Result with either the original text or a preview with hint.
func Apply(text string, opts Options) (Result, error) {
	if opts.MaxLines <= 0 {
		opts.MaxLines = DefaultMaxLines
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.Direction == "" {
		opts.Direction = "head"
	}

	lines := strings.Split(text, "\n")
	totalBytes := len(text)

	// Check if truncation is needed
	if len(lines) <= opts.MaxLines && totalBytes <= opts.MaxBytes {
		return Result{Content: text, Truncated: false}, nil
	}

	// Build preview within limits
	var preview []string
	bytes := 0
	hitBytes := false

	if opts.Direction == "head" {
		for i := 0; i < len(lines) && i < opts.MaxLines; i++ {
			lineSize := len(lines[i])
			if i > 0 {
				lineSize++ // account for newline
			}
			if bytes+lineSize > opts.MaxBytes {
				hitBytes = true
				break
			}
			preview = append(preview, lines[i])
			bytes += lineSize
		}
	} else {
		// tail direction
		for i := len(lines) - 1; i >= 0 && len(preview) < opts.MaxLines; i-- {
			lineSize := len(lines[i])
			if len(preview) > 0 {
				lineSize++ // account for newline
			}
			if bytes+lineSize > opts.MaxBytes {
				hitBytes = true
				break
			}
			preview = append([]string{lines[i]}, preview...)
			bytes += lineSize
		}
	}

	// Calculate what was removed
	var removed int
	var unit string
	if hitBytes {
		removed = totalBytes - bytes
		unit = "bytes"
	} else {
		removed = len(lines) - len(preview)
		unit = "lines"
	}

	// Save full output to disk
	outputPath, err := write(text)
	if err != nil {
		// Fall back to inline truncation without file
		previewText := strings.Join(preview, "\n")
		return Result{
			Content:   fmt.Sprintf("%s\n\n...%d %s truncated (failed to save: %v)...", previewText, removed, unit, err),
			Truncated: true,
		}, nil
	}

	// Build hint
	var hint string
	if opts.HasTask {
		hint = fmt.Sprintf(
			"The tool call succeeded but the output was truncated. Full output saved to: %s\n"+
				"Use the Task tool to have explore agent process this file with Grep and Read (with offset/limit). "+
				"Do NOT read the full file yourself - delegate to save context.",
			outputPath,
		)
	} else {
		hint = fmt.Sprintf(
			"The tool call succeeded but the output was truncated. Full output saved to: %s\n"+
				"Use Grep to search the full content or Read with offset/limit to view specific sections.",
			outputPath,
		)
	}

	previewText := strings.Join(preview, "\n")
	var content string
	if opts.Direction == "head" {
		content = fmt.Sprintf("%s\n\n...%d %s truncated...\n\n%s", previewText, removed, unit, hint)
	} else {
		content = fmt.Sprintf("...%d %s truncated...\n\n%s\n\n%s", removed, unit, hint, previewText)
	}

	// Record truncation metrics
	bytesSaved := totalBytes - bytes
	sessionstats.SessionMetrics.RecordTruncate(bytesSaved)

	return Result{
		Content:    content,
		Truncated:  true,
		OutputPath: outputPath,
	}, nil
}

// fileCounter generates unique filenames within the same millisecond.
var (
	fileCounterMu sync.Mutex
	fileCounter   int64
	lastMs        int64
)

func write(text string) (string, error) {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create truncate dir: %w", err)
	}

	// Generate unique filename: tool_<timestamp_ms>_<counter>.txt
	fileCounterMu.Lock()
	ms := time.Now().UnixMilli()
	if ms == lastMs {
		fileCounter++
	} else {
		lastMs = ms
		fileCounter = 0
	}
	counter := fileCounter
	fileCounterMu.Unlock()

	filename := fmt.Sprintf("tool_%d_%d.txt", ms, counter)
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("write truncate file: %w", err)
	}

	return path, nil
}

// Cleanup removes truncation files older than Retention.
func Cleanup() error {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read truncate dir: %w", err)
	}

	cutoff := time.Now().Add(-Retention)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "tool_") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}

// ListFiles returns truncation files sorted by modification time (newest first).
func ListFiles() ([]FileInfo, error) {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []FileInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "tool_") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, FileInfo{
			Path:    filepath.Join(dir, entry.Name()),
			Name:    entry.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})

	return files, nil
}

// FileInfo describes a truncation file.
type FileInfo struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
}

// Stats returns storage statistics.
func Stats() (count int, totalBytes int64, err error) {
	files, err := ListFiles()
	if err != nil {
		return 0, 0, err
	}
	for _, f := range files {
		count++
		totalBytes += f.Size
	}
	return count, totalBytes, nil
}
