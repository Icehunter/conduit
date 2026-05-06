package main

import (
	"flag"
	"fmt"
	"os"
)

// cliFlags holds the parsed top-level CLI flags for conduit.
type cliFlags struct {
	PrintMode    bool
	ContinueMode bool
	ResumeID     string
}

// registerFlags wires all flag.XxxVar calls and sets flag.Usage.
// Call before flag.Parse().
func registerFlags(f *cliFlags) {
	flag.BoolVar(&f.PrintMode, "print", false, "non-interactive: send a one-shot prompt and print the response")
	flag.BoolVar(&f.PrintMode, "p", false, "alias for --print")
	flag.BoolVar(&f.ContinueMode, "continue", false, "resume the most recent conversation for the current directory")
	flag.BoolVar(&f.ContinueMode, "c", false, "alias for --continue")
	flag.StringVar(&f.ResumeID, "resume", "", "resume a specific session (session UUID or path to .jsonl file)")
	flag.StringVar(&f.ResumeID, "r", "", "alias for --resume")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: conduit [version] | conduit --print \"prompt\" | conduit [--continue|--resume <id>] (REPL)")
		fmt.Fprintln(os.Stderr, "       Login and logout are managed via /login and /logout inside the REPL.")
		flag.PrintDefaults()
	}
}
