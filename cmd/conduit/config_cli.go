package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/icehunter/conduit/internal/settings/schema"
)

// runConfigCmd dispatches `conduit config <subcommand>`.
func runConfigCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printConfigUsage(stderr)
		return errors.New("config: missing subcommand")
	}
	switch args[0] {
	case "schema":
		return runConfigSchema(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printConfigUsage(stdout)
		return nil
	}
	printConfigUsage(stderr)
	return fmt.Errorf("config: unknown subcommand %q", args[0])
}

func printConfigUsage(w io.Writer) {
	fmt.Fprintf(w, `Usage: conduit config <subcommand>

Subcommands:
  schema [%s]   Print a JSON Schema for a config file (default: conduit).
`, strings.Join(schema.Kinds, "|"))
}

func runConfigSchema(args []string, stdout, stderr io.Writer) error {
	kind := "conduit"
	switch len(args) {
	case 0:
	case 1:
		kind = args[0]
	default:
		printConfigUsage(stderr)
		return errors.New("config schema: too many arguments")
	}
	out, err := schema.Document(kind)
	if err != nil {
		printConfigUsage(stderr)
		return fmt.Errorf("config schema: %w", err)
	}
	if _, err := stdout.Write(out); err != nil {
		return fmt.Errorf("config schema: write: %w", err)
	}
	return nil
}
