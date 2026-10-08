package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/app"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/bgjobs"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/globalconfig"
	"github.com/icehunter/conduit/internal/instructions"
	"github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/memdir"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/team"
	"github.com/icehunter/conduit/internal/tools/agenttool"
	"github.com/icehunter/conduit/internal/tools/skilltool"
	"github.com/icehunter/conduit/internal/tools/workflowtool"
	"github.com/icehunter/conduit/internal/workflow"
)

// runPrint executes a one-shot non-interactive agent run, streaming the
// response text to stdout. Mirrors the --print / -p flag behavior.
func runPrint(args []string) error {
	if len(args) == 0 {
		return errors.New("--print requires a prompt argument")
	}
	prompt := strings.Join(args, " ")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	p, err := app.LoadAuth(ctx)
	if err != nil {
		return fmt.Errorf("authentication: %w (use /login inside the REPL to sign in)", err)
	}

	cwd, _ := os.Getwd()
	loadedPlugins, _ := plugins.LoadAll(cwd)
	skillEntries := app.BuildSkillEntries(loadedPlugins)
	agentEntries := app.BuildAgentEntries(loadedPlugins)
	_ = memdir.EnsureDir(cwd)
	mem := memdir.BuildPrompt(cwd)
	claudeMdFiles, _ := instructions.Load(cwd)
	claudeMdPrompt := instructions.BuildPrompt(claudeMdFiles)
	c := app.NewAPIClient(p, Version)
	reg := app.BuildRegistry(c, nil, lsp.NewManager(), nil, nil)
	modelName := internalmodel.Resolve()

	// Non-interactive mode: create a gate with default mode and no trusted roots.
	// AskPermission auto-denies anything that is not on an allow list — there is
	// no user present to respond to a prompt.
	gate := permissions.New(cwd, nil, permissions.ModeDefault, nil, nil, nil)

	// Print mode runs no tool hooks, but a UserPromptSubmit hook still gates
	// the prompt. Project-local hooks need the workspace to be trusted.
	var promptHooks *settings.HooksSettings
	if merged, err := settings.Load(cwd); err == nil && merged != nil {
		trusted, trustErr := globalconfig.IsTrusted(cwd)
		all := settings.FilterUntrustedHooks(plugins.MergeHooksFrom(loadedPlugins, &merged.Hooks), cwd, trustErr == nil && trusted)
		promptHooks = &settings.HooksSettings{UserPromptSubmit: all.UserPromptSubmit}
	}

	lp := agent.NewLoop(c, reg, agent.LoopConfig{
		Model:     modelName,
		MaxTokens: internalmodel.MaxTokens,
		System:    agent.BuildSystemBlocks(mem, claudeMdPrompt, memdir.Path(cwd), agentEntries, skillEntries...),
		Metadata:  app.BuildMetadata(),
		// Re-read the memory directory each turn so anything extracted during
		// this run reaches the model, not just the next process.
		RebuildSystem: func() []api.SystemBlock {
			return agent.BuildSystemBlocks(memdir.BuildPrompt(cwd), claudeMdPrompt, memdir.Path(cwd), agentEntries, skillEntries...)
		},
		MaxTurns:            50,
		Gate:                gate,
		Hooks:               promptHooks,
		Cwd:                 cwd,
		SessionID:           api.NewUUID(),
		AskPermission:       func(_ context.Context, _, _ string) (bool, bool) { return false, false },
		BackgroundModel:     func() string { return compact.DefaultModel },
		IsOAuthSubscription: auth.InferAccountKind(p) == auth.AccountKindClaudeAI,
	})
	agentRegistry := plugins.NewAgentRegistry(loadedPlugins)
	agentRegistry.AddFS(cwd)
	reg.Register(agenttool.New(
		func(ctx context.Context, prompt string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
				Mode:      permissions.ModeBypassPermissions,
				AgentType: taskAgentType(""),
			})
			return r.Text, err
		},
		agentRegistry,
		func(ctx context.Context, prompt, systemPrompt, model, role string, tools []string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
				SystemPrompt: systemPrompt,
				Model:        model,
				Role:         role,
				Tools:        tools,
				AgentType:    taskAgentType(systemPrompt),
			})
			return r.Text, err
		},
	).WithSpawnTeammate(func(ctx context.Context, name, prompt string) (string, error) {
		return lp.SpawnTeammate(ctx, name, prompt, agent.SubAgentSpec{}, team.Default)
	}).WithOutputContract(func(ctx context.Context, prompt, systemPrompt, model, role string, tools []string, schema json.RawMessage) (string, error) {
		r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
			SystemPrompt: systemPrompt,
			Model:        model,
			Role:         role,
			Tools:        tools,
			OutputSchema: schema,
			AgentType:    taskAgentType(systemPrompt),
		})
		if err != nil {
			return "", err
		}
		if r.OutputError != "" {
			return "", fmt.Errorf("sub-agent did not satisfy output_schema after a retry: %s", r.OutputError)
		}
		return r.Text, nil
	}))
	reg.Register(workflowtool.New(workflowtool.Config{
		Host:      &workflowtool.LoopHost{Loop: lp, Agents: agentRegistry, Cwd: func() string { return cwd }},
		Notify:    lp.InjectMessage,
		Cwd:       func() string { return cwd },
		RunsDir:   filepath.Join(session.ProjectDirInConfig(cwd, settings.ConduitDir()), "workflows"),
		Ultracode: workflow.Ultracode.Load,
	}))
	defer workflow.Default.KillAll()
	// Print mode has no idle session to wake: finished jobs are queued for
	// the run's next delivery point.
	bgjobs.Default.SetHeadless(true)
	bgjobs.Default.SetOnDone(func(j bgjobs.Job) { lp.InjectMessage(bgjobs.Default.Notification(j)) })
	defer bgjobs.Default.KillAll()
	reg.Register(skilltool.New(
		plugins.NewSkillLoader(loadedPlugins, cwd),
		lp.RunBackgroundSubAgent,
		func(ctx context.Context, prompt string, tools []string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{Tools: tools})
			return r.Text, err
		},
	))

	_, err = lp.RunPrompt(ctx, agent.UserPrompt{Raw: prompt}, []api.Message{{
		Role:    "user",
		Content: []api.ContentBlock{{Type: "text", Text: prompt}},
	}}, func(ev agent.LoopEvent) {
		switch ev.Type {
		case agent.EventText:
			fmt.Print(ev.Text)
		case agent.EventNotice:
			fmt.Fprintln(os.Stderr, ev.Notice) // hook messages and warnings
		}
	})
	var blocked *agent.PromptBlockedError
	if err != nil && !errors.As(err, &blocked) {
		fmt.Println()
	}
	return err
}

// thinkingBudget returns the token budget for extended thinking from the
// CLAUDE_THINKING_BUDGET env var. 0 means thinking is disabled.
func thinkingBudget() int {
	if v := os.Getenv("CLAUDE_THINKING_BUDGET"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// taskAgentType is the x-claude-code-agent-type for a Task sub-agent: a
// registry agent supplies its own system prompt and is "custom"; a plain Task
// call is CC's built-in general-purpose agent.
func taskAgentType(systemPrompt string) string {
	if systemPrompt != "" {
		return "custom"
	}
	return "general-purpose"
}
