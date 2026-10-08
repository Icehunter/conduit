// Package coding owns bounded local implementation tasks and their evidence.
package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/pendingedits"
)

const maxSourceBytes = 64 * 1024

var ErrWriteDenied = errors.New("coding: write denied")

var ErrWorkspaceChanged = errors.New("coding: workspace changed outside the candidate")

// Source is the actual local model input and preimage used for conflict checks.
type Source struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Exists  bool   `json:"exists"`
}

type edit struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}
type draft struct {
	Edits []edit `json:"edits"`
}

// SafePath rejects traversal, symlinks, non-regular targets and policy edits.
// The explicit task scope is checked separately by ReadSources/ApplyDraft.
func SafePath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return "", fmt.Errorf("coding: invalid relative path %q", rel)
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("coding: path escapes task root")
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" || part == ".conduit" || part == ".claude" {
			return "", fmt.Errorf("coding: protected path %q", rel)
		}
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("coding: resolve root: %w", err)
	}
	path := base
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		st, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("coding: inspect path: %w", err)
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("coding: symlink in target %q", rel)
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return "", fmt.Errorf("coding: non-regular target %q", rel)
		}
	}
	return path, nil
}

// ReadSources captures an explicit bounded task scope before generation.
func ReadSources(root string, files []string) ([]Source, error) {
	if len(files) == 0 || len(files) > 32 {
		return nil, fmt.Errorf("coding: provide 1 to 32 task files")
	}
	seen := map[string]bool{}
	out := make([]Source, 0, len(files))
	total := 0
	for _, name := range files {
		path, err := SafePath(root, name)
		if err != nil {
			return nil, err
		}
		rel := filepath.ToSlash(filepath.Clean(name))
		if seen[rel] {
			return nil, fmt.Errorf("coding: duplicate task file %q", rel)
		}
		seen[rel] = true
		data, err := os.ReadFile(path)
		exists := true
		if errors.Is(err, os.ErrNotExist) {
			exists = false
			data = nil
		} else if err != nil {
			return nil, fmt.Errorf("coding: read %s: %w", rel, err)
		}
		total += len(data)
		if total > maxSourceBytes {
			return nil, fmt.Errorf("coding: task source exceeds %d bytes; narrow the task", maxSourceBytes)
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return nil, fmt.Errorf("coding: task file %q must be UTF-8 text", rel)
		}
		out = append(out, Source{Path: rel, Content: string(data), Exists: exists})
	}
	return out, nil
}

// ApplyDraft validates all edits and permissions before writing any file.
// On a later I/O conflict it reports the partial application; it never restores
// over potentially concurrent user edits. The caller must not mark it verified.
func ApplyDraft(ctx context.Context, root string, sources []Source, response string, authorize func(context.Context, string) error) (string, error) {
	patch, _, err := ApplyDraftChecked(ctx, root, sources, response, authorize)
	return patch, err
}

// ApplyDraftChecked returns the intended postimage derived from validated edits,
// not a later disk read that could accidentally attribute another writer's edit.
func ApplyDraftChecked(ctx context.Context, root string, sources []Source, response string, authorize func(context.Context, string) error) (string, []Source, error) {
	if authorize == nil {
		return "", nil, fmt.Errorf("%w: authorization is required", ErrWriteDenied)
	}
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, fmt.Errorf("coding: resolve root: %w", err)
	}
	if len(response) > 1024*1024 {
		return "", nil, fmt.Errorf("coding: draft exceeds output limit")
	}
	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, "```") {
		lines := strings.Split(response, "\n")
		if len(lines) < 3 || strings.TrimSpace(lines[len(lines)-1]) != "```" {
			return "", nil, fmt.Errorf("coding: incomplete fenced draft")
		}
		response = strings.Join(lines[1:len(lines)-1], "\n")
	}
	var d draft
	dec := json.NewDecoder(strings.NewReader(response))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return "", nil, fmt.Errorf("coding: invalid edit JSON: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return "", nil, fmt.Errorf("coding: trailing draft content")
	}
	if len(d.Edits) > 32 {
		return "", nil, fmt.Errorf("coding: too many edits")
	}
	intended := append([]Source(nil), sources...)
	known := map[string]Source{}
	for _, s := range sources {
		known[s.Path] = s
	}
	seen := map[string]bool{}
	entries := make([]pendingedits.Entry, 0, len(d.Edits))
	var patch strings.Builder
	for _, e := range d.Edits {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		s, ok := known[e.Path]
		if !ok || seen[e.Path] {
			return "", nil, fmt.Errorf("coding: duplicate or out-of-scope edit %q", e.Path)
		}
		seen[e.Path] = true
		path, err := SafePath(root, e.Path)
		if err != nil {
			return "", nil, err
		}
		next := e.New
		if s.Exists {
			if e.Old == "" || strings.Count(s.Content, e.Old) != 1 {
				return "", nil, fmt.Errorf("coding: old text must match exactly once in %s", e.Path)
			}
			next = strings.Replace(s.Content, e.Old, e.New, 1)
		} else if e.Old != "" {
			return "", nil, fmt.Errorf("coding: new file must use empty old text")
		}
		if !utf8.ValidString(next) || strings.IndexByte(next, 0) >= 0 {
			return "", nil, fmt.Errorf("coding: invalid replacement text")
		}
		if len(next) > 1024*1024 {
			return "", nil, fmt.Errorf("coding: replacement exceeds size limit")
		}
		if err := authorize(ctx, path); err != nil {
			return "", nil, fmt.Errorf("%w: %w", ErrWriteDenied, err)
		}
		for i, source := range intended {
			if source.Path == e.Path {
				intended[i] = Source{Path: e.Path, Content: next, Exists: true}
				break
			}
		}
		entries = append(entries, pendingedits.Entry{Path: path, OrigContent: []byte(s.Content), OrigExisted: s.Exists, NewContent: []byte(next), Op: pendingedits.OpWrite, ToolName: "CodingTask"})
		fmt.Fprintf(&patch, "--- a/%s\n+++ b/%s\n%s\n", e.Path, e.Path, pendingedits.DiffString([]byte(s.Content), []byte(next)))
	}
	// Recheck all consumed inputs, including files the draft did not change.
	files := make([]string, len(sources))
	for i, s := range sources {
		files[i] = s.Path
	}
	current, err := ReadSources(root, files)
	if err != nil {
		return "", nil, err
	}
	for i, s := range sources {
		if current[i] != s {
			return "", nil, fmt.Errorf("%w during generation: %s", ErrWorkspaceChanged, s.Path)
		}
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return patch.String(), nil, err
		}
		if _, err := SafePath(root, mustRelative(root, e.Path)); err != nil {
			return patch.String(), nil, err
		}
		if err := pendingedits.FlushOne(e); err != nil {
			return patch.String(), nil, fmt.Errorf("%w: patch application incomplete: %w", ErrWorkspaceChanged, err)
		}
	}
	return patch.String(), intended, nil
}

func mustRelative(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return ".."
	}
	return r
}

// CumulativePatch describes the final candidate against its task preimage.
func CumulativePatch(initial, current []Source) string {
	var b strings.Builder
	for i, s := range initial {
		if i < len(current) && s != current[i] {
			fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n%s\n", s.Path, s.Path, pendingedits.DiffString([]byte(s.Content), []byte(current[i].Content)))
		}
	}
	return b.String()
}
