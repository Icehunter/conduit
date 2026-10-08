// Package evaluation runs explicit held-out tasks in disposable local Git
// checkouts. This isolates repository state, not arbitrary code at the OS level;
// checks still require host authorization and callbacks must honor cancellation.
package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/episodes"
	"github.com/icehunter/conduit/internal/procgroup"
	"github.com/icehunter/conduit/internal/verification"
)

type Budget struct {
	TimeoutSeconds int `json:"timeout_seconds"`
	MaxTurns       int `json:"max_turns"`
	MaxTokens      int `json:"max_tokens"`
}

type Task struct {
	Version        int                 `json:"version"`
	ID             string              `json:"id"`
	RepoPath       string              `json:"repo_path"`
	Commit         string              `json:"commit"`
	Prompt         string              `json:"prompt"`
	ModelRevision  string              `json:"model_revision"`
	Budget         Budget              `json:"budget"`
	Policy         verification.Policy `json:"policy"`
	ProtectedPaths []string            `json:"protected_paths"`
}

type Workspace struct {
	Root          string
	Task          Task
	Origin        string
	DisableMemory bool
	// Environment replaces inherited shell environments when used by a host.
	Environment []string
}

type Usage struct {
	Turns         int    `json:"turns"`
	Tokens        int    `json:"tokens"`
	ModelRevision string `json:"model_revision"`
}

type Options struct {
	Implement func(context.Context, Workspace) (Usage, error)
	Authorize func(context.Context, verification.Check) error
}

type Result struct {
	TaskID         string              `json:"task_id"`
	Origin         string              `json:"origin"`
	ManifestDigest string              `json:"manifest_digest"`
	ModelRevision  string              `json:"model_revision"`
	Status         string              `json:"status"`
	Usage          Usage               `json:"usage"`
	Verification   verification.Report `json:"verification"`
	DurationMS     int64               `json:"duration_ms"`
}

var commitPattern = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)

func Validate(t Task) error {
	if t.Version != 1 || t.ID == "" || t.Prompt == "" || t.RepoPath == "" {
		return errors.New("evaluation: version 1, task ID, prompt and local repository are required")
	}
	if !commitPattern.MatchString(t.Commit) {
		return errors.New("evaluation: fixture commit must be a full object ID")
	}
	if t.ModelRevision == "" || t.ModelRevision == "latest" || t.ModelRevision == "main" || t.ModelRevision == "master" {
		return errors.New("evaluation: immutable model revision required")
	}
	if t.Budget.TimeoutSeconds <= 0 || t.Budget.TimeoutSeconds > 86400 || t.Budget.MaxTurns <= 0 || t.Budget.MaxTokens <= 0 {
		return errors.New("evaluation: positive bounded time, turn and token budgets required")
	}
	if !t.Policy.Enabled {
		return errors.New("evaluation: verification must be enabled")
	}
	if err := verification.Validate(t.Policy); err != nil {
		return fmt.Errorf("evaluation: policy: %w", err)
	}
	if len(t.ProtectedPaths) == 0 {
		return errors.New("evaluation: protected check definitions are required")
	}
	for _, path := range t.ProtectedPaths {
		clean := filepath.Clean(path)
		if path == "" || clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return errors.New("evaluation: protected path must be repository-relative")
		}
	}
	return nil
}

// LoadManifest decodes one pinned task. Unknown fields are rejected to prevent a
// misspelled budget or check property from silently weakening evaluation.
func LoadManifest(path string) (Task, error) {
	f, err := os.Open(path)
	if err != nil {
		return Task{}, fmt.Errorf("evaluation: manifest open: %w", err)
	}
	defer f.Close()
	var task Task
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&task); err != nil {
		return task, fmt.Errorf("evaluation: manifest decode: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return task, errors.New("evaluation: manifest must contain exactly one JSON object")
	}
	return task, Validate(task)
}

// Run invokes only explicit callbacks and authorized checks. It creates no
// training recorder; callers must preserve Workspace.Origin in any telemetry.
func Run(ctx context.Context, task Task, opts Options) (out Result, err error) {
	started := time.Now()
	out = Result{TaskID: task.ID, Origin: episodes.OriginEvaluation, ModelRevision: task.ModelRevision, Status: "incomplete"}
	defer func() { out.DurationMS = time.Since(started).Milliseconds() }()
	if err := Validate(task); err != nil {
		return out, err
	}
	if opts.Implement == nil {
		return out, errors.New("evaluation: implementation callback required")
	}
	// Freeze caller-owned slices before work starts.
	task.ProtectedPaths = append([]string(nil), task.ProtectedPaths...)
	task.Policy.Checks = append([]verification.Check(nil), task.Policy.Checks...)
	b, _ := json.Marshal(task)
	sum := sha256.Sum256(b)
	out.ManifestDigest = hex.EncodeToString(sum[:])
	ctx, cancel := context.WithTimeout(ctx, time.Duration(task.Budget.TimeoutSeconds)*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			out.Status = "cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				out.Status = "timed_out"
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	repo, err := filepath.Abs(task.RepoPath)
	if err != nil {
		return out, fmt.Errorf("evaluation: repository path: %w", err)
	}
	if st, err := os.Stat(repo); err != nil || !st.IsDir() {
		return out, errors.New("evaluation: repository must be an existing local directory")
	}
	temp, err := os.MkdirTemp("", "conduit-evaluation-")
	if err != nil {
		return out, fmt.Errorf("evaluation: temporary workspace: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(temp); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("evaluation: cleanup: %w", cleanupErr))
			if out.Status == "passed" {
				out.Status = "cleanup_failed"
			}
		}
	}()
	root := filepath.Join(temp, "work")
	env := isolatedEnvironment(temp)
	if err := git(ctx, repo, env, "rev-parse", "--verify", task.Commit+"^{commit}"); err != nil {
		return out, err
	}
	if err := git(ctx, temp, env, "clone", "--quiet", "--no-local", "--no-hardlinks", "--no-checkout", "--template=", "--", repo, root); err != nil {
		return out, err
	}
	if err := git(ctx, root, env, "checkout", "--quiet", "--detach", task.Commit); err != nil {
		return out, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return out, fmt.Errorf("evaluation: canonical workspace: %w", err)
	}
	protected, err := protectedDigest(root, task.ProtectedPaths)
	if err != nil {
		return out, err
	}
	runner, err := verification.NewRunner(root, task.Policy, verification.Options{Authorize: opts.Authorize, Env: env})
	if err != nil {
		return out, err
	}
	callbackTask := task
	callbackTask.ProtectedPaths = append([]string(nil), task.ProtectedPaths...)
	callbackTask.Policy.Checks = append([]verification.Check(nil), task.Policy.Checks...)
	w := Workspace{Root: root, Task: callbackTask, Origin: episodes.OriginEvaluation, DisableMemory: true, Environment: append([]string(nil), env...)}
	out.Usage, err = opts.Implement(ctx, w)
	if err != nil {
		return out, fmt.Errorf("evaluation: implement: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if out.Usage.ModelRevision != task.ModelRevision {
		out.Status = "revision_mismatch"
		return out, nil
	}
	if out.Usage.Turns < 0 || out.Usage.Tokens < 0 || out.Usage.Turns > task.Budget.MaxTurns || out.Usage.Tokens > task.Budget.MaxTokens {
		out.Status = "budget_exceeded"
		return out, nil
	}
	after, err := protectedDigest(root, task.ProtectedPaths)
	if err != nil {
		return out, err
	}
	if after != protected {
		out.Status = "check_definitions_changed"
		return out, nil
	}
	out.Verification, err = runner.Run(ctx)
	if err != nil {
		return out, fmt.Errorf("evaluation: verification: %w", err)
	}
	out.Status = string(out.Verification.Status)
	return out, nil
}

func git(ctx context.Context, root string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = root
	cmd.Env = env
	procgroup.Set(cmd)
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("evaluation: git %s: %w", args[0], err)
	}
	return nil
}

func isolatedEnvironment(home string) []string {
	// Empty inherited values explicitly: verification's executor merges its Env
	// overlay with the parent environment. No credentials are inherited by checks.
	env := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok {
			env = append(env, key+"=")
		}
	}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return append(env, "HOME="+home, "USERPROFILE="+home, "TMPDIR="+home, "TEMP="+home, "TMP="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
}

func protectedDigest(root string, paths []string) (string, error) {
	hash := sha256.New()
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	for _, relative := range paths {
		path := filepath.Join(root, relative)
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("evaluation: protected definition: %w", err)
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", errors.New("evaluation: protected path escapes workspace")
		}
		st, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("evaluation: protected stat: %w", err)
		}
		if !st.Mode().IsRegular() {
			return "", errors.New("evaluation: protected definitions must be regular files")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("evaluation: protected read: %w", err)
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(relative), st.Mode())
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
