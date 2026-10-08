package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/rtk"
)

type CompletionStatus string

const (
	CompletionVerified   CompletionStatus = "verified"
	CompletionFailed     CompletionStatus = "failed"
	CompletionBlocked    CompletionStatus = "blocked"
	CompletionIncomplete CompletionStatus = "incomplete"
	CompletionCancelled  CompletionStatus = "cancelled"
	CompletionYielded    CompletionStatus = "yielded"
	CompletionVerifying  CompletionStatus = "verifying"
	CompletionRepairing  CompletionStatus = "repairing"
)

// ErrCompletionIncomplete means this run did not establish verified completion.
// The returned history still contains provisional candidates for local recovery.
var ErrCompletionIncomplete = errors.New("agent: completion not verified")

type CompletionError struct {
	Status CompletionStatus
	Reason string
}

func (e *CompletionError) Error() string {
	return fmt.Sprintf("%s (%s): %s", ErrCompletionIncomplete, e.Status, e.Reason)
}
func (e *CompletionError) Unwrap() error { return ErrCompletionIncomplete }

type CompletionCandidate struct {
	History []api.Message
	Text    string
	Attempt int
}

type CompletionDecision struct {
	Status   CompletionStatus
	Feedback string
}

// CompletionConfig is opt-in and snapshotted at Run entry. Active lets the
// host exclude planning/read-only turns. Verify must establish machine evidence;
// assistant claims alone are not a successful decision. Callbacks must honor ctx.
type CompletionConfig struct {
	Active            func() bool
	PrepareCompletion func(context.Context) error
	Verify            func(context.Context, CompletionCandidate) (CompletionDecision, error)
	// Repair optionally invokes a pinned implementer. Returned messages are
	// appended to the parent history before it generates a new candidate.
	// Nil feeds the failure back through this same loop and client instead.
	Repair            func(context.Context, CompletionCandidate, string) ([]api.Message, error)
	MaxRepairAttempts int
	TotalTimeout      time.Duration
}

// SetCompletion installs the controller for subsequent runs. It does not change
// a controller already executing. Children deliberately do not inherit it.
func (l *Loop) SetCompletion(cfg *CompletionConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cfg == nil {
		l.cfg.Completion = nil
		return
	}
	copy := *cfg
	l.cfg.Completion = &copy
}

// SetResolveRole installs strict provider-aware resolution for subsequent child
// calls. Legacy RoleResolver remains supported when this callback is absent.
func (l *Loop) SetResolveRole(resolve func(string) (string, *api.Client, error)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.ResolveRole = resolve
}

// RequestToolPermission reuses the interactive permission callback for an
// explicitly authorized host command (for example a repository check). The
// caller must apply the permission gate first. Missing UI authority denies.
func (l *Loop) RequestToolPermission(ctx context.Context, name, input string) (bool, error) {
	l.mu.RLock()
	ask := l.cfg.AskPermission
	l.mu.RUnlock()
	if ask == nil {
		return false, errors.New("interactive permission is unavailable")
	}
	allow, _ := ask(ctx, name, input)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return allow, nil
}

type completionRun struct {
	cfg      CompletionConfig
	repairs  int
	finished bool
	text     strings.Builder
}

func (l *Loop) beginCompletion(ctx context.Context) (context.Context, context.CancelFunc, *completionRun, error) {
	l.mu.RLock()
	var cfg *CompletionConfig
	if l.cfg.Completion != nil {
		copy := *l.cfg.Completion
		cfg = &copy
	}
	l.mu.RUnlock()
	if cfg == nil || (cfg.Active != nil && !cfg.Active()) {
		return ctx, func() {}, nil, nil
	}
	if cfg.Verify == nil || cfg.MaxRepairAttempts < 0 || cfg.MaxRepairAttempts > 10 || cfg.TotalTimeout < 0 {
		return ctx, func() {}, nil, &CompletionError{Status: CompletionBlocked, Reason: "invalid completion configuration"}
	}
	if cfg.TotalTimeout == 0 {
		cfg.TotalTimeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.TotalTimeout)
	return ctx, cancel, &completionRun{cfg: *cfg}, nil
}

func (c *completionRun) emit(handler func(LoopEvent), status CompletionStatus, notice string) {
	handler(LoopEvent{Type: EventCompletion, CompletionStatus: status, CompletionAttempt: c.repairs + 1})
	if notice != "" {
		handler(LoopEvent{Type: EventNotice, Notice: notice})
	}
}

func (c *completionRun) incomplete(handler func(LoopEvent), status CompletionStatus, reason string) error {
	c.finished = true
	c.emit(handler, status, reason)
	return &CompletionError{Status: status, Reason: reason}
}

func (c *completionRun) check(ctx context.Context, msgs []api.Message, handler func(LoopEvent)) ([]api.Message, bool, error) {
	if err := ctx.Err(); err != nil {
		return msgs, false, err
	}
	c.emit(handler, CompletionVerifying, "Verifying repository checks…")
	if c.cfg.PrepareCompletion != nil {
		if err := c.cfg.PrepareCompletion(ctx); err != nil {
			if ctx.Err() != nil {
				return msgs, false, ctx.Err()
			}
			return msgs, false, c.incomplete(handler, CompletionBlocked, "Cannot prepare completion: "+err.Error())
		}
	}
	if err := ctx.Err(); err != nil {
		return msgs, false, err
	}
	candidate := CompletionCandidate{History: append([]api.Message(nil), msgs...), Text: c.text.String(), Attempt: c.repairs + 1}
	decision, err := c.cfg.Verify(ctx, candidate)
	if ctx.Err() != nil {
		return msgs, false, ctx.Err()
	}
	if err != nil {
		return msgs, false, c.incomplete(handler, CompletionBlocked, "Verification unavailable: "+err.Error())
	}
	if decision.Status == CompletionVerified {
		c.finished = true
		c.emit(handler, CompletionVerified, "Repository checks passed.")
		return msgs, false, nil
	}
	feedback, _ := rtk.RedactSecrets(decision.Feedback)
	if len(feedback) > 30000 {
		feedback = strings.ToValidUTF8(feedback[:30000], "") + "\n[verification feedback truncated]"
	}
	if decision.Status != CompletionFailed {
		status := decision.Status
		if status != CompletionBlocked && status != CompletionIncomplete && status != CompletionCancelled && status != CompletionYielded {
			status = CompletionBlocked
		}
		return msgs, false, c.incomplete(handler, status, "Completion not verified: "+feedback)
	}
	if c.repairs >= c.cfg.MaxRepairAttempts {
		return msgs, false, c.incomplete(handler, CompletionIncomplete, "Repair budget exhausted. "+feedback)
	}
	c.repairs++
	c.emit(handler, CompletionRepairing, fmt.Sprintf("Repairing verification failure (%d/%d)…", c.repairs, c.cfg.MaxRepairAttempts))
	msgs = append(msgs, nudgeMessage("<verification-failure>\n"+feedback+"\n</verification-failure>\nRepair the implementation, preserve the configured checks, and produce a new completion candidate."))
	if c.cfg.Repair != nil {
		added, err := c.cfg.Repair(ctx, candidate, feedback)
		if ctx.Err() != nil {
			return msgs, false, ctx.Err()
		}
		if err != nil {
			return msgs, false, c.incomplete(handler, CompletionIncomplete, "Implementation repair failed: "+err.Error())
		}
		msgs = append(msgs, added...)
	}
	c.text.Reset()
	return msgs, true, nil
}

func (l *Loop) resolveExplicitRole(spec SubAgentSpec) (SubAgentSpec, error) {
	l.mu.RLock()
	resolve := l.cfg.ResolveRole
	l.mu.RUnlock()
	if resolve == nil || spec.Role == "" || spec.Client != nil {
		return spec, nil
	}
	model, client, err := resolve(spec.Role)
	if err != nil {
		return spec, fmt.Errorf("resolve subagent role %q: %w", spec.Role, err)
	}
	if model == "" || client == nil {
		return spec, fmt.Errorf("resolve subagent role %q: explicit model and client are required", spec.Role)
	}
	if spec.Model == "" {
		spec.Model = model
	}
	spec.Client = client
	return spec, nil
}
