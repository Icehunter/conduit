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
		{"bash redirect reaches the gate", "Bash", `{"command":"echo x > f"}`, permissions.ModeBypassPermissions, true, false},
		{"bash git commit reaches the gate", "Bash", `{"command":"git commit -m x"}`, permissions.ModeBypassPermissions, true, false},
		{"bash heredoc reaches the gate", "Bash", `{"command":"cat > f <<EOF\nx\nEOF"}`, permissions.ModeBypassPermissions, true, false},
		{"bash mkdir reaches the gate", "Bash", `{"command":"mkdir -p d"}`, permissions.ModeBypassPermissions, true, false},
		{"bash gofmt -w reaches the gate", "Bash", `{"command":"gofmt -w ."}`, permissions.ModeBypassPermissions, true, false},
		{"bash inactive allowed", "Bash", `{"command":"echo x > f"}`, permissions.ModeBypassPermissions, false, false},
		{"repl reaches the gate", "REPL", `{}`, permissions.ModeBypassPermissions, true, false},
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

func TestImplGuardDenial_Tools(t *testing.T) {
	dir := t.TempDir()
	l := &Loop{cfg: LoopConfig{ImplGuard: guardOn(true), Cwd: dir}}
	tests := []struct {
		name       string
		tool       string
		input      map[string]any
		wantDenied bool
	}{
		{"read tool", "Read", nil, false},
		{"mcp tool without readOnlyHint", "mcp__github__create_file", nil, false},
		{"raw local_implement", "mcp__local-coder__local_implement", nil, true},
		{"raw renamed implement tool", "mcp__qwen-router__qwen_implement", nil, true},
		{"EnterWorktree", "EnterWorktree", nil, false},
		{"SkillManage write_file", "SkillManage", map[string]any{"action": "write_file", "name": "x"}, false},
		{"REPL", "REPL", nil, false},
		{"unknown future tool", "Frobnicate", nil, false},
		{"LocalImplement", "LocalImplement", nil, false},
		{"CodingTask", "CodingTask", nil, false},
		{"Task sub-agent", "Task", nil, false},
		{"AstGrep search", "AstGrep", map[string]any{"pattern": "x"}, false},
		{"AstGrep dry run", "AstGrep", map[string]any{"pattern": "x", "rewrite": "y"}, false},
		{"AstGrep rewrite no paths", "AstGrep", map[string]any{"pattern": "x", "rewrite": "y", "dry_run": false}, true},
		{"Bash git commit", "Bash", map[string]any{"command": "git commit -am x"}, false},
		{"Bash heredoc", "Bash", map[string]any{"command": "bash /dev/stdin <<EOF\ntouch f\nEOF"}, false},
		{"Bash env prefix", "Bash", map[string]any{"command": "FOO=1 make build"}, false},
		{"Shell redirect", "Shell", map[string]any{"command": "go test > out"}, false},
		{"Write unrecorded", "Write", map[string]any{"file_path": filepath.Join(dir, "x.go")}, true},
		{"Edit unrecorded", "Edit", map[string]any{"file_path": filepath.Join(dir, "x.go")}, true},
		{"MultiEdit unrecorded", "MultiEdit", map[string]any{"file_path": filepath.Join(dir, "x.go")}, true},
		{"HashEdit unrecorded", "HashEdit", map[string]any{"file_path": filepath.Join(dir, "x.go")}, true},
		{"NotebookEdit unrecorded", "NotebookEdit", map[string]any{"notebook_path": filepath.Join(dir, "x.ipynb")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := l.implGuardDenial(tt.tool, tt.input, dir)
			if (msg != "") != tt.wantDenied {
				t.Fatalf("denial = %q, wantDenied %v", msg, tt.wantDenied)
			}
			if tt.wantDenied && !strings.Contains(msg, "mcp.local-coder") {
				t.Errorf("denial should name the provider, got %q", msg)
			}
		})
	}
}

// TestImplGuard_AvailabilityTransitions drives one guard through connected,
// dropped and reconnected states, as an MCP server does mid-session.
func TestImplGuard_AvailabilityTransitions(t *testing.T) {
	dir := t.TempDir()
	var up atomic.Bool
	g := implguard.NewStatus(func() implguard.Status {
		if up.Load() {
			return implguard.Status{Active: true, Label: "mcp.local-coder"}
		}
		return implguard.Status{Label: "mcp.local-coder", Unreachable: true}
	})
	l := &Loop{cfg: LoopConfig{ImplGuard: g, Cwd: dir}}
	edit := map[string]any{"file_path": filepath.Join(dir, "x.go")}
	steps := []struct {
		name       string
		up         bool
		wantDenied bool
		wantNote   bool
	}{
		{"connected denies untouched edit", true, true, false},
		{"dropped: edit proceeds with a notice", false, false, true},
		{"still dropped: no second notice", false, false, false},
		{"reconnected denies again", true, true, false},
		{"dropped again: notice again", false, false, true},
		{"still dropped: quiet", false, false, false},
	}
	for _, st := range steps {
		up.Store(st.up)
		msg := l.implGuardDenial("Edit", edit, dir)
		if (msg != "") != st.wantDenied {
			t.Fatalf("%s: denial = %q, wantDenied %v", st.name, msg, st.wantDenied)
		}
		note := l.implGuardFallbackNote("Edit", edit, dir)
		if (note != "") != st.wantNote {
			t.Fatalf("%s: note = %q, wantNote %v", st.name, note, st.wantNote)
		}
	}
	// A non-edit call never consumes the notice.
	up.Store(false)
	l.implGuardDenial("Bash", map[string]any{"command": "git status"}, dir)
	if n := l.implGuardFallbackNote("Bash", map[string]any{"command": "git status"}, dir); n != "" {
		t.Errorf("Bash consumed the notice: %q", n)
	}
}

func TestLoop_ImplGuardFallbackNoticeInResult(t *testing.T) {
	dir := t.TempDir()
	g := implguard.NewStatus(func() implguard.Status {
		return implguard.Status{Label: "mcp.local-coder", Unreachable: true}
	})
	cfg := LoopConfig{Gate: permissions.New(dir, nil, permissions.ModeBypassPermissions, nil, nil, nil), Cwd: dir, ImplGuard: g}
	input := `{"file_path":"` + filepath.Join(dir, "x.go") + `"}`
	_, first, runs := runGuarded(t, "Write", input, cfg)
	if runs != 1 || !strings.Contains(first, implguard.FallbackNotice) {
		t.Fatalf("first edit: runs = %d, result %q; want it to run with the fallback notice", runs, first)
	}
	_, second, _ := runGuarded(t, "Write", input, cfg)
	if strings.Contains(second, implguard.FallbackNotice) {
		t.Fatalf("second edit repeated the notice: %q", second)
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
			msg := l.implGuardDenial(tt.tool, in, dir)
			if (msg != "") != tt.wantDenied {
				t.Fatalf("denial = %q, wantDenied %v", msg, tt.wantDenied)
			}
		})
	}
	for _, name := range []string{"KillShell", "KillBash"} {
		if msg := l.implGuardDenial(name, map[string]any{}, dir); msg != "" {
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
		msg := l.implGuardDenial("Write", in, dir)
		if (msg != "") != wantDenied {
			t.Errorf("%s: denial = %q, wantDenied %v", path, msg, wantDenied)
		}
	}
}
