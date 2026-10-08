// Package shellsafe is the single source of truth for classifying whether a
// shell command is read-only (safe to auto-approve) or contains constructs
// that could mutate state or execute arbitrary code.
//
// It replaces three independent hand-rolled byte scanners that previously lived
// in internal/permissions and internal/tools/bashtool. Those scanners bailed on
// any metacharacter they didn't special-case, so every new shape of compound
// command (e.g. `cd x && ls 2>/dev/null`) regressed and had to be patched
// individually. This package instead parses the command into a real bash AST
// (mvdan.cc/sh/v3/syntax) and classifies it structurally, so compound shapes
// are handled by grammar rather than by adding the next special case.
//
// bashtool executes commands via real `bash -c`, so modeling real bash grammar
// is the correct contract.
package shellsafe

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// parse returns the parsed bash AST for cmd. A parse failure means the input is
// not well-formed shell we can reason about; callers treat that conservatively
// (not read-only / unsafe).
func parse(cmd string) (*syntax.File, bool) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil, false
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, false
	}
	return f, true
}

// HasUnsafeConstructs reports whether cmd contains any construct that could
// chain to, redirect into, or execute additional commands — i.e. it is NOT a
// single bare command safe to inspect. It is the structural successor to
// bashtool.hasShellMetachars.
//
// Returns true (unsafe) for: command separators (; && || | &), command and
// process substitution ($() “ <() >()), any redirection to a real file, and
// here-documents. Discard redirects (>/dev/null, 2>/dev/null) and fd-dups
// (2>&1) are considered safe. A parse failure returns true (conservative).
func HasUnsafeConstructs(cmd string) bool {
	f, ok := parse(cmd)
	if !ok {
		return true
	}
	// A single bare command parses to exactly one top-level statement whose
	// command is a CallExpr (or a bare assignment). More than one statement, or
	// any binary/compound/subshell construct, is unsafe for the bashtool guard.
	if len(f.Stmts) != 1 {
		return true
	}
	stmt := f.Stmts[0]
	if _, isCall := stmt.Cmd.(*syntax.CallExpr); !isCall {
		return true
	}
	if stmt.Background || stmt.Coprocess {
		return true
	}
	for _, r := range stmt.Redirs {
		if !isSafeRedirect(r) {
			return true
		}
	}
	return hasSubstitution(f)
}

// IsReadOnly reports whether cmd is composed entirely of read-only commands
// that may run without a permission prompt.
//
// It parses the command rather than matching against its text, and accepts
// only literal words: no redirects (2>/dev/null and 2>&1 included), pipes,
// background jobs, assignments, declarations, expansions ($VAR, $(...),
// $((...)), {a,b}) or backslash escapes. Statements chained with &&, || or
// ; and subshells pass only when every part passes, and a `cd <dir>` is
// allowed only as the leading command. Each command must match an entry in
// safeCommands, which also polices the flags and operands it is given; see
// readOnlyCheck. A parse failure returns false (conservative).
func IsReadOnly(cmd string) bool {
	f, ok := parse(cmd)
	if !ok {
		return false
	}
	var c readOnlyCheck
	return c.stmts(f.Stmts)
}

// IsFileDump reports whether cmd's primary purpose is to dump file contents to
// stdout. Commands in this set (cat, head, tail, less, more, bat) are blocked
// in plan mode — the model must use the Read tool instead.
// Compound commands (pipelines, &&, ||) and parse failures return false so only
// direct, unambiguous file-dump invocations are blocked.
func IsFileDump(cmd string) bool {
	f, ok := parse(cmd)
	if !ok {
		return false
	}
	if len(f.Stmts) != 1 {
		return false
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	return isFileDumpBinary(call.Args[0].Lit())
}

// ContainsFileDump reports whether cmd invokes a file-dump binary (cat, head,
// tail, less, more, bat) anywhere in its structure, including inside chains
// (&&, ||, ;) and pipelines. Unlike IsFileDump, this walks the whole AST so a
// `cd dir && cat file` cannot slip a file-content read past plan mode's
// Read-tool-only policy just by prefixing it with another command. A parse
// failure returns false (conservative — falls back to other checks).
func ContainsFileDump(cmd string) bool {
	f, ok := parse(cmd)
	if !ok {
		return false
	}
	found := false
	syntax.Walk(f, func(n syntax.Node) bool {
		if found {
			return false
		}
		if call, isCall := n.(*syntax.CallExpr); isCall && len(call.Args) > 0 {
			if isFileDumpBinary(call.Args[0].Lit()) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// isFileDumpBinary reports whether bin (a command's argv[0]) is a binary whose
// purpose is dumping file contents to stdout.
func isFileDumpBinary(arg string) bool {
	switch filepath.Base(arg) {
	case "cat", "head", "tail", "less", "more", "bat":
		return true
	}
	return false
}

// StripLeadingCd removes a leading `cd <dir> &&` (or `cd <dir>;`) prefix and
// returns the remaining command. ok is false when there is no such prefix.
// Used to normalize rule matching so a `cd subdir && <cmd>` is matched against
// the rule for `<cmd>`. The successor to permissions.normalizeBashPermissionInput.
func StripLeadingCd(cmd string) (string, bool) {
	f, ok := parse(cmd)
	if !ok {
		return "", false
	}
	if len(f.Stmts) != 1 {
		return "", false
	}
	bin, ok := f.Stmts[0].Cmd.(*syntax.BinaryCmd)
	if !ok {
		return "", false
	}
	if bin.Op != syntax.AndStmt && bin.Op != syntax.OrStmt {
		return "", false
	}
	call, ok := bin.X.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 2 {
		return "", false
	}
	if call.Args[0].Lit() != "cd" {
		return "", false
	}
	rest := printNode(cmd, bin.Y)
	if rest == "" {
		return "", false
	}
	return rest, true
}

// isSafeRedirect reports whether a redirect only discards output to /dev/null
// or dups a file descriptor (2>&1), neither of which writes to a real file.
func isSafeRedirect(r *syntax.Redirect) bool {
	switch r.Op {
	case syntax.DplOut, syntax.DplIn:
		// fd duplication: 2>&1, <&3 — no file written.
		return true
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll:
		// >, >>, &> — safe only if the target is /dev/null.
		if r.Word == nil {
			return false
		}
		return r.Word.Lit() == "/dev/null"
	case syntax.RdrIn:
		// < input redirection reads a file; harmless for read-only inspection.
		return true
	}
	// Here-docs, here-strings, <> read-write: treat as unsafe.
	return false
}

// hasSubstitution reports whether the AST contains command substitution,
// process substitution, or backquotes anywhere — all of which can run
// arbitrary commands.
func hasSubstitution(f *syntax.File) bool {
	found := false
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			found = true
			return false
		}
		return !found
	})
	return found
}

// printNode renders an AST node back to source text using the original command
// as the substrate. It uses the node's byte offsets, which are exact for the
// parser we use.
func printNode(src string, n syntax.Node) string {
	start := n.Pos().Offset()
	end := n.End().Offset()
	if start > end || end > uint(len(src)) {
		return ""
	}
	return strings.TrimSpace(src[start:end])
}
