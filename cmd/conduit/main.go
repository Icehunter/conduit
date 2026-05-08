// Package main is the conduit entrypoint.
//
// Surface:
//
//	conduit                      Full-screen Bubble Tea TUI.
//	conduit --print "prompt"     One-shot streaming response.
//	conduit version              Print binary version.
package main

import (
	"flag"
	"fmt"
	"os"
)

// AppVersion is the conduit release version shown to users.
// Populated at build time via -ldflags "-X main.AppVersion=$(VERSION)".
var AppVersion = "1.0.0"

// Version is the CC wire version sent in User-Agent/X-App headers.
var Version = "2.1.133"

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
	default:
		flag.Usage()
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}
