// Package hashedittool implements the HashEdit tool — content-hash-anchored
// editing that survives line-number drift. Each edit is addressed by the 7-char
// SHA-256 anchor of its context (line + neighbors) rather than an exact string.
package hashedittool

import (
	"bytes"
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
	"github.com/icehunter/conduit/internal/readtracker"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/pmezard/go-difflib/difflib"
)

// Tool implements the HashEdit tool.
type Tool struct {
	stager  pendingedits.Stager
	tracker *readtracker.Tracker
}

type pendingLookup interface {
	Pending(path string) (pendingedits.Entry, bool)
}

// New returns a HashEdit tool that writes directly to disk.
func New() *Tool { return &Tool{} }

// NewWithStager returns a HashEdit tool that stages writes through s.
func NewWithStager(s pendingedits.Stager) *Tool { return &Tool{stager: s} }

// WithTracker makes the tool refuse to edit a file the model has not read,
// or that changed since it was read, and reject anchors for lines no anchored
// Read displayed. It returns t.
func (t *Tool) WithTracker(rt *readtracker.Tracker) *Tool {
	t.tracker = rt
	return t
}

func (*Tool) Name() string { return "HashEdit" }

func (*Tool) Description() string {
	return "Edits a file using content-hash anchors instead of exact strings.\n\n" +
		"- Read the file with anchors:true to obtain 7-char anchor hashes for each line\n" +
		"- Each edit targets a line by its anchor hash — stable across line-number drift\n" +
		"- Multiple edits are applied bottom-to-top so earlier edits do not shift later targets\n" +
		"- Use delete:true to remove the anchored line entirely\n" +
		"- Set end_anchor to make an edit cover the inclusive range anchor..end_anchor\n" +
		"- Set insert:\"before\" or \"after\" to add new_lines next to the anchored line without replacing it\n" +
		"- Anchors must come from a Read with anchors:true; lines that Read never showed are rejected"
}

// EditOp is one anchored edit operation.
type EditOp struct {
	Anchor    string `json:"anchor"`
	EndAnchor string `json:"end_anchor,omitempty"` // inclusive range end for replace/delete
	NewLines  string `json:"new_lines,omitempty"`
	Delete    bool   `json:"delete,omitempty"`
	Insert    string `json:"insert,omitempty"` // "before" | "after": add new_lines beside the anchored line
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
						"end_anchor": {
							"type": "string",
							"description": "Optional anchor of the last line of an inclusive range starting at anchor. The whole range is replaced by new_lines, or removed with delete:true."
						},
						"delete": {
							"type": "boolean",
							"description": "If true, remove the anchored line (or range) entirely (new_lines ignored)"
						},
						"insert": {
							"type": "string",
							"enum": ["before", "after"],
							"description": "Insert new_lines before or after the anchored line instead of replacing it. Cannot be combined with delete or end_anchor."
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
	in.FilePath = tool.ResolvePath(ctx, in.FilePath)
	if !filepath.IsAbs(in.FilePath) {
		return tool.ErrorResult(fmt.Sprintf("`file_path` must be absolute, got: %s", in.FilePath)), nil
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
	if origExisted {
		if err := t.tracker.Check(in.FilePath, origContent); err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
	}

	// Address lines on the content without its BOM; it is restored on write.
	hasBOM := bytes.HasPrefix(content, []byte(hashline.BOM))
	content = bytes.TrimPrefix(content, []byte(hashline.BOM))
	// Edit on LF-normalised text and convert back, so a CRLF file keeps its
	// line endings. Files with mixed endings are treated as LF-only text.
	crlf := bytes.Contains(content, []byte("\r\n")) && !bytes.Contains(bytes.ReplaceAll(content, []byte("\r\n"), nil), []byte("\n"))
	if crlf {
		content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	}

	ops, errMsg := t.resolveOps(in.FilePath, content, in.Edits, expectUnique)
	if errMsg != "" {
		return tool.ErrorResult(errMsg), nil
	}

	// Apply bottom-to-top so earlier targets keep their line numbers.
	lines := splitLines(string(content))
	for _, r := range ops {
		next := make([]string, 0, len(lines)-(r.end-r.start)+len(r.lines))
		next = append(next, lines[:r.start]...)
		next = append(next, r.lines...)
		next = append(next, lines[r.end:]...)
		lines = next
	}

	updated := strings.Join(lines, "\n")
	// Restore trailing newline if original had one (an emptied file stays empty).
	orig := string(content)
	if len(lines) > 0 && len(orig) > 0 && orig[len(orig)-1] == '\n' && (len(updated) == 0 || updated[len(updated)-1] != '\n') {
		updated += "\n"
	}
	toWrite := updated
	if crlf {
		toWrite = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	if hasBOM {
		toWrite = hashline.BOM + toWrite
	}

	if t.stager != nil {
		err := t.stager.Stage(pendingedits.Entry{
			Path:        in.FilePath,
			OrigContent: origContent,
			OrigExisted: origExisted,
			NewContent:  []byte(toWrite),
			Op:          pendingedits.OpEdit,
			ToolName:    "HashEdit",
		})
		if err == nil {
			t.tracker.RecordStaged(in.FilePath, []byte(toWrite))
			return tool.TextResult(stagedMessage(in.FilePath, orig, updated, len(ops))), nil
		}
		if !errors.Is(err, pendingedits.ErrNotStaging) {
			return tool.ErrorResult(fmt.Sprintf("cannot stage edit: %v", err)), nil
		}
		// ErrNotStaging → fall through to direct write.
	}

	if err := writeAtomic(in.FilePath, toWrite); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot write file: %v", err)), nil
	}
	t.tracker.RecordWrite(in.FilePath, []byte(toWrite))
	return tool.TextResult(editSummary(in.FilePath, orig, updated, len(ops))), nil
}

// lineEdit replaces lines[start:end] with lines; start == end is a pure insert.
type lineEdit struct {
	start, end int
	lines      []string
}

// resolveOps turns the requested edits into non-overlapping line edits ordered
// so they can be applied one after another without shifting later targets. On
// failure it returns a message for the model.
func (t *Tool) resolveOps(path string, content []byte, edits []EditOp, expectUnique bool) ([]lineEdit, string) {
	var anchors []hashline.Anchor
	locate := func(anchor string) (int, string) {
		idx, count := hashline.Find(content, anchor)
		switch {
		case count == 0:
			return 0, fmt.Sprintf("stale anchor %q — re-read the file with anchors:true to get fresh anchors", anchor)
		case count > 1 && expectUnique:
			return 0, fmt.Sprintf("ambiguous anchor %q (%d matches) — re-read the file with anchors:true to narrow your edit", anchor, count)
			// When expect_unique is false and count > 1, the first matching line is used.
		}
		return idx, ""
	}
	// checkShown rejects ranges that cover a line no anchored Read displayed.
	checkShown := func(from, to int) string {
		if anchors == nil {
			anchors = hashline.Compute(content)
		}
		for i := from; i <= to && i < len(anchors); i++ {
			if !t.tracker.Shown(path, anchors[i].Hash) {
				return fmt.Sprintf("line %d (anchor %q) was never shown to you — Read the file with anchors:true (covering that line) before editing it", i+1, anchors[i].Hash)
			}
		}
		return ""
	}

	ops := make([]lineEdit, 0, len(edits))
	for _, e := range edits {
		first, msg := locate(e.Anchor)
		if msg != "" {
			return nil, msg
		}
		last := first
		if e.EndAnchor != "" {
			if last, msg = locate(e.EndAnchor); msg != "" {
				return nil, msg
			}
			if last < first {
				return nil, fmt.Sprintf("end_anchor %q (line %d) is above anchor %q (line %d)", e.EndAnchor, last+1, e.Anchor, first+1)
			}
		}
		if msg := checkShown(first, last); msg != "" {
			return nil, msg
		}
		switch e.Insert {
		case "":
			var repl []string
			if !e.Delete {
				// new_lines == "" is a blank line, not a delete; use delete:true to remove lines.
				repl = splitLines(e.NewLines)
			}
			ops = append(ops, lineEdit{start: first, end: last + 1, lines: repl})
		case "before", "after":
			if e.Delete || e.EndAnchor != "" {
				return nil, fmt.Sprintf("insert:%q cannot be combined with delete or end_anchor", e.Insert)
			}
			at := first
			if e.Insert == "after" {
				at = first + 1
			}
			ops = append(ops, lineEdit{start: at, end: at, lines: splitLines(e.NewLines)})
		default:
			return nil, fmt.Sprintf("invalid insert %q; must be \"before\" or \"after\"", e.Insert)
		}
	}

	// Bottom first. At equal starts a replacement goes before an insertion
	// (it spans lines from start, the insertion lands just before them), and
	// among equal inserts the later request goes first so they read in order.
	order := make([]int, len(ops))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := ops[order[a]], ops[order[b]]
		if x.start != y.start {
			return x.start > y.start
		}
		if x.end != y.end {
			return x.end > y.end
		}
		return order[a] > order[b]
	})
	sorted := make([]lineEdit, len(ops))
	for i, o := range order {
		sorted[i] = ops[o]
	}
	for i := 1; i < len(sorted); i++ {
		// sorted[i-1] sits at or below sorted[i]; they must not share lines.
		if sorted[i-1].start < sorted[i].end {
			return nil, fmt.Sprintf("edits overlap near line %d — merge them into one edit", sorted[i].start+1)
		}
	}
	return sorted, ""
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
