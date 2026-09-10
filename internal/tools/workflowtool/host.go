package workflowtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tools/agenttool"
	"github.com/icehunter/conduit/internal/workflow"
)

// LoopHost runs workflow agents as sub-agents of an agent.Loop.
type LoopHost struct {
	Loop *agent.Loop
	// Agents resolves opts.agentType; nil disables agentType.
	Agents agenttool.Registry
	// Cwd is the session working directory used for saved-workflow lookup,
	// relative scriptPaths and worktree creation.
	Cwd func() string
}

// subagentPrompt tells the child its final text is data, not prose.
const subagentPrompt = "You are a workflow node. Your final message IS the return value consumed by code, not a human-facing reply: return raw data with no preamble or commentary."

// RunAgent implements workflow.Host.
func (h *LoopHost) RunAgent(ctx context.Context, req workflow.AgentRequest) workflow.AgentResult {
	spec := agent.SubAgentSpec{
		SystemPrompt:     subagentPrompt,
		OutputSchema:     req.Schema,
		Model:            req.Model,
		Mode:             permissions.ModeBypassPermissions,
		DisableModeTools: true,
	}
	if req.AgentType != "" {
		if h.Agents == nil {
			return workflow.AgentResult{Err: fmt.Errorf("agentType %q: no agent registry", req.AgentType)}
		}
		def := h.Agents.FindAgent(req.AgentType)
		if def == nil {
			return workflow.AgentResult{Err: fmt.Errorf("unknown agentType %q", req.AgentType)}
		}
		spec.SystemPrompt = def.SystemPrompt + "\n\n" + subagentPrompt
		if spec.Model == "" {
			spec.Model = def.Model
		}
		spec.Role = def.Role
		spec.Tools = def.Tools
	}

	var wt *workflow.Worktree
	if req.Isolation == "worktree" {
		var err error
		wt, err = workflow.NewWorktree(ctx, h.cwd(), "wf", req.Seq)
		if err != nil {
			return workflow.AgentResult{Err: err}
		}
		spec.Cwd = wt.Path
	}

	res, err := h.Loop.RunSubAgentTyped(ctx, req.Prompt, spec)
	out := workflow.AgentResult{InputTokens: res.Usage.InputTokens + res.Usage.CacheReadInputTokens + res.Usage.CacheCreationInputTokens, OutputTokens: res.Usage.OutputTokens}

	if wt != nil {
		kept, ferr := wt.Finish(context.WithoutCancel(ctx))
		if ferr != nil && err == nil {
			err = ferr
		}
		if kept != "" && err == nil {
			// The script's reviewer needs to know where the edit lives.
			out.Note = "worktree kept at " + kept
		}
	}
	if err != nil {
		out.Err = err
		return out
	}
	if res.OutputError != "" {
		out.Err = errors.New("output did not satisfy schema: " + res.OutputError)
		return out
	}
	if req.Schema != nil {
		var v any
		if uerr := json.Unmarshal(res.Output, &v); uerr != nil {
			out.Err = fmt.Errorf("output is not valid JSON: %w", uerr)
			return out
		}
		out.Value = v
		return out
	}
	out.Value = res.Text
	return out
}

// ResolveWorkflow implements workflow.Host.
func (h *LoopHost) ResolveWorkflow(_ context.Context, ref workflow.WorkflowRef) (string, error) {
	if ref.ScriptPath != "" {
		p := ref.ScriptPath
		if !filepath.IsAbs(p) {
			p = filepath.Join(h.cwd(), p)
		}
		b, err := os.ReadFile(p) //nolint:gosec // script path chosen by the running workflow
		if err != nil {
			return "", fmt.Errorf("workflow(): %w", err)
		}
		return string(b), nil
	}
	saved := workflow.Find(h.cwd(), ref.Name)
	if saved == nil {
		return "", fmt.Errorf("workflow(): unknown workflow %q", ref.Name)
	}
	b, err := os.ReadFile(saved.Path) //nolint:gosec // discovered under the user's config dirs
	if err != nil {
		return "", fmt.Errorf("workflow(): %w", err)
	}
	return string(b), nil
}

func (h *LoopHost) cwd() string {
	if h.Cwd != nil {
		if d := h.Cwd(); d != "" {
			return d
		}
	}
	d, _ := os.Getwd()
	return d
}
