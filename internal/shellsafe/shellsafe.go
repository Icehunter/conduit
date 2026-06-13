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

// IsReadOnly reports whether cmd is composed entirely of read-only inspection
// commands, possibly chained with &&/||/|/; and an optional leading `cd <dir>`.
// Redirects are permitted only when they discard to /dev/null or dup an fd.
// Command/process substitution, here-docs, and any non-allowlisted binary make
// the verdict false. A parse failure returns false (conservative).
func IsReadOnly(cmd string) bool {
	f, ok := parse(cmd)
	if !ok {
		return false
	}
	if hasSubstitution(f) {
		return false
	}

	readOnly := true
	syntax.Walk(f, func(n syntax.Node) bool {
		if !readOnly {
			return false
		}
		switch x := n.(type) {
		case *syntax.Stmt:
			if x.Background || x.Coprocess {
				readOnly = false
				return false
			}
			for _, r := range x.Redirs {
				if !isSafeRedirect(r) {
					readOnly = false
					return false
				}
			}
		case *syntax.CallExpr:
			if !isReadOnlyCall(x) {
				readOnly = false
				return false
			}
		}
		return true
	})
	return readOnly
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

// isReadOnlyCall reports whether a single CallExpr (a "simple command") invokes
// a binary known to be read-only with the given arguments.
func isReadOnlyCall(c *syntax.CallExpr) bool {
	if len(c.Args) == 0 {
		// Bare assignment (a=b) with no command: harmless, read-only.
		return true
	}
	fields := make([]string, 0, len(c.Args))
	for _, w := range c.Args {
		lit := w.Lit()
		if lit == "" {
			// Non-literal argument (expansion, quoting we can't flatten);
			// fields[0] still matters for the binary name, but for args we
			// pass through — only specific binaries inspect their args below.
			lit = "\x00"
		}
		fields = append(fields, lit)
	}
	bin := filepath.Base(fields[0])
	switch bin {
	case "ls", "ll", "la", "dir", "cat", "bat", "less", "more", "head", "tail",
		"echo", "printf", "pwd", "which", "type", "whereis", "wc", "du", "df",
		"stat", "file", "uname", "hostname", "whoami", "id", "date", "uptime",
		"ps", "env", "printenv", "diff", "cmp", "grep", "egrep", "fgrep", "rg",
		"ag", "sort", "uniq", "cut", "awk", "jq", "yq", "tree", "realpath",
		"basename", "dirname", "cd", "true", "false", "test":
		return true
	case "find", "fd":
		// -delete, -exec*, -ok* execute or mutate; -fprint* write to files.
		return !containsAnyArg(fields[1:],
			"-delete", "-exec", "-execdir", "-ok", "-okdir",
			"-fprint", "-fprint0", "-fprintf", "-printf")
	case "sed":
		// -i / --in-place edits files; the `e` flag in s/// executes shell.
		// Treat any sed command as non-readonly — detecting s///e inline is
		// too fragile without a full sed parser.
		return false
	case "git":
		return len(fields) >= 2 && map[string]bool{
			"log": true, "status": true, "diff": true, "show": true, "blame": true,
			"branch": true, "tag": true, "remote": true, "stash": true,
			"describe": true, "rev-parse": true, "ls-files": true,
			"shortlog": true, "reflog": true, "config": true,
		}[fields[1]]
	case "go":
		return len(fields) >= 2 && map[string]bool{"version": true, "env": true, "list": true, "doc": true, "vet": true}[fields[1]]
	case "npm":
		return len(fields) >= 2 && map[string]bool{"list": true, "ls": true, "outdated": true, "audit": true, "info": true, "view": true}[fields[1]]
	}
	return false
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

func containsAnyArg(fields []string, values ...string) bool {
	for _, f := range fields {
		for _, v := range values {
			if f == v {
				return true
			}
		}
	}
	return false
}
