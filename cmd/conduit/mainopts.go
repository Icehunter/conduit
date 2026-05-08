package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/app"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/claudemd"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/memdir"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/tools/agenttool"
	"github.com/icehunter/conduit/internal/tools/skilltool"
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
	_ = memdir.EnsureDir(cwd)
	mem := memdir.BuildPrompt(cwd)
	claudeMdFiles, _ := claudemd.Load(cwd)
	claudeMdPrompt := claudemd.BuildPrompt(claudeMdFiles)
	c := app.NewAPIClient(p, Version)
	reg := app.BuildRegistry(c, nil, lsp.NewManager(), nil, nil)
	modelName := internalmodel.Resolve()

	// Non-interactive mode: create a gate with default mode and no trusted roots.
	// AskPermission auto-denies anything that is not on an allow list — there is
	// no user present to respond to a prompt.
	gate := permissions.New(cwd, nil, permissions.ModeDefault, nil, nil, nil)

	lp := agent.NewLoop(c, reg, agent.LoopConfig{
		Model:               modelName,
		MaxTokens:           internalmodel.MaxTokens,
		System:              agent.BuildSystemBlocks(mem, claudeMdPrompt, skillEntries...),
		Metadata:            app.BuildMetadata(),
		MaxTurns:            50,
		Gate:                gate,
		AskPermission:       func(_ context.Context, _, _ string) (bool, bool) { return false, false },
		BackgroundModel:     func() string { return compact.DefaultModel },
		IsOAuthSubscription: auth.InferAccountKind(p) == auth.AccountKindClaudeAI,
	})
	agentRegistry := plugins.NewAgentRegistry(loadedPlugins)
	reg.Register(agenttool.New(
		func(ctx context.Context, prompt string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
				Mode: permissions.ModeBypassPermissions,
			})
			return r.Text, err
		},
		agentRegistry,
		func(ctx context.Context, prompt, systemPrompt, model string, tools []string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
				SystemPrompt: systemPrompt,
				Model:        model,
				Tools:        tools,
			})
			return r.Text, err
		},
	))
	reg.Register(skilltool.New(
		plugins.NewSkillLoader(loadedPlugins),
		lp.RunBackgroundAgent,
		func(ctx context.Context, prompt string, tools []string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{Tools: tools})
			return r.Text, err
		},
	))

	_, err = lp.Run(ctx, []api.Message{{
		Role:    "user",
		Content: []api.ContentBlock{{Type: "text", Text: prompt}},
	}}, func(ev agent.LoopEvent) {
		if ev.Type == agent.EventText {
			fmt.Print(ev.Text)
		}
	})
	if err != nil {
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
