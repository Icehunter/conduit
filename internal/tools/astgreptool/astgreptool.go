// Package astgreptool implements the AstGrep tool — structural code search and
// rewrite via an installed ast-grep (sg) binary. The tool never auto-downloads
// ast-grep; it only uses whatever is on the user's PATH.
//
// Search mode (no rewrite field): runs ast-grep with --json stream output and
// formats each match as "file:line: matched_text".
//
// Rewrite mode (dry_run=true, default): identifies affected files and reports
// what would change without touching disk.
//
// Rewrite mode (dry_run=false): runs ast-grep with --update-all to apply
// in-place rewrites. Reads each target file before and after, then routes the
// change through the pending-edits staging gate. Falls back to direct disk
// write on ErrNotStaging.
package astgreptool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/tool"
)

// lookAstGrepFunc is injectable for tests. It locates the ast-grep binary,
// trying "ast-grep" then "sg" if the first name is not found.
var lookAstGrepFunc = func() (string, error) {
	if p, err := exec.LookPath("ast-grep"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("sg"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("ast-grep not found")
}

// runCmdFunc is injectable for tests. It runs a command and captures combined
// stdout/stderr, returning the output bytes and any exec error.
var runCmdFunc = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// Tool implements the AstGrep structural-search/rewrite tool.
type Tool struct {
	stager pendingedits.Stager
}

// New returns a new AstGrep tool that writes directly to disk on rewrites.
func New() *Tool { return &Tool{} }

// NewWithStager returns a new AstGrep tool that stages rewrites through s.
// When s is nil the tool behaves identically to New().
func NewWithStager(s pendingedits.Stager) *Tool { return &Tool{stager: s} }

// Name implements tool.Tool.
func (*Tool) Name() string { return "AstGrep" }

// Description is the prompt text the model sees.
func (*Tool) Description() string {
	return "Structural code search and rewrite using ast-grep (sg). " +
		"Unlike regex grep, ast-grep understands syntax trees — patterns match code structure. " +
		"Requires ast-grep or sg to be installed on PATH (not auto-downloaded). " +
		"Set rewrite to perform structural refactoring; dry_run=true (default) previews changes without applying them."
}

// InputSchema returns the JSON Schema for AstGrep inputs.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "The structural pattern to search for (ast-grep syntax)"
			},
			"rewrite": {
				"type": "string",
				"description": "Optional replacement pattern. When provided, enables rewrite mode."
			},
			"lang": {
				"type": "string",
				"description": "Language for pattern matching (e.g. go, ts, py, js, rust)"
			},
			"paths": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Paths to search (files or directories). Defaults to current directory."
			},
			"dry_run": {
				"type": "boolean",
				"description": "In rewrite mode: show what would change without applying (default true)"
			}
		},
		"required": ["pattern"]
	}`)
}

// IsReadOnly returns true when no rewrite field is set (pure search).
func (*Tool) IsReadOnly(raw json.RawMessage) bool {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return false // conservative
	}
	return in.Rewrite == ""
}

// IsConcurrencySafe returns false — rewrites mutate files.
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Input is the typed view of the JSON input.
type Input struct {
	Pattern string   `json:"pattern"`
	Rewrite string   `json:"rewrite,omitempty"`
	Lang    string   `json:"lang,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	DryRun  *bool    `json:"dry_run,omitempty"`
}

// isDryRun reports whether the input represents a dry-run. Defaults to true.
func (in *Input) isDryRun() bool {
	if in.DryRun == nil {
		return true
	}
	return *in.DryRun
}

// effectivePaths returns the search paths, defaulting to ".".
func (in *Input) effectivePaths() []string {
	if len(in.Paths) > 0 {
		return in.Paths
	}
	return []string{"."}
}

// Execute runs the structural search or rewrite.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return tool.ErrorResult("`pattern` is required"), nil
	}

	select {
	case <-ctx.Done():
		return tool.ErrorResult("cancelled"), nil
	default:
	}

	bin, err := lookAstGrepFunc()
	if err != nil {
		return tool.ErrorResult("ast-grep not found — install ast-grep (sg) to use structural search; not auto-downloaded"), nil
	}

	if in.Rewrite == "" {
		return t.runSearch(ctx, bin, &in)
	}
	return t.runRewrite(ctx, bin, &in)
}

// ── Search mode ──────────────────────────────────────────────────────────────

// matchJSON is the shape of a single JSON line from ast-grep's --json stream.
type matchJSON struct {
	File  string `json:"file"`
	Range struct {
		Start struct {
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"start"`
	} `json:"range"`
	Text        string `json:"text"`
	Replacement string `json:"replacement,omitempty"`
}

func (t *Tool) runSearch(ctx context.Context, bin string, in *Input) (tool.Result, error) {
	args := buildSearchArgs(in)
	out, runErr := runCmdFunc(ctx, bin, args...)

	if ctx.Err() != nil {
		return tool.ErrorResult("cancelled"), nil
	}

	// Exit code 1 from ast-grep means no matches — not an error.
	if runErr != nil && !isExitCode(runErr, 1) {
		return tool.ErrorResult(fmt.Sprintf("ast-grep error: %v\n%s", runErr, string(out))), nil
	}

	matches := parseMatchLines(out)
	if len(matches) == 0 {
		return tool.TextResult(fmt.Sprintf("No matches found for pattern: %s", in.Pattern)), nil
	}

	var sb strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&sb, "%s:%d: %s\n", m.File, m.Range.Start.Line+1, strings.TrimSpace(m.Text))
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

func buildSearchArgs(in *Input) []string {
	args := []string{"run", "--pattern", in.Pattern}
	if in.Lang != "" {
		args = append(args, "--lang", in.Lang)
	}
	args = append(args, "--json=stream")
	args = append(args, in.effectivePaths()...)
	return args
}

// ── Rewrite mode ─────────────────────────────────────────────────────────────

func (t *Tool) runRewrite(ctx context.Context, bin string, in *Input) (tool.Result, error) {
	// Step 1: identify affected files by running a search pass.
	searchArgs := buildSearchArgs(in)
	searchOut, searchErr := runCmdFunc(ctx, bin, searchArgs...)

	if ctx.Err() != nil {
		return tool.ErrorResult("cancelled"), nil
	}

	if searchErr != nil && !isExitCode(searchErr, 1) {
		return tool.ErrorResult(fmt.Sprintf("ast-grep error during search pass: %v\n%s", searchErr, string(searchOut))), nil
	}

	matches := parseMatchLines(searchOut)
	if len(matches) == 0 {
		return tool.TextResult(fmt.Sprintf("No matches found for pattern: %s", in.Pattern)), nil
	}

	// Collect unique affected files.
	affected := uniqueFiles(matches)

	if in.isDryRun() {
		return t.rewriteDryRun(matches, affected, in)
	}
	return t.rewriteApply(ctx, bin, in, affected)
}

func (t *Tool) rewriteDryRun(matches []matchJSON, affected []string, in *Input) (tool.Result, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Would rewrite %d %s (dry_run=true; set dry_run=false to apply):\n",
		len(affected), plural(len(affected), "file"))
	for _, f := range affected {
		fmt.Fprintf(&sb, "  %s\n", f)
	}
	sb.WriteString("\nMatches that would be rewritten:\n")
	for _, m := range matches {
		from := strings.TrimSpace(m.Text)
		to := in.Rewrite
		if r := strings.TrimSpace(m.Replacement); r != "" {
			to = r
		}
		fmt.Fprintf(&sb, "  %s:%d: %q → %q\n", m.File, m.Range.Start.Line+1, from, to)
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

func (t *Tool) rewriteApply(ctx context.Context, bin string, in *Input, affected []string) (tool.Result, error) {
	// Read original content of each affected file before running --update-all.
	origContents := make(map[string][]byte, len(affected))
	for _, f := range affected {
		b, err := os.ReadFile(f)
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("cannot read %s before rewrite: %v", f, err)), nil
		}
		origContents[f] = b
	}

	// Run ast-grep with --update-all to apply rewrites in-place.
	args := []string{"run", "--pattern", in.Pattern, "--rewrite", in.Rewrite}
	if in.Lang != "" {
		args = append(args, "--lang", in.Lang)
	}
	args = append(args, "--update-all")
	args = append(args, in.effectivePaths()...)

	applyOut, applyErr := runCmdFunc(ctx, bin, args...)
	if ctx.Err() != nil {
		return tool.ErrorResult("cancelled"), nil
	}
	if applyErr != nil && !isExitCode(applyErr, 1) {
		return tool.ErrorResult(fmt.Sprintf("ast-grep rewrite error: %v\n%s", applyErr, string(applyOut))), nil
	}

	// Stage or report each changed file.
	var staged, direct, unchanged []string
	for _, f := range affected {
		newContent, err := os.ReadFile(f)
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("cannot read %s after rewrite: %v", f, err)), nil
		}
		orig := origContents[f]
		if bytes.Equal(orig, newContent) {
			unchanged = append(unchanged, f)
			continue
		}

		if t.stager != nil {
			stageErr := t.stager.Stage(pendingedits.Entry{
				Path:        f,
				OrigContent: orig,
				OrigExisted: true,
				NewContent:  newContent,
				Op:          pendingedits.OpEdit,
				ToolName:    "AstGrep",
			})
			if stageErr == nil {
				staged = append(staged, f)
				// Restore the file to its original content because the change is
				// now pending review in the staging gate. The flusher will apply
				// newContent when the user approves.
				if writeErr := writeAtomic(f, orig); writeErr != nil {
					return tool.ErrorResult(fmt.Sprintf("cannot restore %s after staging: %v", f, writeErr)), nil
				}
				continue
			}
			if !errors.Is(stageErr, pendingedits.ErrNotStaging) {
				return tool.ErrorResult(fmt.Sprintf("cannot stage %s: %v", f, stageErr)), nil
			}
			// ErrNotStaging → file was already written by ast-grep; leave it.
		}
		direct = append(direct, f)
	}

	return tool.TextResult(buildApplyResult(staged, direct, unchanged)), nil
}

func buildApplyResult(staged, direct, unchanged []string) string {
	var sb strings.Builder
	total := len(staged) + len(direct)
	fmt.Fprintf(&sb, "Rewrote %d %s.\n", total, plural(total, "file"))
	if len(staged) > 0 {
		sb.WriteString("\nStaged for review:\n")
		for _, f := range staged {
			fmt.Fprintf(&sb, "  %s\n", f)
		}
	}
	if len(direct) > 0 {
		sb.WriteString("\nApplied directly to disk:\n")
		for _, f := range direct {
			fmt.Fprintf(&sb, "  %s\n", f)
		}
	}
	if len(unchanged) > 0 {
		sb.WriteString("\nUnchanged (already matched):\n")
		for _, f := range unchanged {
			fmt.Fprintf(&sb, "  %s\n", f)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// parseMatchLines parses streaming JSON output from ast-grep. Each line is a
// JSON object. Non-JSON lines are skipped defensively.
func parseMatchLines(out []byte) []matchJSON {
	var matches []matchJSON
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m matchJSON
		if err := json.Unmarshal([]byte(line), &m); err == nil && m.File != "" {
			matches = append(matches, m)
		}
	}
	return matches
}

// uniqueFiles returns deduplicated, ordered list of file paths from matches.
func uniqueFiles(matches []matchJSON) []string {
	seen := make(map[string]bool, len(matches))
	var out []string
	for _, m := range matches {
		if !seen[m.File] {
			seen[m.File] = true
			out = append(out, m.File)
		}
	}
	return out
}

// isExitCode reports whether err is an *exec.ExitError with the given code.
func isExitCode(err error, code int) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == code
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// writeAtomic writes b to path via a temp file + rename (preserves permissions).
func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".astgrep-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op if rename succeeded

	if _, err := tmp.Write(b); err != nil {
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
	if st, err := os.Lstat(path); err == nil {
		_ = os.Chmod(tmpPath, st.Mode())
	}
	return os.Rename(tmpPath, path)
}
