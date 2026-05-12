// Package healthcheck provides pre-flight diagnostics that run at session start.
// Checks for common issues: uncommitted git changes, failing tests, outdated deps.
// Results are advisory and injected as additionalContext into the first turn.
package healthcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Result holds the outcome of all health checks.
type Result struct {
	Warnings []string // human-readable warnings
	GitInfo  *GitInfo // nil if not in a git repo
	HasIssue bool     // true if any check found problems
}

// GitInfo contains git repository state.
type GitInfo struct {
	Branch           string
	UncommittedFiles int
	UntrackedFiles   int
	HasRemote        bool
	Ahead            int
	Behind           int
}

// DefaultTimeout for all health checks combined.
const DefaultTimeout = 5 * time.Second

// Run executes all health checks in parallel with the given timeout.
// Returns collected warnings suitable for injection into conversation context.
func Run(ctx context.Context, cwd string, timeout time.Duration) *Result {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := &Result{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Git check
	wg.Add(1)
	go func() {
		defer wg.Done()
		gitInfo, warnings := checkGit(ctx, cwd)
		mu.Lock()
		result.GitInfo = gitInfo
		result.Warnings = append(result.Warnings, warnings...)
		if len(warnings) > 0 {
			result.HasIssue = true
		}
		mu.Unlock()
	}()

	// Dependency check
	wg.Add(1)
	go func() {
		defer wg.Done()
		warnings := checkDeps(ctx, cwd)
		mu.Lock()
		result.Warnings = append(result.Warnings, warnings...)
		if len(warnings) > 0 {
			result.HasIssue = true
		}
		mu.Unlock()
	}()

	wg.Wait()
	return result
}

// FormatContext returns the warnings as a string suitable for additionalContext.
func (r *Result) FormatContext() string {
	if len(r.Warnings) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("⚠️ Pre-flight health check detected issues:\n")
	for _, w := range r.Warnings {
		sb.WriteString("• ")
		sb.WriteString(w)
		sb.WriteString("\n")
	}
	sb.WriteString("\nConsider addressing these before starting work.")
	return sb.String()
}

// checkGit checks git repository state.
func checkGit(ctx context.Context, cwd string) (*GitInfo, []string) {
	var warnings []string

	// Check if we're in a git repo
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = cwd
	if err := cmd.Run(); err != nil {
		return nil, nil // Not a git repo, skip
	}

	info := &GitInfo{}

	// Get current branch
	cmd = exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.Dir = cwd
	if out, err := cmd.Output(); err == nil {
		info.Branch = strings.TrimSpace(string(out))
	}

	// Count uncommitted changes (staged + unstaged)
	cmd = exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = cwd
	if out, err := cmd.Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, line := range lines {
			if len(line) < 2 {
				continue
			}
			// First two chars are status codes
			// '?' means untracked, anything else is uncommitted
			if line[0] == '?' && line[1] == '?' {
				info.UntrackedFiles++
			} else {
				info.UncommittedFiles++
			}
		}
	}

	// Check remote tracking
	cmd = exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "@{upstream}")
	cmd.Dir = cwd
	if err := cmd.Run(); err == nil {
		info.HasRemote = true

		// Get ahead/behind counts
		cmd = exec.CommandContext(ctx, "git", "rev-list", "--left-right", "--count", "@{upstream}...HEAD")
		cmd.Dir = cwd
		if out, err := cmd.Output(); err == nil {
			parts := strings.Fields(strings.TrimSpace(string(out)))
			if len(parts) == 2 {
				_, _ = fmt.Sscanf(parts[0], "%d", &info.Behind)
				_, _ = fmt.Sscanf(parts[1], "%d", &info.Ahead)
			}
		}
	}

	// Generate warnings
	if info.UncommittedFiles > 0 {
		warnings = append(warnings, fmt.Sprintf("Git: %d uncommitted changes", info.UncommittedFiles))
	}
	if info.Ahead > 0 {
		warnings = append(warnings, fmt.Sprintf("Git: %d commits ahead of remote (unpushed)", info.Ahead))
	}
	if info.Behind > 0 {
		warnings = append(warnings, fmt.Sprintf("Git: %d commits behind remote (pull needed)", info.Behind))
	}

	return info, warnings
}

// checkDeps checks for dependency issues.
func checkDeps(ctx context.Context, cwd string) []string {
	var warnings []string
	var mu sync.Mutex
	var wg sync.WaitGroup

	// npm audit (if package.json exists)
	if fileExists(filepath.Join(cwd, "package.json")) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := checkNpmAudit(ctx, cwd)
			mu.Lock()
			warnings = append(warnings, w...)
			mu.Unlock()
		}()
	}

	// go mod verify (if go.mod exists)
	if fileExists(filepath.Join(cwd, "go.mod")) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := checkGoMod(ctx, cwd)
			mu.Lock()
			warnings = append(warnings, w...)
			mu.Unlock()
		}()
	}

	wg.Wait()
	return warnings
}

func checkNpmAudit(ctx context.Context, cwd string) []string {
	// npm audit returns non-zero if vulnerabilities found
	cmd := exec.CommandContext(ctx, "npm", "audit", "--json")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err == nil {
		return nil // No vulnerabilities
	}

	// Parse output for vulnerability count
	outStr := string(out)
	if strings.Contains(outStr, `"vulnerabilities"`) {
		// Count severity levels
		high := strings.Count(outStr, `"severity":"high"`)
		critical := strings.Count(outStr, `"severity":"critical"`)
		total := high + critical
		if total > 0 {
			return []string{fmt.Sprintf("npm: %d high/critical vulnerabilities found", total)}
		}
	}
	return nil
}

func checkGoMod(ctx context.Context, cwd string) []string {
	// go mod verify checks module checksums
	cmd := exec.CommandContext(ctx, "go", "mod", "verify")
	cmd.Dir = cwd
	if err := cmd.Run(); err != nil {
		return []string{"Go: module verification failed (run 'go mod verify')"}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
