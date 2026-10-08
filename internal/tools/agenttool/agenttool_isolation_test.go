package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/tool"
)

func gitFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "commit.gpgsign")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func isolatedCall(t *testing.T, dir string, run func(ctx context.Context, prompt string) (string, error)) tool.Result {
	t.Helper()
	raw, _ := json.Marshal(Input{Prompt: "go", Isolation: "worktree"})
	res, err := New(run, nil, nil).Execute(tool.WithCwd(context.Background(), dir), raw)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func resultText(r tool.Result) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestIsolation_UnchangedRemoved(t *testing.T) {
	dir := gitFixture(t)
	var wt string
	res := isolatedCall(t, dir, func(ctx context.Context, _ string) (string, error) {
		wt = tool.IsolatedCwd(ctx)
		return "done", nil
	})
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	if wt == "" || !strings.HasPrefix(wt, dir) {
		t.Fatalf("child ctx cwd = %q, want a path under %s", wt, dir)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("unchanged worktree should be removed, stat err = %v", err)
	}
	if got := resultText(res); got != "done" {
		t.Errorf("result = %q, want plain child output", got)
	}
}

func TestIsolation_ChangedKept(t *testing.T) {
	dir := gitFixture(t)
	var wt string
	res := isolatedCall(t, dir, func(ctx context.Context, _ string) (string, error) {
		wt = tool.IsolatedCwd(ctx)
		return "edited", os.WriteFile(filepath.Join(wt, "x.txt"), []byte("hi"), 0o600)
	})
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("changed worktree should be kept: %v", err)
	}
	if got := resultText(res); !strings.Contains(got, wt) || !strings.Contains(got, "kept") {
		t.Errorf("result should report kept path %s, got %q", wt, got)
	}
}

func TestIsolation_CommittedKept(t *testing.T) {
	dir := gitFixture(t)
	var wt string
	res := isolatedCall(t, dir, func(ctx context.Context, _ string) (string, error) {
		wt = tool.IsolatedCwd(ctx)
		out, err := exec.Command("git", "-C", wt, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c").CombinedOutput()
		if err != nil {
			t.Errorf("commit: %v\n%s", err, out)
		}
		return "committed", nil
	})
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree with new commits must be kept: %v", err)
	}
	if !strings.Contains(resultText(res), wt) {
		t.Errorf("result should report path, got %q", resultText(res))
	}
}

func TestIsolation_NonGitErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	called := false
	res := isolatedCall(t, t.TempDir(), func(context.Context, string) (string, error) {
		called = true
		return "x", nil
	})
	if !res.IsError || !strings.Contains(resultText(res), "git repository") {
		t.Errorf("want git repository error, got %+v", res)
	}
	if called {
		t.Error("sub-agent must not run without a worktree")
	}
}

func TestIsolation_ConcurrentUniquePaths(t *testing.T) {
	dir := gitFixture(t)
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := isolatedCall(t, dir, func(ctx context.Context, _ string) (string, error) {
				mu.Lock()
				seen[tool.IsolatedCwd(ctx)] = true
				mu.Unlock()
				return "ok", nil
			})
			if res.IsError {
				t.Errorf("error: %s", resultText(res))
			}
		}()
	}
	wg.Wait()
	if len(seen) != 4 {
		t.Errorf("want 4 distinct worktrees, got %d", len(seen))
	}
}

func TestIsolation_InvalidValueAndSchema(t *testing.T) {
	at := New(runAgent, nil, nil)
	raw, _ := json.Marshal(Input{Prompt: "p", Isolation: "docker"})
	res, _ := at.Execute(context.Background(), raw)
	if !res.IsError {
		t.Error("unknown isolation value should error")
	}
	if !strings.Contains(string(at.InputSchema()), `"isolation"`) {
		t.Error("schema must include isolation")
	}
}

func TestIsolation_ExcludedFromMainCheckout(t *testing.T) {
	dir := gitFixture(t)
	isolatedCall(t, dir, func(ctx context.Context, _ string) (string, error) {
		return "x", os.WriteFile(filepath.Join(tool.IsolatedCwd(ctx), "f.txt"), []byte("1"), 0o600)
	})
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("main checkout must stay clean with a kept worktree, got %q", out)
	}
}

func TestIsolation_NestedUsesMainRepo(t *testing.T) {
	dir := gitFixture(t)
	var inner string
	isolatedCall(t, dir, func(ctx context.Context, _ string) (string, error) {
		outer := tool.IsolatedCwd(ctx)
		// Keep the outer worktree so the nested call runs from inside it.
		if err := os.WriteFile(filepath.Join(outer, "f"), []byte("1"), 0o600); err != nil {
			return "", err
		}
		isolatedCall(t, outer, func(ctx context.Context, _ string) (string, error) {
			inner = tool.IsolatedCwd(ctx)
			return "ok", nil
		})
		return "ok", nil
	})
	want := filepath.Join(dir, ".conduit", "worktrees")
	if filepath.Dir(inner) != want {
		t.Errorf("nested worktree %q should live under %s", inner, want)
	}
}
