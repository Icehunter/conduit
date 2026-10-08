package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/icehunter/conduit/internal/app"
	"github.com/icehunter/conduit/internal/coding"
	"github.com/icehunter/conduit/internal/globalconfig"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

type taskFiles []string

func (f *taskFiles) String() string     { return strings.Join(*f, ",") }
func (f *taskFiles) Set(s string) error { *f = append(*f, s); return nil }

func runCode(args []string) error {
	fs := flag.NewFlagSet("code", flag.ContinueOnError)
	var files taskFiles
	fs.Var(&files, "file", "Repository-relative task file; repeat for each file, including new files")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: conduit code --file path [--file path] \"bounded implementation task\"\nRequires an enabled definitionOfDone and configured implement role. Uses existing permission rules; no interactive approvals.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(files) == 0 || fs.NArg() == 0 {
		return errors.New("code requires --file and a task prompt (see conduit code -h)")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := app.RepositoryRoot(ctx, cwd)
	if err != nil {
		return err
	}
	policies, err := settings.LoadCodingPolicies(root)
	if err != nil {
		return err
	}
	if policies.DefinitionOfDone == nil || !policies.DefinitionOfDone.Enabled {
		return errors.New("code requires definitionOfDone.enabled=true in repository settings")
	}
	merged, err := settings.Load(root)
	if err != nil {
		return err
	}
	store := secure.NewDefault()
	manager := mcp.NewManager()
	manager.SetSecureStore(store)
	defer manager.Close()
	trusted, err := globalconfig.IsTrusted(root)
	if err != nil {
		return err
	}
	if err = manager.ConnectAll(ctx, root, trusted); err != nil {
		return err
	}
	gate := permissions.New(root, nil, permissions.Mode(merged.DefaultMode), merged.Allow, merged.Deny, merged.Ask)
	rt, err := app.NewCodingRuntime(app.CodingOptions{Root: root, SessionID: app.NewSessionID(), Settings: merged, Policies: &policies, Gate: gate, MCP: manager, Secure: store, WireVersion: Version})
	if err != nil {
		return err
	}
	defer func() { _ = rt.Close() }()
	out, err := rt.Run(ctx, coding.Task{Prompt: strings.Join(fs.Args(), " "), Files: files})
	if err != nil {
		return err
	}
	if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
		return err
	}
	if out.Status != "verified" {
		return fmt.Errorf("coding task %s", out.Status)
	}
	return nil
}
