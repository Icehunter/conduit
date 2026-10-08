package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// ResolvePath makes p absolute against the effective working directory and
// cleans it. Tools must use the returned path for every filesystem call: the
// permission gate checks the lexically cleaned path, so a raw "link/../x"
// would be resolved by the OS through the symlink to a different file.
func ResolvePath(ctx context.Context, p string) string {
	if p == "" || hasURLScheme(p) {
		return p
	}
	if !filepath.IsAbs(p) {
		if dir := CwdFromContext(ctx); dir != "" {
			p = filepath.Join(dir, p)
		}
	}
	return filepath.Clean(p)
}

// hasURLScheme reports whether p starts with a scheme of two or more
// characters followed by ":/" (http://, issue:/...). Such strings are
// identifiers the Read tool dispatches on, not filesystem paths; a single
// letter is a Windows drive.
func hasURLScheme(p string) bool {
	i := strings.Index(p, ":/")
	if i < 2 {
		return false
	}
	for _, r := range p[:i] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

type isolatedCwdKey struct{}

// WithIsolatedCwd returns a context asking the sub-agent runner to start the
// child in dir (an isolated worktree). agent.RunSubAgentTyped reads it back.
func WithIsolatedCwd(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, isolatedCwdKey{}, dir)
}

// IsolatedCwd returns the directory set by WithIsolatedCwd, or "".
func IsolatedCwd(ctx context.Context) string {
	dir, _ := ctx.Value(isolatedCwdKey{}).(string)
	return dir
}
