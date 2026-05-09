// Package fileedittool implements the Edit tool — targeted string replacement
// in files. Mirrors src/tools/FileEditTool/FileEditTool.ts.
//
// Core contract (from TS source + decoded binary):
//   - old_string must appear exactly once in the file (or replace_all=true)
//   - old_string == new_string is rejected
//   - old_string == "" creates a new file with new_string as content
//   - Quote normalization: curly quotes in file match straight quotes from model
//   - Atomic write: temp file + rename, preserves permissions
package fileedittool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/pmezard/go-difflib/difflib"
)

// Tool implements the Edit tool. When stager is non-nil writes are routed
// through the diff-first review gate instead of touching disk directly.
type Tool struct {
	stager pendingedits.Stager
}

// New returns a fresh Edit tool that writes directly to disk. Used by
// sub-agent registries (council, summariser, Task) that should never stage.
func New() *Tool { return &Tool{} }

// NewWithStager returns an Edit tool that stages writes through s. When s is
// nil the tool behaves identically to New().
func NewWithStager(s pendingedits.Stager) *Tool { return &Tool{stager: s} }

func (*Tool) Name() string { return "Edit" }

func (*Tool) Description() string {
	return "Performs exact string replacements in files.\n\n" +
		"- old_string must uniquely identify the location to edit\n" +
		"- old_string == \"\" creates a new file with new_string as content\n" +
		"- set replace_all=true to replace every occurrence\n" +
		"- The edit will FAIL if old_string is not found in the file"
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to modify"
			},
			"old_string": {
				"type": "string",
				"description": "The text to replace (empty string to create a new file)"
			},
			"new_string": {
				"type": "string",
				"description": "The text to replace it with"
			},
			"replace_all": {
				"type": "boolean",
				"description": "Replace all occurrences (default false — replaces first only)"
			}
		},
		"required": ["file_path", "old_string", "new_string"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Input is the typed view of the JSON input.
type Input struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// Execute applies the edit.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.FilePath) == "" {
		return tool.ErrorResult("`file_path` is required"), nil
	}
	if in.OldString == in.NewString {
		return tool.ErrorResult("No changes to make: old_string and new_string are exactly the same."), nil
	}

	select {
	case <-ctx.Done():
		return tool.ErrorResult("cancelled"), nil
	default:
	}

	// Create new file when old_string is empty.
	// Guard: if the file already exists, refuse rather than silently truncating it.
	// The model must supply old_string to edit existing content, or use Write to
	// explicitly overwrite. This prevents clobbering via acceptEdits mode bypass.
	if in.OldString == "" {
		if _, err := os.Lstat(in.FilePath); err == nil {
			return tool.ErrorResult(
				"file already exists; supply a non-empty old_string to edit it, " +
					"or use the Write tool to explicitly overwrite",
			), nil
		}
		return t.createFile(in.FilePath, in.NewString)
	}

	// Read existing file.
	content, err := os.ReadFile(in.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.ErrorResult(fmt.Sprintf("file not found: %s", in.FilePath)), nil
		}
		return tool.ErrorResult(fmt.Sprintf("cannot read file: %v", err)), nil
	}

	fileStr := string(content)

	// Find the old_string, with curly-quote normalization fallback.
	actual, found := findString(fileStr, in.OldString)
	if !found {
		return tool.ErrorResult(fmt.Sprintf(
			"String not found in file.\n\nold_string:\n%s", in.OldString,
		)), nil
	}

	// Apply replacement.
	var updated string
	if in.ReplaceAll {
		updated = strings.ReplaceAll(fileStr, actual, in.NewString)
	} else {
		updated = strings.Replace(fileStr, actual, in.NewString, 1)
	}

	if updated == fileStr {
		return tool.ErrorResult("Edit produced no change — old_string may not be unique enough."), nil
	}

	if t.stager != nil {
		err := t.stager.Stage(pendingedits.Entry{
			Path:        in.FilePath,
			OrigContent: append([]byte(nil), content...),
			OrigExisted: true,
			NewContent:  []byte(updated),
			Op:          pendingedits.OpEdit,
			ToolName:    "Edit",
		})
		if err == nil {
			return tool.TextResult(stagedMessage(in.FilePath, fileStr, updated)), nil
		}
		if !errors.Is(err, pendingedits.ErrNotStaging) {
			return tool.ErrorResult(fmt.Sprintf("cannot stage edit: %v", err)), nil
		}
		// ErrNotStaging → fall through to direct write.
	}

	if err := writeAtomic(in.FilePath, updated); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot write file: %v", err)), nil
	}

	return tool.TextResult(editDiff(in.FilePath, fileStr, updated)), nil
}

// createFile creates a new file (or overwrites) with the given content.
func (t *Tool) createFile(path, content string) (tool.Result, error) {
	if t.stager != nil {
		err := t.stager.Stage(pendingedits.Entry{
			Path:        path,
			OrigContent: nil,
			OrigExisted: false,
			NewContent:  []byte(content),
			Op:          pendingedits.OpEdit,
			ToolName:    "Edit",
		})
		if err == nil {
			return tool.TextResult(stagedMessage(path, "", content)), nil
		}
		if !errors.Is(err, pendingedits.ErrNotStaging) {
			return tool.ErrorResult(fmt.Sprintf("cannot stage create: %v", err)), nil
		}
		// ErrNotStaging → fall through to direct write.
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot create directory: %v", err)), nil
	}
	if err := writeAtomic(path, content); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot write file: %v", err)), nil
	}
	return tool.TextResult(editDiff(path, "", content)), nil
}

// stagedMessage formats the tool result for a staged edit. The "Staged: …"
// prefix is the marker downstream layers (PostToolUse hooks, the TUI tool
// renderer) use to recognise pending-edit results.
func stagedMessage(path, oldContent, newContent string) string {
	return "Staged: " + displayPath(path) + " — awaiting review\n\n" + editDiff(path, oldContent, newContent)
}

// displayPath shortens paths under $HOME to ~/ form for display. Mirrors the
// labelling logic used by editDiff so messages render consistently.
func displayPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~" + strings.TrimPrefix(path, home)
		}
	}
	return path
}

// editDiff generates a unified diff between old and new content wrapped in a
// fenced diff block for TUI rendering. Empty oldContent means a new file.
func editDiff(path, oldContent, newContent string) string {
	label := path
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			label = "~" + strings.TrimPrefix(path, home)
		}
	}
	ud := difflib.UnifiedDiff{
		A:        difflib.SplitLines(oldContent),
		B:        difflib.SplitLines(newContent),
		FromFile: label,
		ToFile:   label,
		Context:  3,
	}
	text, err := difflib.GetUnifiedDiffString(ud)
	if err != nil || text == "" {
		return fmt.Sprintf("Edited %s", label)
	}
	return "```diff\n" + strings.TrimRight(text, "\n") + "\n```"
}

// findString looks for needle in haystack. First tries exact match, then
// tries with curly quotes normalized to straight quotes (the model outputs
// straight quotes but files may contain typographic curly quotes).
func findString(haystack, needle string) (actual string, found bool) {
	if strings.Contains(haystack, needle) {
		return needle, true
	}
	// Normalize curly → straight in both and re-search.
	normHaystack := normalizeQuotes(haystack)
	normNeedle := normalizeQuotes(needle)
	idx := strings.Index(normHaystack, normNeedle)
	if idx < 0 {
		return "", false
	}
	// The actual bytes in the file at the matched position.
	// Measure in runes to correctly handle multibyte characters.
	runeIdx := utf8.RuneCountInString(haystack[:idx])
	runeLen := utf8.RuneCountInString(needle)
	runes := []rune(haystack)
	if runeIdx+runeLen > len(runes) {
		return needle, true // fallback
	}
	return string(runes[runeIdx : runeIdx+runeLen]), true
}

// normalizeQuotes converts typographic curly quotes to their ASCII equivalents.
func normalizeQuotes(s string) string {
	s = strings.ReplaceAll(s, "‘", "'") // '
	s = strings.ReplaceAll(s, "’", "'") // '
	s = strings.ReplaceAll(s, "“", `"`) // "
	s = strings.ReplaceAll(s, "”", `"`) // "
	return s
}

// writeAtomic writes content to path via a temp file + rename.
func writeAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".edit-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op if rename succeeded

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Preserve original file permissions if possible.
	// Use Lstat so we read the mode of the file AT path, not through a symlink.
	if st, err := os.Lstat(path); err == nil {
		_ = os.Chmod(tmpPath, st.Mode())
	}
	return os.Rename(tmpPath, path)
}
