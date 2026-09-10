// Package workflowtool implements the Workflow tool: the model hands it a
// JavaScript orchestration script (or the name of a saved one) and the
// script fans work out across sub-agents deterministically. Runs execute in
// the background; the tool returns a runId at once and a <task-notification>
// arrives as a user message when the run settles.
package workflowtool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/coordinator"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/workflow"
)

// Notify delivers the completion notification to the conversation. Provided
// by main as loop.InjectMessage.
type Notify func(text string)

// Tool implements the Workflow tool.
type Tool struct {
	tool.NotDeferrable
	host     workflow.Host
	registry *workflow.Registry
	notify   Notify
	// cwd resolves saved workflows and relative scriptPaths.
	cwd func() string
	// runsDir is where per-run directories (script.js, journal.jsonl) live.
	runsDir string
	// ultracode reports whether the standing opt-in is on for the session.
	ultracode func() bool
	// maxNotify caps the JSON result embedded in the notification; larger
	// results point at journal.jsonl instead.
	maxNotify int

	mu      sync.Mutex
	started map[string]context.CancelFunc
}

// Config wires the tool.
type Config struct {
	Host      workflow.Host
	Registry  *workflow.Registry
	Notify    Notify
	Cwd       func() string
	RunsDir   string
	Ultracode func() bool
}

// New returns a Workflow tool.
func New(cfg Config) *Tool {
	if cfg.Registry == nil {
		cfg.Registry = workflow.Default
	}
	if cfg.Cwd == nil {
		cfg.Cwd = func() string { d, _ := os.Getwd(); return d }
	}
	if cfg.Ultracode == nil {
		cfg.Ultracode = func() bool { return false }
	}
	return &Tool{
		host:      cfg.Host,
		registry:  cfg.Registry,
		notify:    cfg.Notify,
		cwd:       cfg.Cwd,
		runsDir:   cfg.RunsDir,
		ultracode: cfg.Ultracode,
		maxNotify: 24 * 1024,
		started:   map[string]context.CancelFunc{},
	}
}

func (*Tool) Name() string { return "Workflow" }

func (t *Tool) Description() string {
	return description(t.ultracode())
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"script": {"type": "string", "description": "Inline workflow script (plain JavaScript beginning with export const meta = {...}). Mutually exclusive with name and scriptPath."},
		"scriptPath": {"type": "string", "description": "Path to a workflow script file (e.g. one persisted by an earlier run, or one you wrote). Mutually exclusive with script and name."},
		"name": {"type": "string", "description": "Name of a saved workflow (~/.conduit/workflows, ~/.claude/workflows, .claude/workflows). Mutually exclusive with script and scriptPath."},
		"args": {"description": "Value exposed to the script as the args global. Pass real JSON values (objects/arrays), not a JSON-encoded string."},
		"budgetTokens": {"type": "integer", "description": "Optional hard ceiling on output tokens for the run; exposed as budget.total. Omit for no budget."},
		"resumeFromRunId": {"type": "string", "description": "Replay cached agent results from an earlier run's journal; unchanged calls return instantly, edited/new calls run live."},
		"runId": {"type": "string", "description": "With op: the run to inspect or kill."},
		"op": {"type": "string", "enum": ["status", "kill", "result"], "description": "Inspect a run instead of starting one. status: phases/nodes/tokens; kill: cancel; result: the final JSON (or journal path when large)."}
	}
}`)
}

func (*Tool) IsReadOnly(raw json.RawMessage) bool {
	var in Input
	_ = json.Unmarshal(raw, &in)
	return in.Op == "status" || in.Op == "result"
}

func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Input is the tool input.
type Input struct {
	Script          string          `json:"script,omitempty"`
	ScriptPath      string          `json:"scriptPath,omitempty"`
	Name            string          `json:"name,omitempty"`
	Args            json.RawMessage `json:"args,omitempty"`
	BudgetTokens    int             `json:"budgetTokens,omitempty"`
	ResumeFromRunID string          `json:"resumeFromRunId,omitempty"`
	RunID           string          `json:"runId,omitempty"`
	Op              string          `json:"op,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("workflow: invalid input: %v", err)), nil
	}
	if in.Op != "" {
		return t.runOp(in)
	}
	if t.host == nil {
		return tool.ErrorResult("workflow: no agent host is configured for this session"), nil
	}

	src, scriptPath, err := t.resolveSource(in)
	if err != nil {
		return tool.ErrorResult("workflow: " + err.Error()), nil
	}
	script, err := workflow.Load(src)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	if len(in.Args) > 0 {
		if s, isString := stringArgs(in.Args); isString && looksLikeJSON(s) {
			return tool.ErrorResult("workflow: args is a JSON-encoded string; pass the object/array itself so the script receives a real value"), nil
		}
	}

	runID := newRunID()
	runDir := filepath.Join(t.runsDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return tool.ErrorResult(fmt.Sprintf("workflow: cannot create run directory: %v", err)), nil
	}
	if scriptPath == "" {
		scriptPath = filepath.Join(runDir, "script.js")
		if err := os.WriteFile(scriptPath, []byte(src), 0o644); err != nil { //nolint:gosec // run dir is user-owned
			return tool.ErrorResult(fmt.Sprintf("workflow: cannot persist script: %v", err)), nil
		}
	}

	journal, err := workflow.OpenJournal(runDir, runID)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	if in.ResumeFromRunID != "" {
		prior := filepath.Join(t.runsDir, filepath.Base(in.ResumeFromRunID), "journal.jsonl")
		if err := journal.LoadPrior(prior, in.ResumeFromRunID); err != nil {
			journal.Final(nil, err.Error())
			return tool.ErrorResult(fmt.Sprintf("workflow: cannot resume from %s: %v", in.ResumeFromRunID, err)), nil
		}
	}

	run := &workflow.Run{
		ID:         runID,
		Name:       script.Meta.Name,
		ScriptPath: scriptPath,
		ScriptHash: script.Hash,
		Args:       in.Args,
		StartedAt:  time.Now(),
	}
	t.registry.Add(run)

	// The run outlives this tool call: detach from the call's context but
	// keep a handle so kill and session shutdown can stop it.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t.mu.Lock()
	t.started[runID] = cancel
	t.mu.Unlock()

	go t.execute(runCtx, cancel, run, script, workflow.Options{
		Host:        t.host,
		Args:        in.Args,
		BudgetTotal: in.BudgetTokens,
		Journal:     journal,
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "Workflow %q started in the background.\nrunId: %s\nscriptPath: %s\njournal: %s\n\n", script.Meta.Name, runID, scriptPath, journal.Path)
	sb.WriteString("A <task-notification> will arrive when it completes. Use /workflows to watch live progress, or Workflow({op: \"status\", runId}) to poll. ")
	sb.WriteString("To iterate, edit the script at scriptPath and relaunch with {scriptPath, resumeFromRunId: runId}.")
	return tool.TextResult(sb.String()), nil
}

func (t *Tool) execute(ctx context.Context, cancel context.CancelFunc, run *workflow.Run, script *workflow.Script, opts workflow.Options) {
	defer cancel()
	defer func() {
		t.mu.Lock()
		delete(t.started, run.ID)
		t.mu.Unlock()
	}()
	start := time.Now()
	result, err := workflow.Execute(ctx, run, script, opts)
	if t.notify == nil {
		return
	}
	snap := run.Snapshot()
	status, summary := "completed", fmt.Sprintf("Workflow %q completed: %d agents, %d output tokens", run.Name, len(snap.Nodes), snap.OutputTokens)
	body := ""
	switch {
	case errors.Is(err, workflow.ErrAborted):
		status, summary = "killed", fmt.Sprintf("Workflow %q was killed after %d agents", run.Name, len(snap.Nodes))
	case err != nil:
		status, summary = "failed", fmt.Sprintf("Workflow %q failed: %v", run.Name, err)
	default:
		body = string(result)
		if len(body) > t.maxNotify {
			body = body[:t.maxNotify] + fmt.Sprintf("\n…[truncated; full result in %s]", opts.Journal.Path)
		}
	}
	body = strings.TrimSpace(body + "\n\n" + fmt.Sprintf("runId: %s\nscriptPath: %s\njournal: %s", run.ID, run.ScriptPath, opts.Journal.Path))
	if n := len(snap.Logs); n > 0 {
		tail := snap.Logs
		if n > 20 {
			tail = tail[n-20:]
		}
		var lb strings.Builder
		for _, l := range tail {
			lb.WriteString("\n- " + l.Text)
		}
		body += "\n\nlog:" + lb.String()
	}
	t.notify(coordinator.TaskNotification(run.ID, status, summary, body, snap.InputTokens+snap.OutputTokens, len(snap.Nodes), time.Since(start).Milliseconds()))
}

func (t *Tool) runOp(in Input) (tool.Result, error) {
	if in.RunID == "" {
		return tool.ErrorResult("workflow: op requires runId"), nil
	}
	run := t.registry.Get(in.RunID)
	if run == nil {
		return tool.ErrorResult(fmt.Sprintf("workflow: unknown runId %q", in.RunID)), nil
	}
	snap := run.Snapshot()
	switch in.Op {
	case "kill":
		if snap.Status != workflow.RunRunning {
			return tool.TextResult(fmt.Sprintf("Run %s is already %s.", in.RunID, snap.Status)), nil
		}
		run.Kill()
		return tool.TextResult(fmt.Sprintf("Run %s killed.", in.RunID)), nil
	case "result":
		if snap.Status == workflow.RunRunning {
			return tool.TextResult(fmt.Sprintf("Run %s is still running (%d agents so far).", in.RunID, len(snap.Nodes))), nil
		}
		if snap.Err != "" {
			return tool.TextResult(fmt.Sprintf("Run %s %s: %s", in.RunID, snap.Status, snap.Err)), nil
		}
		body := string(snap.Result)
		if len(body) > t.maxNotify {
			body = body[:t.maxNotify] + fmt.Sprintf("\n…[truncated; full result in %s]", filepath.Join(t.runsDir, in.RunID, "journal.jsonl"))
		}
		return tool.TextResult(body), nil
	case "status":
		return tool.TextResult(FormatStatus(snap)), nil
	default:
		return tool.ErrorResult(fmt.Sprintf("workflow: unknown op %q", in.Op)), nil
	}
}

// FormatStatus renders a run snapshot as a compact text report.
func FormatStatus(s workflow.Snapshot) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "run %s (%s) — %s\n", s.ID, s.Name, s.Status)
	byPhase := map[string][]workflow.Node{}
	for _, n := range s.Nodes {
		byPhase[n.Phase] = append(byPhase[n.Phase], n)
	}
	for _, p := range s.Phases {
		nodes := byPhase[p.Title]
		var done, running, failed, cached int
		for _, n := range nodes {
			switch n.Status {
			case workflow.NodeDone:
				done++
			case workflow.NodeRunning:
				running++
			case workflow.NodeFailed:
				failed++
			case workflow.NodeCached:
				cached++
			case workflow.NodeQueued:
			}
		}
		marker := " "
		if p.Title == s.CurrentPhase && s.Status == workflow.RunRunning {
			marker = "▶"
		}
		fmt.Fprintf(&sb, "%s %s: %d agents (%d done, %d running, %d failed, %d cached)\n", marker, p.Title, len(nodes), done, running, failed, cached)
		delete(byPhase, p.Title)
	}
	for title, nodes := range byPhase {
		fmt.Fprintf(&sb, "  %s: %d agents\n", title, len(nodes))
	}
	fmt.Fprintf(&sb, "tokens: %d in / %d out", s.InputTokens, s.OutputTokens)
	if s.BudgetTotal > 0 {
		fmt.Fprintf(&sb, " (budget %d, spent %d)", s.BudgetTotal, s.Spent)
	}
	sb.WriteString("\n")
	if n := len(s.Logs); n > 0 {
		sb.WriteString("recent log:\n")
		tail := s.Logs
		if n > 10 {
			tail = tail[n-10:]
		}
		for _, l := range tail {
			sb.WriteString("  " + l.Text + "\n")
		}
	}
	if s.Err != "" {
		sb.WriteString("error: " + s.Err + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (t *Tool) resolveSource(in Input) (src, path string, err error) {
	set := 0
	for _, v := range []string{in.Script, in.ScriptPath, in.Name} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return "", "", errors.New("exactly one of script, scriptPath or name is required")
	}
	switch {
	case in.Script != "":
		return in.Script, "", nil
	case in.ScriptPath != "":
		p := in.ScriptPath
		if !filepath.IsAbs(p) {
			p = filepath.Join(t.cwd(), p)
		}
		b, err := os.ReadFile(p) //nolint:gosec // model-supplied path is permission-gated upstream
		if err != nil {
			return "", "", fmt.Errorf("cannot read scriptPath: %w", err)
		}
		return string(b), p, nil
	default:
		saved := workflow.Find(t.cwd(), in.Name)
		if saved == nil {
			names := []string{}
			for _, s := range workflow.Discover(t.cwd()) {
				if s.Err == "" {
					names = append(names, s.Name)
				}
			}
			return "", "", fmt.Errorf("no saved workflow named %q; available: %s", in.Name, strings.Join(names, ", "))
		}
		b, err := os.ReadFile(saved.Path) //nolint:gosec // discovered under the user's config dirs
		if err != nil {
			return "", "", fmt.Errorf("cannot read saved workflow: %w", err)
		}
		return string(b), saved.Path, nil
	}
}

// KillAll cancels every run this tool started (session shutdown).
func (t *Tool) KillAll() {
	t.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(t.started))
	for _, c := range t.started {
		cancels = append(cancels, c)
	}
	t.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

func newRunID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

func stringArgs(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}
