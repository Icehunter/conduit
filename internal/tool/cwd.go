package tool

import (
	"context"
	"os"
	"path/filepath"
)

type cwdKey struct{}

// WithCwd returns a context whose tools resolve relative paths and run
// shell commands under dir instead of the process working directory. Used
// to give a sub-agent an isolated git worktree without chdir'ing the whole
// process.
func WithCwd(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, cwdKey{}, dir)
}

// CwdFromContext returns the working directory set by WithCwd, or "".
func CwdFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	dir, _ := ctx.Value(cwdKey{}).(string)
	return dir
}

// Cwd returns the effective working directory: the context override when
// set, else the process working directory.
func Cwd(ctx context.Context) (string, error) {
	if dir := CwdFromContext(ctx); dir != "" {
		return dir, nil
	}
	return os.Getwd()
}

// ResolvePath makes p absolute against the effective working directory.
// Absolute paths are returned unchanged.
func ResolvePath(ctx context.Context, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if dir := CwdFromContext(ctx); dir != "" {
		return filepath.Join(dir, p)
	}
	return p
}
