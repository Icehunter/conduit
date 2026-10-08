package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

func defaultProtectedPaths() []string {
	return []string{"Makefile", "makefile", "GNUmakefile", "package.json", "pyproject.toml", "Cargo.toml", ".conduit/settings.json", ".conduit/settings.local.json", ".claude/settings.json", ".claude/settings.local.json"}
}

func validateProtectedPaths(patterns []string) error {
	for _, pattern := range patterns {
		if pattern == "" || path.IsAbs(pattern) || filepath.IsAbs(pattern) || strings.Contains(pattern, `\`) || path.Clean(pattern) == ".." || strings.HasPrefix(path.Clean(pattern), "../") || !doublestar.ValidatePattern(pattern) {
			return fmt.Errorf("invalid protectedPaths pattern %q", pattern)
		}
	}
	return nil
}

// IsProtectedPath rejects a proposed write before mutation, including a file
// that does not yet exist. Invalid relative paths fail closed.
func (r *Runner) IsProtectedPath(relative string) bool {
	if relative == "" || filepath.IsAbs(relative) || strings.Contains(relative, `\`) {
		return true
	}
	relative = path.Clean(relative)
	if relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return true
	}
	for _, pattern := range r.opts.ProtectedPaths {
		if matched, err := doublestar.Match(pattern, relative); err != nil || matched {
			return true
		}
	}
	return false
}

// CheckProtected rejects changes to check definitions since runner creation.
// Call it again after independent review and immediately before publishing.
func (r *Runner) CheckProtected(ctx context.Context) error {
	hash, err := r.protectedFingerprint(ctx)
	if err != nil {
		return err
	}
	if hash != r.protectedHash {
		return errors.New("verification policy or protected check definitions changed; restart with user-approved definitions")
	}
	return nil
}

func (r *Runner) protectedFingerprint(ctx context.Context) (string, error) {
	names := make([]string, 0)
	fsys := os.DirFS(r.root)
	for _, pattern := range r.opts.ProtectedPaths {
		err := doublestar.GlobWalk(fsys, pattern, func(name string, entry fs.DirEntry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("protected check definition %q must not be a symlink", name)
			}
			names = append(names, name)
			if len(names) > 100000 {
				return errors.New("protectedPaths matches too many files")
			}
			return nil
		}, doublestar.WithNoFollow(), doublestar.WithFailOnIOErrors())
		if err != nil {
			return "", fmt.Errorf("verification protected paths: %w", err)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	h := sha256.New()
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		file := filepath.Join(r.root, filepath.FromSlash(name))
		resolved, err := filepath.EvalSymlinks(file)
		if err != nil {
			return "", fmt.Errorf("protected file resolve: %w", err)
		}
		rel, err := filepath.Rel(r.root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("protected file %q escapes repository", name)
		}
		f, err := os.Open(file)
		if err != nil {
			return "", fmt.Errorf("protected file open: %w", err)
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return "", fmt.Errorf("protected file stat: %w", err)
		}
		if !info.Mode().IsRegular() {
			_ = f.Close()
			return "", fmt.Errorf("protected file %q is not regular", name)
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%d\x00", name, info.Mode(), info.Size())
		_, copyErr := io.Copy(h, contextReader{ctx: ctx, r: f})
		closeErr := f.Close()
		if copyErr != nil {
			return "", fmt.Errorf("protected file read: %w", copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("protected file close: %w", closeErr)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
