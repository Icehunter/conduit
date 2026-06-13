// Package hashedittool implements the HashEdit tool — content-hash-anchored
// editing that survives line-number drift. Each edit is addressed by the 7-char
// SHA-256 anchor of its context (line + neighbors) rather than an exact string.
package hashedittool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/hashline"
	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/pmezard/go-difflib/difflib"
)

// Tool implements the HashEdit tool.
type Tool struct {
	stager pendingedits.Stager
}

type pendingLookup interface {
	Pending(path string) (pendingedits.Entry, bool)
}

// New returns a HashEdit tool that writes directly to disk.
func New() *Tool { return &Tool{} }

// NewWithStager returns a HashEdit tool that stages writes through s.
func NewWithStager(s pendingedits.Stager) *Tool { return &Tool{stager: s} }

func (*Tool) Name() string { return "HashEdit" }

func (*Tool) Description() string {
	return "Edits a file using content-hash anchors instead of exact strings.\n\n" +
		"- Read the file with anchors:true to obtain 7-char anchor hashes for each line\n" +
		"- Each edit targets a line by its anchor hash — stable across line-number drift\n" +
		"- Multiple edits are applied bottom-to-top so earlier edits do not shift later targets\n" +
		"- Use delete:true to remove the anchored line entirely"
}

// EditOp is one anchored edit operation.
type EditOp struct {
	Anchor   string `json:"anchor"`
	NewLines string `json:"new_lines,omitempty"`
	Delete   bool   `json:"delete,omitempty"`
}

// Input is the typed view of the JSON input.
type Input struct {
	FilePath     string   `json:"file_path"`
	Edits        []EditOp `json:"edits"`
	ExpectUnique *bool    `json:"expect_unique,omitempty"` // default true
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to edit"
			},
			"edits": {
				"type": "array",
				"description": "List of anchored edits to apply",
				"items": {
					"type": "object",
					"properties": {
						"anchor": {
							"type": "string",
							"description": "7-char content-hash anchor from a prior Read with anchors:true"
						},
						"new_lines": {
							"type": "string",
							"description": "Replacement text for the target line (may be multi-line). Empty string inserts a blank line. Use delete:true to remove the line entirely."
						},
						"delete": {
							"type": "boolean",
							"description": "If true, remove the anchored line entirely (new_lines ignored)"
						}
					},
					"required": ["anchor"]
				}
			},
			"expect_unique": {
				"type": "boolean",
				"description": "Reject ambiguous anchors (default true)"
			}
		},
		"required": ["file_path", "edits"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Execute applies the anchored edits.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.FilePath) == "" {
		return tool.ErrorResult("`file_path` is required"), nil
	}
	if len(in.Edits) == 0 {
		return tool.ErrorResult("`edits` must not be empty"), nil
	}

	// Default expect_unique = true
	expectUnique := true
	if in.ExpectUnique != nil {
		expectUnique = *in.ExpectUnique
	}

	select {
	case <-ctx.Done():
		return tool.ErrorResult("cancelled"), nil
	default:
	}

	content, origContent, origExisted, err := t.readEditBase(in.FilePath)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}

	// Resolve all anchors first (before mutating content).
	type resolved struct {
		op      EditOp
		lineIdx int
	}
	var ops []resolved
	for _, edit := range in.Edits {
		lineIdx, count := hashline.Find(content, edit.Anchor)
		switch {
		case count == 0:
			return tool.ErrorResult(fmt.Sprintf(
				"stale anchor %q — re-read the file with anchors:true to get fresh anchors", edit.Anchor,
			)), nil
		case count > 1 && expectUnique:
			return tool.ErrorResult(fmt.Sprintf(
				"ambiguous anchor %q (%d matches) — re-read the file with anchors:true to narrow your edit", edit.Anchor, count,
			)), nil
			// When expect_unique is false and count > 1, we use the first matching line.
			// Callers that need exact targeting should leave expect_unique=true (default).
		}
		ops = append(ops, resolved{op: edit, lineIdx: lineIdx})
	}

	// Sort descending by lineIdx so bottom edits are applied first (line
	// numbers don't shift for earlier targets).
	sort.Slice(ops, func(i, j int) bool {
		return ops[i].lineIdx > ops[j].lineIdx
	})

	// Apply edits to a line slice.
	lines := splitLines(string(content))
	for _, r := range ops {
		if r.lineIdx < 0 || r.lineIdx >= len(lines) {
			return tool.ErrorResult(fmt.Sprintf("resolved line index %d out of range", r.lineIdx)), nil
		}
		if r.op.Delete {
			lines = append(lines[:r.lineIdx], lines[r.lineIdx+1:]...)
		} else {
			// Replace the single line with the new_lines content (may be multi-line).
			// new_lines == "" inserts a blank line (not a delete); use delete:true to remove the line
			newParts := splitLines(r.op.NewLines)
			var next []string
			next = append(next, lines[:r.lineIdx]...)
			next = append(next, newParts...)
			next = append(next, lines[r.lineIdx+1:]...)
			lines = next
		}
	}

	updated := strings.Join(lines, "\n")
	// Restore trailing newline if original had one.
	orig := string(content)
	if len(orig) > 0 && orig[len(orig)-1] == '\n' && (len(updated) == 0 || updated[len(updated)-1] != '\n') {
		updated += "\n"
	}

	if t.stager != nil {
		err := t.stager.Stage(pendingedits.Entry{
			Path:        in.FilePath,
			OrigContent: origContent,
			OrigExisted: origExisted,
			NewContent:  []byte(updated),
			Op:          pendingedits.OpEdit,
			ToolName:    "HashEdit",
		})
		if err == nil {
			return tool.TextResult(stagedMessage(in.FilePath, orig, updated, len(ops))), nil
		}
		if !errors.Is(err, pendingedits.ErrNotStaging) {
			return tool.ErrorResult(fmt.Sprintf("cannot stage edit: %v", err)), nil
		}
		// ErrNotStaging → fall through to direct write.
	}

	if err := writeAtomic(in.FilePath, updated); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot write file: %v", err)), nil
	}
	return tool.TextResult(editSummary(in.FilePath, orig, updated, len(ops))), nil
}

func (t *Tool) readEditBase(path string) (content, origContent []byte, origExisted bool, err error) {
	if e, ok := stagedPending(t.stager, path); ok {
		return append([]byte(nil), e.NewContent...), append([]byte(nil), e.OrigContent...), e.OrigExisted, nil
	}
	content, err = os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, false, fmt.Errorf("file not found: %w", err)
		}
		return nil, nil, false, fmt.Errorf("cannot read file: %w", err)
	}
	return content, append([]byte(nil), content...), true, nil
}

func stagedPending(s pendingedits.Stager, path string) (pendingedits.Entry, bool) {
	if s == nil {
		return pendingedits.Entry{}, false
	}
	lookup, ok := s.(pendingLookup)
	if !ok {
		return pendingedits.Entry{}, false
	}
	return lookup.Pending(path)
}

// splitLines splits s on "\n" without producing a trailing empty element.
func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	// Normalize CRLF.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	// If the string ends with \n, the Split produces a trailing ""; keep it
	// only if it represents a real blank last line (i.e., not a lone trailing \n).
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func stagedMessage(path, oldContent, newContent string, n int) string {
	return fmt.Sprintf("Staged: %s — awaiting review (%d edit(s))\n\n", displayPath(path), n) +
		editDiff(path, oldContent, newContent)
}

func editSummary(path, oldContent, newContent string, n int) string {
	label := displayPath(path)
	diff := editDiff(path, oldContent, newContent)
	if diff == fmt.Sprintf("Edited %s", label) {
		return fmt.Sprintf("HashEdit applied %d edit(s) to %s (no net change)", n, label)
	}
	return fmt.Sprintf("HashEdit applied %d edit(s) to %s\n\n", n, label) + diff
}

func displayPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~" + strings.TrimPrefix(path, home)
		}
	}
	return path
}

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

func writeAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".hashedit-*.tmp")
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
	if st, err := os.Lstat(path); err == nil {
		_ = os.Chmod(tmpPath, st.Mode())
	}
	return os.Rename(tmpPath, path)
}
