package app

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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/coding"
	"github.com/icehunter/conduit/internal/episodes"
	"github.com/icehunter/conduit/internal/globalconfig"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/routing"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/verification"
)

type CodingOptions struct {
	Root, SessionID, WireVersion, Model string
	Settings                            *settings.Merged
	Gate                                *permissions.Gate
	MCP                                 *mcp.Manager
	Secure                              secure.Storage
	Client                              *api.Client
	AskPermission                       func(context.Context, string, string) (bool, error)
	// StateDir isolates tests and managed evaluation runs from live session data.
	StateDir         string
	Policies         *settings.CodingPolicies
	CurrentRoot      func() string
	HasActiveWriters func() bool
}

type CodingRuntime struct {
	opts       CodingOptions
	policies   settings.CodingPolicies
	verifier   *verification.Runner
	providers  CodingProviderResolver
	routes     *routing.Store
	recorder   *episodes.Recorder
	lockPath   string
	mu         sync.Mutex
	lastTask   *coding.Task
	lastDigest string
}

// RepositoryRoot resolves the Git worktree without changing cwd.
func RepositoryRoot(ctx context.Context, cwd string) (string, error) {
	c := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	b, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("coding requires a Git worktree: %w", err)
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(b)))
}

// NewCodingRuntime is nil when enforcement is disabled. Policies must be loaded
// before legacy settings migration; callers may provide that strict snapshot.
func NewCodingRuntime(opts CodingOptions) (*CodingRuntime, error) {
	var p settings.CodingPolicies
	var err error
	if opts.Policies != nil {
		p = *opts.Policies
	} else {
		p, err = settings.LoadCodingPolicies(opts.Root)
		if err != nil {
			return nil, err
		}
	}
	if p.DefinitionOfDone == nil || !p.DefinitionOfDone.Enabled {
		return nil, nil
	}
	opts.Root, err = RepositoryRoot(context.Background(), opts.Root)
	if err != nil {
		return nil, err
	}
	if opts.Gate == nil {
		return nil, errors.New("coding requires a permission gate")
	}
	if opts.Settings == nil {
		return nil, errors.New("coding requires provider settings")
	}
	// Freeze provider, pool and policy maps; hot reload must not move an active
	// session's implementer or weaken its completion requirements.
	raw, err := json.Marshal(struct {
		Settings *settings.Merged
		Policies settings.CodingPolicies
	}{opts.Settings, p})
	if err != nil {
		return nil, fmt.Errorf("coding configuration snapshot: %w", err)
	}
	var frozen struct {
		Settings *settings.Merged
		Policies settings.CodingPolicies
	}
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	opts.Settings = frozen.Settings
	p = frozen.Policies
	if err = settings.ValidateProviderPools(opts.Settings.Providers, p.ProviderPools, p.RolePools); err != nil {
		return nil, err
	}
	if opts.SessionID == "" {
		opts.SessionID = NewSessionID()
	}
	if opts.StateDir == "" {
		opts.StateDir = filepath.Join(settings.ConduitDir(), "coding")
	}
	if err = os.MkdirAll(opts.StateDir, 0o700); err != nil {
		return nil, err
	}
	rt := &CodingRuntime{opts: opts, policies: p}
	policy := verification.Policy{Enabled: true, MaxRepairAttempts: p.DefinitionOfDone.MaxRepairAttempts, TotalTimeoutSeconds: p.DefinitionOfDone.TotalTimeoutSeconds}
	for _, c := range p.DefinitionOfDone.Checks {
		policy.Checks = append(policy.Checks, verification.Check{ID: c.ID, Command: c.Command, Cwd: c.Cwd, TimeoutSeconds: c.TimeoutSeconds})
	}
	var checkEnv []string
	for key, value := range opts.Settings.Env {
		checkEnv = append(checkEnv, key+"="+value)
	}
	sort.Strings(checkEnv)
	rt.verifier, err = verification.NewRunner(opts.Root, policy, verification.Options{Authorize: rt.authorizeCheck, ProtectedPaths: p.DefinitionOfDone.ProtectedPaths, Env: checkEnv})
	if err != nil {
		return nil, err
	}
	rt.routes, err = routing.Open(filepath.Join(opts.StateDir, "routing.sqlite"))
	if err != nil {
		return nil, err
	}
	rt.providers = CodingProviderResolver{Settings: opts.Settings, Policies: p, Routing: rt.routes, SessionID: opts.SessionID, WireVersion: opts.WireVersion, Credentials: opts.Secure, MCP: opts.MCP, FallbackClient: opts.Client, FallbackModel: opts.Model}
	ep := episodes.Config{Dir: filepath.Join(opts.StateDir, "episodes"), RepositoryRoot: opts.Root, SessionID: opts.SessionID}
	if p.Episodes != nil {
		ep.Enabled = p.Episodes.Enabled
		ep.IncludeContent = p.Episodes.IncludeContent
		ep.AllowedPaths = p.Episodes.AllowedPaths
		ep.ExcludedPaths = p.Episodes.ExcludedPaths
		ep.RetentionDays = p.Episodes.RetentionDays
	}
	if os.Getenv("CONDUIT_EVAL_ORIGIN") == episodes.OriginEvaluation {
		ep.Enabled = false
	}
	rt.providers.CaptureMCPTrace = ep.Enabled && ep.IncludeContent
	rt.recorder, err = episodes.New(ep)
	if err != nil {
		_ = rt.routes.Close()
		return nil, err
	}
	if ep.Enabled && ep.RetentionDays > 0 {
		if err = episodes.Prune(ep.Dir, ep.RetentionDays, time.Now()); err != nil {
			_ = rt.Close()
			return nil, err
		}
	}
	rt.lockPath = filepath.Join(opts.StateDir, "workspace-"+codingDigest(opts.Root)+".lock")
	return rt, nil
}

func (r *CodingRuntime) Close() error {
	if r == nil {
		return nil
	}
	return errors.Join(r.recorder.Close(), r.routes.Close())
}
func (r *CodingRuntime) Register(reg *tool.Registry) {
	if r != nil {
		reg.Register(&coding.Tool{Run: r.Run})
	}
}
func (r *CodingRuntime) ResolveRole(role string) (string, *api.Client, error) {
	return r.providers.ResolveRole(role)
}

func (r *CodingRuntime) authorize(ctx context.Context, name, input string) error {
	switch r.opts.Gate.Check(name, input) {
	case permissions.DecisionAllow:
		return nil
	case permissions.DecisionDeny:
		return fmt.Errorf("%s denied by permission policy", name)
	default:
		if r.opts.AskPermission == nil {
			return fmt.Errorf("%s requires approval; unavailable in noninteractive mode", name)
		}
		ok, err := r.opts.AskPermission(ctx, name, input)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s permission declined", name)
		}
		return nil
	}
}
func (r *CodingRuntime) authorizeCheck(ctx context.Context, c verification.Check) error {
	trusted, err := globalconfig.IsTrusted(r.opts.Root)
	if err != nil {
		return err
	}
	if !trusted {
		return errors.New("repository must be trusted before executing its definition of done")
	}
	name := "Bash"
	if runtime.GOOS == "windows" {
		name = "Shell"
	}
	return r.authorize(ctx, name, c.Command)
}
func (r *CodingRuntime) verify(ctx context.Context) (coding.Evidence, error) {
	if err := r.checkLocation(ctx); err != nil {
		return coding.Evidence{Blocked: true}, err
	}
	report, err := r.verifier.Run(ctx)
	b, marshalErr := json.Marshal(report)
	if marshalErr != nil {
		return coding.Evidence{}, marshalErr
	}
	return coding.Evidence{Passed: report.Status == verification.Passed, Blocked: report.Status == verification.Blocked, Feedback: string(b), WorkspaceBefore: report.WorkspaceBefore, WorkspaceDigest: report.WorkspaceAfter, PolicyDigest: report.PolicyHash, Detail: report}, err
}

// Run serializes participating Conduit writers for the complete task. External
// editor changes are still detected through source and verification digests.
func (r *CodingRuntime) Run(ctx context.Context, task coding.Task) (coding.Outcome, error) {
	timeout := r.policies.DefinitionOfDone.TotalTimeoutSeconds
	if timeout == 0 {
		timeout = 600
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	if err := r.checkLocation(ctx); err != nil {
		return coding.Outcome{Status: "blocked"}, err
	}
	trusted, err := globalconfig.IsTrusted(r.opts.Root)
	if err != nil {
		return coding.Outcome{Status: "blocked"}, err
	}
	if !trusted {
		return coding.Outcome{Status: "blocked"}, errors.New("repository must be trusted before coding begins")
	}
	if !r.mu.TryLock() {
		return coding.Outcome{Status: "blocked"}, errors.New("another coding task is active in this session")
	}
	defer r.mu.Unlock()
	mode := r.opts.Gate.Mode()
	if mode == permissions.ModePlan || mode == permissions.ModeCouncil {
		return coding.Outcome{Status: "blocked"}, errors.New("CodingTask is unavailable in planning/review mode")
	}
	lock := flock.New(r.lockPath)
	ok, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return coding.Outcome{}, err
	}
	if !ok {
		return coding.Outcome{}, errors.New("coding workspace busy")
	}
	defer func() { _ = lock.Close() }()
	r.lastDigest = ""
	copyTask := task
	copyTask.Files = append([]string(nil), task.Files...)
	r.lastTask = &copyTask
	provider := r.providers
	group, err := codingRepositoryGroup(ctx, r.opts.Root)
	if err != nil {
		return coding.Outcome{}, err
	}
	journal := newCodingJournal(r.recorder, group, task)
	provider.OnRequest = journal.request
	provider.OnResponse = journal.response
	run := coding.Runner{Root: r.opts.Root, MaxRepairAttempts: r.policies.DefinitionOfDone.MaxRepairAttempts, TotalTimeout: time.Duration(r.policies.DefinitionOfDone.TotalTimeoutSeconds) * time.Second, Generate: provider.Generate, Verify: r.verify, AuthorizeWrite: func(ctx context.Context, path string) error { return r.authorize(ctx, "Edit", path) }, Observe: journal.observe}
	run.AuthorizeRead = func(ctx context.Context, path string) error { return r.authorize(ctx, "Read", path) }
	run.Snapshot = func(ctx context.Context) (string, error) { return verification.WorkspaceFingerprint(ctx, r.opts.Root) }
	run.AuthorizeWrite = func(ctx context.Context, path string) error {
		rel, err := filepath.Rel(r.opts.Root, path)
		if err != nil {
			return err
		}
		if r.verifier.IsProtectedPath(filepath.ToSlash(rel)) {
			return errors.New("task cannot edit frozen verification definitions; change them explicitly and restart")
		}
		return r.authorize(ctx, "Edit", path)
	}
	if r.policies.DefinitionOfDone.RequireReview {
		run.Review = provider.Review
	}
	out, err := run.Run(ctx, task)
	if err == nil && out.Status == "verified" {
		r.lastDigest = journal.lastDigest
	}
	return out, err
}

func (r *CodingRuntime) checkLocation(ctx context.Context) error {
	if r.opts.CurrentRoot == nil {
		return nil
	}
	root, err := RepositoryRoot(ctx, r.opts.CurrentRoot())
	if err != nil {
		return err
	}
	if root != r.opts.Root {
		return errors.New("coding worktree changed; restart the session to accept its policy and provider assignments")
	}
	return nil
}

func (r *CodingRuntime) Completion(prepare func(context.Context) error, active func() bool) *agent.CompletionConfig {
	if r == nil {
		return nil
	}
	return &agent.CompletionConfig{Active: active, PrepareCompletion: prepare, MaxRepairAttempts: r.policies.DefinitionOfDone.MaxRepairAttempts, TotalTimeout: time.Duration(r.policies.DefinitionOfDone.TotalTimeoutSeconds) * time.Second,
		Verify: func(ctx context.Context, _ agent.CompletionCandidate) (agent.CompletionDecision, error) {
			if !r.mu.TryLock() {
				return agent.CompletionDecision{Status: agent.CompletionBlocked, Feedback: "A coding task is still active."}, nil
			}
			defer r.mu.Unlock()
			if r.opts.HasActiveWriters != nil && r.opts.HasActiveWriters() {
				return agent.CompletionDecision{Status: agent.CompletionBlocked, Feedback: "Background work is still active; wait for it before completion."}, nil
			}
			lock := flock.New(r.lockPath)
			locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
			if err != nil {
				return agent.CompletionDecision{}, err
			}
			if !locked {
				return agent.CompletionDecision{Status: agent.CompletionBlocked, Feedback: "Workspace is busy"}, nil
			}
			defer func() { _ = lock.Close() }()
			evidence, err := r.verify(ctx)
			if err != nil {
				return agent.CompletionDecision{}, err
			}
			if evidence.Blocked {
				return agent.CompletionDecision{Status: agent.CompletionBlocked, Feedback: evidence.Feedback}, nil
			}
			if !evidence.Passed {
				return agent.CompletionDecision{Status: agent.CompletionFailed, Feedback: evidence.Feedback}, nil
			}
			reviewed := r.lastDigest == evidence.WorkspaceDigest
			if r.policies.DefinitionOfDone.RequireReview && !reviewed {
				return agent.CompletionDecision{Status: agent.CompletionBlocked, Feedback: "Checks passed, but this workspace has no matching independent review. Use CodingTask."}, nil
			}
			if r.opts.HasActiveWriters != nil && r.opts.HasActiveWriters() {
				return agent.CompletionDecision{Status: agent.CompletionBlocked, Feedback: "Background work started during verification."}, nil
			}
			return agent.CompletionDecision{Status: agent.CompletionVerified}, nil
		}, Repair: func(ctx context.Context, _ agent.CompletionCandidate, feedback string) ([]api.Message, error) {
			r.mu.Lock()
			var task coding.Task
			if r.lastTask != nil {
				task = *r.lastTask
				task.Files = append([]string(nil), task.Files...)
			}
			r.mu.Unlock()
			if task.Prompt == "" {
				return nil, errors.New("verification failed without a bounded local task; invoke CodingTask with explicit files")
			}
			task.Prompt += "\nRepair repository verification failure:\n" + feedback
			out, err := r.Run(ctx, task)
			if err != nil {
				return nil, err
			}
			if out.Status != "verified" {
				return nil, fmt.Errorf("local repair %s: %s", out.Status, out.Feedback)
			}
			return []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "The pinned local implementer repaired the task and repository verification passed. Produce the final result from the actual changes."}}}}, nil
		}}
}

func codingDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Keep a repository history together across clones, worktrees and repairs.
func codingRepositoryGroup(ctx context.Context, root string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-list", "--max-parents=0", "HEAD").Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		out, err = exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
		if err != nil {
			return "", fmt.Errorf("coding repository identity: %w", err)
		}
	}
	parts := strings.Fields(string(out))
	sort.Strings(parts)
	return codingDigest(strings.Join(parts, "\n")), nil
}

type codingJournal struct {
	recorder                                                                            *episodes.Recorder
	episode, repo, task, requestID, contextDigest, model, lastDigest, implementIdentity string
	sources                                                                             []string
	attempt                                                                             int
	pending                                                                             *episodes.Candidate
}

func newCodingJournal(recorder *episodes.Recorder, root string, task coding.Task) *codingJournal {
	return &codingJournal{recorder: recorder, episode: api.NewUUID(), repo: root, task: codingDigest(task.Prompt), sources: append([]string(nil), task.Files...)}
}
func (j *codingJournal) write(kind string, payload any) error {
	return j.recorder.Record(episodes.Event{Kind: kind, EpisodeID: j.episode, RunID: j.episode, AttemptID: strconv.Itoa(j.attempt), RequestID: j.requestID, Origin: episodes.OriginInteractive, Sources: j.sources, Payload: payload})
}
func (j *codingJournal) request(role string, p settings.ActiveProviderSettings, request episodes.Request) error {
	identity := p.Kind + "|" + p.BaseURL + "|" + p.Server + "|" + p.Account + "|" + p.Model
	if role != settings.RoleImplement {
		if identity == j.implementIdentity || p.Model == j.model {
			return errors.New("review requires a model distinct from the implementer")
		}
		return j.write("review_request", request)
	}
	j.implementIdentity = identity
	j.requestID = api.NewUUID()
	j.contextDigest = episodes.ContextDigest(request)
	j.model = p.Model
	return j.write(episodes.KindRequest, request)
}
func (j *codingJournal) response(role, text string) error {
	kind := episodes.KindResponse
	if role != settings.RoleImplement {
		kind = "review_response"
	}
	return j.write(kind, map[string]string{"role": role, "text": text})
}
func (j *codingJournal) observe(e coding.Event) error {
	j.attempt = e.Attempt
	switch e.Kind {
	case "start":
		return j.write(episodes.KindStart, episodes.Start{RepoID: j.repo, TaskID: j.task, SplitGroup: j.repo})
	case "candidate":
		j.pending = &episodes.Candidate{ID: j.episode + ":" + strconv.Itoa(e.Attempt), RequestID: j.requestID, ContextDigest: j.contextDigest, BaseDigest: e.BaseDigest, Completion: e.Draft, Patch: e.Patch, Role: settings.RoleImplement, ModelID: j.model}
	case "verification":
		j.lastDigest = e.Evidence.WorkspaceDigest
		id := j.episode + ":" + strconv.Itoa(e.Attempt)
		if j.pending != nil {
			j.pending.WorkspaceDigest = e.Evidence.WorkspaceDigest
			if err := j.write(episodes.KindCandidate, *j.pending); err != nil {
				return err
			}
			j.pending = nil
		}
		return j.write(episodes.KindVerification, episodes.Verification{CandidateID: id, WorkspaceDigest: e.Evidence.WorkspaceDigest, PolicyDigest: e.Evidence.PolicyDigest, Passed: e.Evidence.Passed})
	case "review":
		action := "rejected"
		if e.Review.Accepted {
			action = "accepted"
		}
		return j.write(episodes.KindDecision, episodes.Decision{CandidateID: j.episode + ":" + strconv.Itoa(e.Attempt), Action: action, Source: episodes.SourceReviewerModel, Reason: e.Review.Feedback})
	case "application_failed":
		return j.write(episodes.KindTool, map[string]any{"operation": "apply_patch", "status": "failed", "outcome": e.Outcome})
	case "end":
		return j.write(episodes.KindEnd, episodes.End{Complete: e.Outcome != nil && e.Outcome.Status != "cancelled" && e.Outcome.Status != "error", Status: e.Outcome.Status, Reason: e.Outcome.Feedback})
	}
	return nil
}
