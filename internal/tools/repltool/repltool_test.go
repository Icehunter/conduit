package repltool

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/kernel"
	"github.com/icehunter/conduit/internal/tool"
)

func pythonAvailable() bool {
	if _, err := exec.LookPath("python3"); err == nil {
		return true
	}
	_, err := exec.LookPath("python")
	return err == nil
}

// resultText extracts the first text block from a tool.Result.
func resultText(r tool.Result) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

func TestReplTool_UsesKernelForPython(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	mgr := kernel.NewManager()
	defer mgr.DisposeSession("test-session")

	tl := NewWithKernelManager(mgr, "test-session")
	ctx := context.Background()

	// Call 1: set a variable via the kernel path.
	raw1, _ := json.Marshal(map[string]string{
		"code":     "x = 99",
		"language": "python",
	})
	res1, err := tl.Execute(ctx, raw1)
	if err != nil {
		t.Fatalf("Execute #1: %v", err)
	}
	if res1.IsError {
		t.Fatalf("Execute #1 returned error result: %s", resultText(res1))
	}

	// Call 2: read the variable — should still be in scope.
	raw2, _ := json.Marshal(map[string]string{
		"code":     "print(x)",
		"language": "python",
	})
	res2, err := tl.Execute(ctx, raw2)
	if err != nil {
		t.Fatalf("Execute #2: %v", err)
	}
	if res2.IsError {
		t.Fatalf("Execute #2 returned error result: %s", resultText(res2))
	}
	if !strings.Contains(resultText(res2), "99") {
		t.Errorf("expected '99' in output, got %q", resultText(res2))
	}
}

func TestReplTool_FallsThroughForBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	mgr := kernel.NewManager()
	defer mgr.DisposeSession("test-session-bash")

	tl := NewWithKernelManager(mgr, "test-session-bash")
	ctx := context.Background()

	raw, _ := json.Marshal(map[string]string{
		"code":     "echo hello",
		"language": "bash",
	})
	res, err := tl.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute bash: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute bash returned error result: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "hello") {
		t.Errorf("expected 'hello' in bash output, got %q", resultText(res))
	}
}

func TestReplTool_NoManager_SubprocessPath(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	tl := New() // no manager
	ctx := context.Background()

	raw, _ := json.Marshal(map[string]string{
		"code":     "print('subprocess')",
		"language": "python",
	})
	res, err := tl.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned error result: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "subprocess") {
		t.Errorf("expected 'subprocess' in output, got %q", resultText(res))
	}
}

func TestKernelKey(t *testing.T) {
	tl := NewWithKernelManager(kernel.NewManager(), "sess")
	tests := []struct{ scope, want string }{
		{"", "sess"},
		{"sub-1", "sess/sub-1"},
	}
	for _, tt := range tests {
		if got := tl.kernelKey(tt.scope); got != tt.want {
			t.Errorf("kernelKey(%q) = %q, want %q", tt.scope, got, tt.want)
		}
	}
}

// A sub-agent's kernel is its own: variables do not cross between parent and
// child, and releasing the child's scope leaves the parent's kernel alone.
func TestReplTool_PerAgentKernels(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	mgr := kernel.NewManager()
	defer mgr.DisposeSession("sess")
	tl := NewWithKernelManager(mgr, "sess")

	run := func(ctx context.Context, code string) tool.Result {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"code": code, "language": "python"})
		res, err := tl.Execute(ctx, raw)
		if err != nil {
			t.Fatalf("Execute(%q): %v", code, err)
		}
		return res
	}
	root := context.Background()
	child := tool.WithScope(root, "sub-1")

	run(root, "who = 'parent'")
	run(child, "who = 'child'")
	if got := resultText(run(root, "print(who)")); !strings.Contains(got, "parent") {
		t.Errorf("parent saw %q, want its own variable", got)
	}
	if got := resultText(run(child, "print(who)")); !strings.Contains(got, "child") {
		t.Errorf("child saw %q, want its own variable", got)
	}

	tl.ReleaseScope("sub-1")
	// A released scope starts from a fresh interpreter: its variable is gone.
	if res := run(child, "print(who)"); !strings.Contains(resultText(res), "NameError") {
		t.Errorf("child state survived release: %q", resultText(res))
	}
	if got := resultText(run(root, "print(who)")); !strings.Contains(got, "parent") {
		t.Errorf("parent kernel lost after releasing the child: %q", got)
	}
	tl.ReleaseScope("sub-1") // idempotent
	New().ReleaseScope("x")  // no manager: no-op
}

func TestReplTool_HonorsContextCwd(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := tool.WithCwd(context.Background(), dir)
	raw, _ := json.Marshal(map[string]string{"code": "import os; print(os.getcwd())", "language": "python"})

	mgr := kernel.NewManager()
	defer mgr.DisposeSession("cwd-session")
	for name, tl := range map[string]*Tool{"kernel": NewWithKernelManager(mgr, "cwd-session"), "oneshot": New()} {
		res, err := tl.Execute(ctx, raw)
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %s", name, err, resultText(res))
		}
		got, _ := filepath.EvalSymlinks(strings.TrimSpace(resultText(res)))
		if got != dir {
			t.Errorf("%s cwd = %q, want %q", name, got, dir)
		}
	}
}
