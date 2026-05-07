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
	"github.com/icehunter/conduit/internal/claudemd"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/memdir"
	internalmodel "github.com/icehunter/conduit/internal/model"
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

	lp := agent.NewLoop(c, reg, agent.LoopConfig{
		Model:           modelName,
		MaxTokens:       internalmodel.MaxTokens,
		System:          agent.BuildSystemBlocks(mem, claudeMdPrompt, skillEntries...),
		Metadata:        app.BuildMetadata(),
		MaxTurns:        50,
		BackgroundModel: func() string { return compact.DefaultModel },
	})
	reg.Register(agenttool.New(lp.RunBackgroundAgent))
	reg.Register(skilltool.New(plugins.NewSkillLoader(loadedPlugins), lp.RunBackgroundAgent))

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
