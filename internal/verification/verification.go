// Package verification runs a frozen, explicitly authorized repository policy.
// Verification results describe process outcomes, never human acceptance.
package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/procgroup"
	"github.com/icehunter/conduit/internal/rtk"
)

type Status string

const (
	Passed    Status = "passed"
	Failed    Status = "failed"
	Blocked   Status = "blocked"
	Cancelled Status = "cancelled"
	TimedOut  Status = "timed_out"
	Changed   Status = "workspace_changed"
	Skipped   Status = "skipped"
)

type Check struct {
	ID             string `json:"id"`
	Command        string `json:"command"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

type Policy struct {
	Enabled             bool    `json:"enabled"`
	Checks              []Check `json:"checks"`
	MaxRepairAttempts   int     `json:"maxRepairAttempts,omitempty"`
	TotalTimeoutSeconds int     `json:"totalTimeoutSeconds,omitempty"`
}

type CheckResult struct {
	CheckID     string `json:"checkId"`
	CommandHash string `json:"commandHash"`
	Status      Status `json:"status"`
	ExitCode    int    `json:"exitCode"`
	DurationMS  int64  `json:"durationMs"`
	Output      string `json:"output,omitempty"`
	OutputHash  string `json:"outputHash,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

type Report struct {
	Status          Status        `json:"status"`
	PolicyHash      string        `json:"policyHash"`
	WorkspaceBefore string        `json:"workspaceBefore,omitempty"`
	WorkspaceAfter  string        `json:"workspaceAfter,omitempty"`
	Checks          []CheckResult `json:"checks,omitempty"`
}

// Options contains host-side authority. A nil Authorize denies every command.
// Authorization happens for every check, even when a previous attempt passed.
type Options struct {
	Authorize        func(context.Context, Check) error
	Env              []string
	OutputLimitBytes int
	// ProtectedPaths are repository-relative doublestar globs for immutable
	// test/check definitions. Built-in policy and build manifests always apply.
	ProtectedPaths []string
}

// Runner owns an immutable policy. Construct a fresh Runner to accept changes.
type Runner struct {
	root          string
	policy        Policy
	policyHash    string
	opts          Options
	protectedHash string
}

func NewRunner(root string, policy Policy, opts Options) (*Runner, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("verification root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("verification root: %w", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("verification root is not a directory: %s", root)
	}
	policy.Checks = append([]Check(nil), policy.Checks...)
	if err := Validate(policy); err != nil {
		return nil, err
	}
	if policy.TotalTimeoutSeconds == 0 {
		policy.TotalTimeoutSeconds = 600
	}
	for i := range policy.Checks {
		if policy.Checks[i].TimeoutSeconds == 0 {
			policy.Checks[i].TimeoutSeconds = 120
		}
		if _, err := checkDir(root, policy.Checks[i].Cwd); err != nil {
			return nil, err
		}
	}
	opts.Env = append([]string(nil), opts.Env...)
	opts.ProtectedPaths = append(defaultProtectedPaths(), opts.ProtectedPaths...)
	if err := validateProtectedPaths(opts.ProtectedPaths); err != nil {
		return nil, err
	}
	if opts.OutputLimitBytes <= 0 {
		opts.OutputLimitBytes = 30000
	}
	if opts.OutputLimitBytes > 1<<20 {
		opts.OutputLimitBytes = 1 << 20
	}
	b, err := json.Marshal(struct {
		Policy         Policy
		ProtectedPaths []string
	}{policy, opts.ProtectedPaths})
	if err != nil {
		return nil, fmt.Errorf("verification policy: %w", err)
	}
	r := &Runner{root: root, policy: policy, policyHash: digest(b), opts: opts}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r.protectedHash, err = r.protectedFingerprint(ctx)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Validate checks policy semantics without executing commands or inspecting disk.
func Validate(p Policy) error {
	if p.MaxRepairAttempts < 0 || p.MaxRepairAttempts > 10 {
		return errors.New("verification maxRepairAttempts must be between 0 and 10")
	}
	if p.TotalTimeoutSeconds < 0 || p.TotalTimeoutSeconds > 86400 {
		return errors.New("verification totalTimeoutSeconds must be between 0 and 86400")
	}
	if p.Enabled && len(p.Checks) == 0 {
		return errors.New("enabled verification policy requires checks")
	}
	if len(p.Checks) > 100 {
		return errors.New("verification policy exceeds 100 checks")
	}
	ids := make(map[string]bool, len(p.Checks))
	for _, c := range p.Checks {
		if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Command) == "" {
			return errors.New("verification checks require id and command")
		}
		if ids[c.ID] {
			return fmt.Errorf("duplicate verification check %q", c.ID)
		}
		ids[c.ID] = true
		if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 86400 {
			return fmt.Errorf("check %q timeoutSeconds must be between 0 and 86400", c.ID)
		}
		if filepath.IsAbs(c.Cwd) || c.Cwd == ".." || strings.HasPrefix(filepath.Clean(c.Cwd), ".."+string(filepath.Separator)) {
			return fmt.Errorf("check %q cwd escapes repository", c.ID)
		}
	}
	return nil
}

func (r *Runner) Root() string       { return r.root }
func (r *Runner) PolicyHash() string { return r.policyHash }

// Run executes foreground checks in policy order and stops at the first failure.
// Any workspace change during checks invalidates even otherwise successful runs.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	out := Report{PolicyHash: r.policyHash, Status: Skipped}
	if err := ctx.Err(); err != nil {
		out.Status = Cancelled
		return out, err
	}
	if !r.policy.Enabled {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.policy.TotalTimeoutSeconds)*time.Second)
	defer cancel()
	if err := r.CheckProtected(ctx); err != nil {
		out.Status = Blocked
		return out, err
	}
	var err error
	out.WorkspaceBefore, err = WorkspaceFingerprint(ctx, r.root)
	if err != nil {
		out.Status = Blocked
		return out, err
	}
	out.Status = Passed
	for _, c := range r.policy.Checks {
		result := r.runCheck(ctx, c)
		out.Checks = append(out.Checks, result)
		if result.Status != Passed {
			out.Status = result.Status
			break
		}
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			out.Status = TimedOut
		} else {
			out.Status = Cancelled
		}
		return out, err
	}
	out.WorkspaceAfter, err = WorkspaceFingerprint(ctx, r.root)
	if err != nil {
		out.Status = Blocked
		return out, err
	}
	if out.Status == Passed && out.WorkspaceBefore != out.WorkspaceAfter {
		out.Status = Changed
	}
	if err := r.CheckProtected(ctx); err != nil {
		out.Status = Blocked
		return out, err
	}
	return out, nil
}

func (r *Runner) runCheck(ctx context.Context, c Check) CheckResult {
	start := time.Now()
	result := CheckResult{CheckID: c.ID, CommandHash: digest([]byte(c.Command)), Status: Blocked, ExitCode: -1}
	finish := func() CheckResult { result.DurationMS = time.Since(start).Milliseconds(); return result }
	if r.opts.Authorize == nil {
		result.Output = "Verification command was not authorized."
		return finish()
	}
	if err := r.opts.Authorize(ctx, c); err != nil {
		result.Output, _ = rtk.RedactSecrets(err.Error())
		return finish()
	}
	// Recheck symlinks after authorization so a replaced directory cannot escape.
	dir, err := checkDir(r.root, c.Cwd)
	if err != nil {
		result.Output = err.Error()
		return finish()
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := shellCommand(cctx, c.Command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), r.opts.Env...)
	procgroup.Set(cmd)
	cmd.WaitDelay = time.Second
	capture := newCapture(r.opts.OutputLimitBytes)
	cmd.Stdout, cmd.Stderr = capture, capture
	err = cmd.Run()
	cleanupShellProcess(cmd)
	result.Output, result.OutputHash, result.Truncated = capture.result()
	result.Status = Passed
	result.ExitCode = 0
	switch {
	case ctx.Err() != nil:
		result.Status = Cancelled
		result.ExitCode = -1
	case cctx.Err() != nil:
		result.Status = TimedOut
		result.ExitCode = -1
	case err != nil:
		result.Status = Failed
		result.ExitCode = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.ExitCode = exit.ExitCode()
		} else {
			result.Status = Blocked
			result.Output += "\n" + err.Error()
		}
	}
	result.Output, _ = rtk.RedactSecrets(result.Output)
	return finish()
}

func checkDir(root, relative string) (string, error) {
	dir, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return "", fmt.Errorf("verification cwd: %w", err)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("verification cwd escapes repository")
	}
	return dir, nil
}

func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
