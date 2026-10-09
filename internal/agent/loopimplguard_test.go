package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/implguard"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tool"
)

// execCountTool counts Execute calls so a test can tell a denied call from one
// that ran.
type execCountTool struct {
	fakeTool
	runs atomic.Int32
}

func (c *execCountTool) Execute(ctx context.Context, in json.RawMessage) (tool.Result, error) {
	c.runs.Add(1)
	return c.fakeTool.Execute(ctx, in)
}

func guardOn(on bool) *implguard.Guard {
	return implguard.New(func() (bool, string) { return on, "mcp.local-coder" })
}

func runGuarded(t *testing.T, toolName, inputJSON string, cfg LoopConfig) (denied bool, text string, runs int32) {
	t.Helper()
	ct := &execCountTool{fakeTool: fakeTool{name: toolName, result: "ran"}}
	reg := tool.NewRegistry()
	reg.Register(ct)
	lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput(toolName, "toolu_g", inputJSON), textOnlySSE("done")}, reg, cfg)
	defer srv.Close()
	var res LoopEvent
	if _, err := lp.Run(context.Background(), []api.Message{user("go")}, func(ev LoopEvent) {
		if ev.Type == EventToolResult {
			res = ev
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res.IsError && strings.Contains(res.ResultText, "implement role"), res.ResultText, ct.runs.Load()
}

func TestLoop_ImplGuard(t *testing.T) {
	dir := t.TempDir()
	touched := filepath.Join(dir, "touched.go")
	other := filepath.Join(dir, "other.go")

	tests := []struct {
		name       string
		tool       string
		input      string
		mode       permissions.Mode
		active     bool
		wantDenied bool
	}{
		{"write untouched file denied", "Write", `{"file_path":"` + other + `"}`, permissions.ModeDefault, true, true},
		{"write touched file allowed", "Write", `{"file_path":"` + touched + `"}`, permissions.ModeAcceptEdits, true, false},
		{"relative path resolves against cwd", "Edit", `{"file_path":"touched.go"}`, permissions.ModeAcceptEdits, true, false},
		{"relative untouched denied", "Edit", `{"file_path":"other.go"}`, permissions.ModeAcceptEdits, true, true},
		{"bypass mode still denied", "Write", `{"file_path":"` + other + `"}`, permissions.ModeBypassPermissions, true, true},
		{"missing path denied", "Write", `{}`, permissions.ModeBypassPermissions, true, true},
		{"notebook untouched denied", "NotebookEdit", `{"notebook_path":"` + other + `"}`, permissions.ModeBypassPermissions, true, true},
		{"inactive guard has no effect", "Write", `{"file_path":"` + other + `"}`, permissions.ModeBypassPermissions, false, false},
		{"bash redirect denied", "Bash", `{"command":"echo x > f"}`, permissions.ModeBypassPermissions, true, true},
		{"bash tests allowed", "Bash", `{"command":"go test ./..."}`, permissions.ModeBypassPermissions, true, false},
		{"bash redirect inactive allowed", "Bash", `{"command":"echo x > f"}`, permissions.ModeBypassPermissions, false, false},
		{"repl denied", "REPL", `{}`, permissions.ModeBypassPermissions, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := guardOn(tt.active)
			g.Record(touched)
			gate := permissions.New(dir, nil, tt.mode, nil, nil, nil)
			denied, text, runs := runGuarded(t, tt.tool, tt.input, LoopConfig{Gate: gate, Cwd: dir, ImplGuard: g})
			if denied != tt.wantDenied {
				t.Fatalf("denied = %v, want %v (result %q)", denied, tt.wantDenied, text)
			}
			if tt.wantDenied && runs != 0 {
				t.Errorf("denied tool ran %d times", runs)
			}
			if tt.wantDenied && !strings.Contains(text, "mcp.local-coder") {
				t.Errorf("denial should name the provider, got %q", text)
			}
		})
	}
}

func TestLoop_ImplGuardRecordAfterDenial(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "late.go")
	g := guardOn(true)
	cfg := LoopConfig{Gate: permissions.New(dir, nil, permissions.ModeBypassPermissions, nil, nil, nil), Cwd: dir, ImplGuard: g}
	input := `{"file_path":"` + p + `"}`
	if denied, _, _ := runGuarded(t, "Write", input, cfg); !denied {
		t.Fatal("write before Record should be denied")
	}
	g.Record(p)
	if denied, text, _ := runGuarded(t, "Write", input, cfg); denied {
		t.Fatalf("write after Record denied: %q", text)
	}
}

func TestLoop_ImplGuardBindsSubAgents(t *testing.T) {
	tests := []struct {
		name     string
		active   bool
		wantRuns int32
	}{
		{"active guard denies the sub-agent", true, 0},
		{"inactive guard lets it run", false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			ct := &execCountTool{fakeTool: fakeTool{name: "Write", result: "ran"}}
			reg := tool.NewRegistry()
			reg.Register(ct)
			input := `{"file_path":"` + filepath.Join(dir, "x.go") + `"}`
			lp, srv := newTestLoopWithConfig(t, []string{singleToolUseSSEWithInput("Write", "toolu_s", input), textOnlySSE("done")}, reg, LoopConfig{
				Gate:      permissions.New(dir, nil, permissions.ModeDefault, nil, nil, nil),
				Cwd:       dir,
				ImplGuard: guardOn(tt.active),
			})
			defer srv.Close()
			if _, err := lp.RunSubAgentTyped(context.Background(), "edit x.go", SubAgentSpec{Mode: permissions.ModeBypassPermissions}); err != nil {
				t.Fatalf("RunSubAgentTyped: %v", err)
			}
			if n := ct.runs.Load(); n != tt.wantRuns {
				t.Errorf("sub-agent Write ran %d times, want %d", n, tt.wantRuns)
			}
		})
	}
}

func TestImplGuardDenial_ToolAllowlist(t *testing.T) {
	dir := t.TempDir()
	g := guardOn(true)
	l := &Loop{cfg: LoopConfig{ImplGuard: g, Cwd: dir}}
	tests := []struct {
		name       string
		tool       string
		readOnly   bool
		input      map[string]any
		wantDenied bool
	}{
		{"read tool", "Read", true, nil, false},
		{"read-only mcp tool", "mcp__github__get_issue", true, nil, false},
		{"mcp write tool", "mcp__github__create_file", false, nil, true},
		{"raw local_implement", "mcp__local-coder__local_implement", false, nil, true},
		{"raw local_implement marked read-only", "mcp__local-coder__local_implement", true, nil, true},
		{"raw renamed implement tool", "mcp__qwen-router__qwen_implement", true, nil, true},
		{"EnterWorktree", "EnterWorktree", false, nil, true},
		{"ExitWorktree", "ExitWorktree", false, nil, true},
		{"SkillManage write_file", "SkillManage", false, map[string]any{"action": "write_file", "name": "x"}, true},
		{"SkillManage patch", "SkillManage", false, map[string]any{"action": "patch", "name": "x"}, true},
		{"SkillManage list", "SkillManage", true, map[string]any{"action": "list"}, false},
		{"REPL", "REPL", false, nil, true},
		{"Workflow run", "Workflow", false, nil, true},
		{"Config write", "Config", false, nil, true},
		{"unknown future tool", "Frobnicate", false, nil, true},
		{"LocalImplement", "LocalImplement", false, nil, false},
		{"CodingTask", "CodingTask", false, nil, false},
		{"Task sub-agent", "Task", false, nil, false},
		{"TodoWrite", "TodoWrite", false, nil, false},
		{"TaskCreate", "TaskCreate", false, nil, false},
		{"TaskStop", "TaskStop", false, nil, false},
		{"RecordDecision", "RecordDecision", false, nil, false},
		{"AskUserQuestion", "AskUserQuestion", true, nil, false},
		{"ExitPlanMode", "ExitPlanMode", true, nil, false},
		{"AstGrep search", "AstGrep", true, map[string]any{"pattern": "x"}, false},
		{"AstGrep dry run", "AstGrep", false, map[string]any{"pattern": "x", "rewrite": "y"}, false},
		{"AstGrep rewrite no paths", "AstGrep", false, map[string]any{"pattern": "x", "rewrite": "y", "dry_run": false}, true},
		{"Bash allowed", "Bash", true, map[string]any{"command": "go test ./..."}, false},
		{"Bash heredoc", "Bash", false, map[string]any{"command": "bash /dev/stdin <<EOF\ntouch f\nEOF"}, true},
		{"Shell redirect", "Shell", false, map[string]any{"command": "go test > out"}, true},
		{"Shell runner", "Shell", false, map[string]any{"command": "go test ./..."}, false},
		{"Write unrecorded", "Write", false, map[string]any{"file_path": filepath.Join(dir, "x.go")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTool{name: tt.tool, readOnly: tt.readOnly}
			raw, _ := json.Marshal(tt.input)
			msg := l.implGuardDenial(ft, tt.tool, raw, tt.input, dir)
			if (msg != "") != tt.wantDenied {
				t.Fatalf("denial = %q, wantDenied %v", msg, tt.wantDenied)
			}
			if tt.wantDenied && !strings.Contains(msg, "mcp.local-coder") {
				t.Errorf("denial should name the provider, got %q", msg)
			}
		})
	}
}

func TestImplGuardDenial_ConduitStorageAndKillTools(t *testing.T) {
	dir := t.TempDir()
	conduitDir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)
	l := &Loop{cfg: LoopConfig{ImplGuard: guardOn(true), Cwd: dir}}
	sanitized := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(dir)
	tests := []struct {
		name       string
		tool       string
		path       string
		wantDenied bool
	}{
		{"conduit project memory", "Write", filepath.Join(conduitDir, "projects", sanitized, "memory", "MEMORY.md"), false},
		{"conduit project memory edit", "Edit", filepath.Join(conduitDir, "projects", sanitized, "memory", "a.md"), false},
		{"other project's storage", "Write", filepath.Join(conduitDir, "projects", "other", "memory", "a.md"), true},
		{"conduit dir outside projects", "Write", filepath.Join(conduitDir, "settings.json"), true},
		{"repo file", "Write", filepath.Join(dir, "main.go"), true},
		{"repo file named like storage", "Write", filepath.Join(dir, "projects", sanitized, "x.md"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := map[string]any{"file_path": tt.path}
			raw, _ := json.Marshal(in)
			msg := l.implGuardDenial(&fakeTool{name: tt.tool}, tt.tool, raw, in, dir)
			if (msg != "") != tt.wantDenied {
				t.Fatalf("denial = %q, wantDenied %v", msg, tt.wantDenied)
			}
		})
	}
	for _, name := range []string{"KillShell", "KillBash"} {
		if msg := l.implGuardDenial(&fakeTool{name: name}, name, json.RawMessage(`{}`), map[string]any{}, dir); msg != "" {
			t.Errorf("%s denied: %q", name, msg)
		}
	}
}

func TestImplGuardDenial_StorageSymlinkIntoRepoDenied(t *testing.T) {
	dir := t.TempDir()
	conduitDir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)
	sanitized := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(dir)
	storage := filepath.Join(conduitDir, "projects", sanitized)
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(storage, "repo")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	l := &Loop{cfg: LoopConfig{ImplGuard: guardOn(true), Cwd: dir}}
	for path, wantDenied := range map[string]bool{
		filepath.Join(storage, "repo", "main.go"): true,
		filepath.Join(storage, "memory.md"):       false,
	} {
		in := map[string]any{"file_path": path}
		raw, _ := json.Marshal(in)
		msg := l.implGuardDenial(&fakeTool{name: "Write"}, "Write", raw, in, dir)
		if (msg != "") != wantDenied {
			t.Errorf("%s: denial = %q, wantDenied %v", path, msg, wantDenied)
		}
	}
}
