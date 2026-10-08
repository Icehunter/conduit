package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// WorkspaceFingerprint includes HEAD, the index, tracked working bytes and
// untracked nonignored files. It never resets or assumes a clean starting tree.
// Ignored build outputs are excluded; check policy must not rely on them as source.
func WorkspaceFingerprint(ctx context.Context, root string) (string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("verification fingerprint root: %w", err)
	}
	h := sha256.New()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("verification fingerprint git %s: %w", args[0], err)
		}
		return out, nil
	}
	// An unborn HEAD is valid. symbolic-ref identifies its initial branch.
	head, err := git("rev-parse", "--verify", "HEAD")
	if err != nil {
		head, err = git("symbolic-ref", "HEAD")
		if err != nil {
			return "", err
		}
	}
	_, _ = h.Write(head)
	index, err := git("ls-files", "--stage", "-z")
	if err != nil {
		return "", err
	}
	_, _ = h.Write(index)
	files, err := git("ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	names := strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00")
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if name == "" {
			continue
		}
		path := filepath.Join(root, name)
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("verification fingerprint path escapes repository")
		}
		info, err := os.Lstat(path)
		_, _ = fmt.Fprintf(h, "\x00%s\x00", name)
		if os.IsNotExist(err) {
			_, _ = io.WriteString(h, "deleted")
			continue
		}
		if err != nil {
			return "", fmt.Errorf("verification fingerprint stat: %w", err)
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", info.Mode(), info.Size())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return "", fmt.Errorf("verification fingerprint symlink: %w", err)
			}
			_, _ = io.WriteString(h, target)
		} else if info.Mode().IsRegular() {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", fmt.Errorf("verification fingerprint resolve: %w", err)
			}
			rel, err := filepath.Rel(root, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("verification fingerprint file %q escapes repository", name)
			}
			f, err := os.Open(path)
			if err != nil {
				return "", fmt.Errorf("verification fingerprint open: %w", err)
			}
			_, copyErr := io.Copy(h, contextReader{ctx: ctx, r: f})
			closeErr := f.Close()
			if copyErr != nil {
				return "", fmt.Errorf("verification fingerprint read: %w", copyErr)
			}
			if closeErr != nil {
				return "", fmt.Errorf("verification fingerprint close: %w", closeErr)
			}
		} else {
			return "", fmt.Errorf("verification fingerprint unsupported file %q (submodules need a separate policy)", name)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
