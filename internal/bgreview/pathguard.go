package bgreview

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/tool"
)

// guardedWriter wraps a file-writing tool so it only touches paths inside the
// allowed directories. A review runs on bypass permissions and is shown a
// digest that includes tool output from the session, which may carry text
// written by an attacker; the prompt asks the reviewer to treat it as data,
// and this makes a mistake there harmless outside the memory directory.
type guardedWriter struct {
	tool.Tool
	dirs []string
}

// RestrictWrites returns t limited to writing under dirs. The tool's input
// must carry the target as file_path (Write, Edit) or path.
func RestrictWrites(t tool.Tool, dirs ...string) tool.Tool {
	resolved := make([]string, 0, len(dirs))
	for _, d := range dirs {
		resolved = append(resolved, resolvePath(d))
	}
	return &guardedWriter{Tool: t, dirs: resolved}
}

// Execute implements tool.Tool.
func (g *guardedWriter) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.ErrorResult("invalid input: " + err.Error()), nil
	}
	target := in.FilePath
	if target == "" {
		target = in.Path
	}
	if !g.allowed(target) {
		return tool.ErrorResult(fmt.Sprintf("%s is outside the directories this task may write to (%s).", target, strings.Join(g.dirs, ", "))), nil
	}
	return g.Tool.Execute(ctx, input)
}

func (g *guardedWriter) allowed(target string) bool {
	if target == "" || !filepath.IsAbs(target) {
		return false
	}
	p := resolvePath(target)
	for _, d := range g.dirs {
		if p == d || strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolvePath cleans p and resolves symlinks in its longest existing prefix,
// so a link inside an allowed directory cannot lead out of it and a path that
// does not exist yet is still judged by where it would land.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
