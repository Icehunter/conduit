package healthcheck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_NotGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	result := Run(context.Background(), dir, time.Second)
	if result.GitInfo != nil {
		t.Error("expected nil GitInfo for non-git directory")
	}
	if result.HasIssue {
		t.Error("expected no issues for empty directory")
	}
}

func TestRun_GitRepoClean(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Initialize a clean git repo
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Create and commit a file
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")

	result := Run(context.Background(), dir, time.Second)
	if result.GitInfo == nil {
		t.Fatal("expected GitInfo for git repo")
	}
	if result.GitInfo.UncommittedFiles != 0 {
		t.Errorf("expected 0 uncommitted files, got %d", result.GitInfo.UncommittedFiles)
	}
	if result.HasIssue {
		t.Error("expected no issues for clean repo")
	}
}

func TestRun_GitRepoUncommitted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Initialize a git repo
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Create and commit a file
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")

	// Create uncommitted change
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Modified"), 0644); err != nil {
		t.Fatal(err)
	}

	result := Run(context.Background(), dir, time.Second)
	if result.GitInfo == nil {
		t.Fatal("expected GitInfo for git repo")
	}
	if result.GitInfo.UncommittedFiles != 1 {
		t.Errorf("expected 1 uncommitted file, got %d", result.GitInfo.UncommittedFiles)
	}
	if !result.HasIssue {
		t.Error("expected issue for uncommitted changes")
	}

	// Check warning message
	foundWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "uncommitted") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Error("expected warning about uncommitted changes")
	}
}

func TestFormatContext_Empty(t *testing.T) {
	t.Parallel()
	result := &Result{}
	if ctx := result.FormatContext(); ctx != "" {
		t.Errorf("expected empty context, got %q", ctx)
	}
}

func TestFormatContext_WithWarnings(t *testing.T) {
	t.Parallel()
	result := &Result{
		Warnings: []string{"Git: 3 uncommitted changes", "npm: 2 vulnerabilities"},
	}
	ctx := result.FormatContext()
	if !strings.Contains(ctx, "uncommitted") {
		t.Error("expected context to contain 'uncommitted'")
	}
	if !strings.Contains(ctx, "vulnerabilities") {
		t.Error("expected context to contain 'vulnerabilities'")
	}
	if !strings.Contains(ctx, "⚠️") {
		t.Error("expected context to contain warning emoji")
	}
}

func TestRun_Timeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Run with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	// Should return quickly without panicking
	result := Run(ctx, dir, time.Nanosecond)
	// Result may or may not have issues depending on timing, but shouldn't panic
	_ = result
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
