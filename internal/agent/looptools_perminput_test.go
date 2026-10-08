package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/readtracker"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/fileedittool"
	"github.com/icehunter/conduit/internal/tools/filereadtool"
	"github.com/icehunter/conduit/internal/tools/hashedittool"
)

// The gate matches protected paths on this string, so every write tool must
// expose its target path here.
func TestToolPermissionInput_WriteTools(t *testing.T) {
	tests := []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"Edit", map[string]any{"file_path": "a.go"}, "a.go"},
		{"Write", map[string]any{"file_path": ".git/config"}, ".git/config"},
		{"HashEdit", map[string]any{"file_path": ".git/hooks/pre-commit"}, ".git/hooks/pre-commit"},
		{"NotebookEdit", map[string]any{"notebook_path": ".vscode/n.ipynb"}, ".vscode/n.ipynb"},
		{"NotebookEdit", map[string]any{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			if got := toolPermissionInput(tt.tool, tt.input, ""); got != tt.want {
				t.Errorf("toolPermissionInput(%q) = %q, want %q", tt.tool, got, tt.want)
			}
		})
	}
}

func TestToolPermissionInput_AstGrepAndSkillManage(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{"astgrep search has no write target", "AstGrep", map[string]any{"pattern": "x", "paths": []any{".git/hooks"}}, ""},
		{"astgrep dry-run by default has no write target", "AstGrep", map[string]any{"pattern": "x", "rewrite": "y", "paths": []any{".git/hooks"}}, ""},
		{"astgrep explicit dry-run has no write target", "AstGrep", map[string]any{"pattern": "x", "rewrite": "y", "dry_run": true}, ""},
		{"astgrep rewrite default path", "AstGrep", map[string]any{"pattern": "x", "rewrite": "y", "dry_run": false}, "."},
		{"astgrep rewrite single path", "AstGrep", map[string]any{"pattern": "x", "rewrite": "y", "dry_run": false, "paths": []any{"src/a.go"}}, "src/a.go"},
		{"astgrep rewrite surfaces a later protected path", "AstGrep", map[string]any{"pattern": "x", "rewrite": "y", "dry_run": false, "paths": []any{"src", ".git/hooks", "docs"}}, ".git/hooks"},
		{"astgrep rewrite no protected path uses first", "AstGrep", map[string]any{"pattern": "x", "rewrite": "y", "dry_run": false, "paths": []any{"src", "docs"}}, "src"},
		{"skill list has no target", "SkillManage", map[string]any{"action": "list"}, ""},
		{"skill view has no target", "SkillManage", map[string]any{"action": "view", "name": "a"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolPermissionInput(tt.tool, tt.input, ""); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolPermissionInput_SkillManageWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, action := range []string{"create", "update", "promote"} {
		got := toolPermissionInput("SkillManage", map[string]any{"action": action, "name": "../../.git/hooks/x"}, "")
		if !permissions.IsProtectedPath("", got) {
			t.Errorf("%s: target %q should resolve into .git", action, got)
		}
	}
	got := toolPermissionInput("SkillManage", map[string]any{"action": "create", "name": "ok", "scope": "project"}, "")
	if !strings.HasSuffix(got, "/.claude/skills/ok/SKILL.md") {
		t.Errorf("project create target = %q", got)
	}
}

func TestModelPrefersPlainEdit(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-opus-5-5", false},
		{"gpt-5", false},
		{"qwen3.8-27b", false},
		{"kimi-k2", true},
		{"moonshotai/Kimi-K2-Instruct", true},
		{"mimo-v2", true},
		{"MiniMax-M2", true},
		{"deepseek-chat", true},
		{"deepseek/deepseek-r1", true},
		{"step-3", true},
		{"stepfun/step-3.5-flash", true},
		{"gpt-5-codex-spark", true},
		{"glm-5.3-flash", true},
		{"glm-5.3", false},
		{"glm-4.6", false},
		{"misstep-model", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := modelPrefersPlainEdit(tt.model); got != tt.want {
				t.Errorf("modelPrefersPlainEdit(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestBuildToolDefsFor_HashEditByModel(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(fileedittool.New())
	reg.Register(hashedittool.New())
	names := func(model string) []string {
		defs := buildToolDefsFor(reg, model)
		out := make([]string, 0, len(defs))
		for _, d := range defs {
			out = append(out, d.Name)
		}
		return out
	}
	if got := names("claude-opus-5-5"); !slices.Equal(got, []string{"Edit", "HashEdit"}) {
		t.Errorf("strong model tools = %v", got)
	}
	got := names("deepseek-chat")
	if !slices.Equal(got, []string{"Edit"}) {
		t.Errorf("weak model tools = %v", got)
	}
	defs := buildToolDefsFor(reg, "deepseek-chat")
	if defs[len(defs)-1].CacheControl == nil {
		t.Error("cache breakpoint must land on the last remaining tool")
	}
}

func TestReadStateResetOnClearAndCompaction(t *testing.T) {
	tests := []struct {
		name      string
		do        func(l *Loop)
		wantReset bool
	}{
		{"clear resets", func(l *Loop) { l.ForgetContextCompacted() }, true},
		// Compaction keeps reads: the hash check still guards stale edits.
		{"compaction keeps", func(l *Loop) { l.MarkContextCompacted("auto") }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := readtracker.New()
			reg := tool.NewRegistry()
			reg.Register(filereadtool.New().WithTracker(tr))
			tr.RecordRead("/x/f", []byte("v"), nil)
			tt.do(&Loop{reg: reg})
			reset := tr.Check("/x/f", []byte("v")) != nil
			if reset != tt.wantReset {
				t.Fatalf("reset = %v, want %v", reset, tt.wantReset)
			}
		})
	}
}

// Targets resolve against the loop's working directory, not the process's.
func TestToolPermissionInput_UsesLoopCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	got := toolPermissionInput("SkillManage", map[string]any{"action": "create", "name": "ok", "scope": "project"}, cwd)
	if !strings.HasPrefix(got, cwd) {
		t.Errorf("SkillManage target %q is not under the loop cwd %q", got, cwd)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink into .git is only protected when resolved against the loop cwd.
	if err := os.Symlink(filepath.Join(cwd, ".git"), filepath.Join(cwd, "gitlink")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	in := map[string]any{"rewrite": "x", "dry_run": false, "paths": []any{"src", "gitlink/hooks/pre-commit"}}
	if got := toolPermissionInput("AstGrep", in, cwd); got != "gitlink/hooks/pre-commit" {
		t.Errorf("AstGrep target = %q, want the protected path", got)
	}
}
