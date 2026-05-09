// Package main is the conduit entrypoint.
//
// Surface:
//
//	conduit                      Full-screen Bubble Tea TUI.
//	conduit --print "prompt"     One-shot streaming response.
//	conduit version              Print binary version.
//	conduit update               Check GitHub for a newer release.
//	conduit mcp <subcommand>     Manage MCP servers (add/list/get/remove/add-json).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/icehunter/conduit/internal/updater"
)

// AppVersion is the conduit release version shown to users.
// Populated at build time via -ldflags "-X main.AppVersion=$(VERSION)".
// Defaults to "dev" so unreleased builds don't masquerade as a real
// release and don't trigger the update notifier.
var AppVersion = "dev"

// Version is the CC wire version sent in User-Agent/X-App headers.
var Version = "2.1.137"

// GitCommit and BuildTime are stamped at build time.
var GitCommit = "unknown"
var BuildTime = "unknown"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "conduit:", err)
		os.Exit(1)
	}
}

func run() error {
	var f cliFlags
	registerFlags(&f)
	flag.Parse()

	args := flag.Args()
	if f.PrintMode {
		return runPrint(args)
	}
	if len(args) == 0 {
		return runREPL(f.ContinueMode, f.ResumeID)
	}

	switch args[0] {
	case "version":
		fmt.Printf("conduit %s (cc-wire/%s, commit %s, built %s)\n", AppVersion, Version, GitCommit, BuildTime)
		return nil
	case "update":
		return runUpdate()
	case "mcp":
		return runMCPCmd(args[1:])
	default:
		flag.Usage()
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

// runUpdate performs an explicit update check and prints a one-line
// status. Exits 0 on success regardless of whether an update is
// available; exits non-zero only when the check itself failed.
func runUpdate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := updater.New().Check(ctx, AppVersion)
	if err != nil {
		return fmt.Errorf("update check: %w", err)
	}
	if AppVersion == "dev" {
		fmt.Println("conduit dev build — update check skipped")
		return nil
	}
	if !res.HasUpdate {
		fmt.Printf("conduit %s is up to date\n", res.Current)
		return nil
	}
	fmt.Printf("conduit %s is available (current %s)\n", res.Latest, res.Current)
	fmt.Printf("  %s\n", res.UpgradeCmd)
	return nil
}
