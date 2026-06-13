package repltool

import (
	"context"
	"encoding/json"
	"os/exec"
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
