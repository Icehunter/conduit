package shellsafe

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// readOnlyCheck walks a parsed command and decides whether every part of it
// is read-only. It fails closed at every step: a parse surprise, an
// unrecognized node, a redirection, a background or negated statement, an
// assignment, a word that is not fully literal, or an argv that does not
// match a safeCommands entry all make the verdict false, which costs the
// user a permission prompt and nothing more.
type readOnlyCheck struct {
	// sawCommand records that a simple command has been accepted, so a
	// later `cd` is no longer the leading command.
	sawCommand bool
}

// stmts reports whether every statement in a list is read-only. An empty
// list is not: a command that resolves to nothing is not something to wave
// through, and it is the shape a parse surprise tends to take.
func (c *readOnlyCheck) stmts(stmts []*syntax.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	for _, s := range stmts {
		if !c.stmt(s) {
			return false
		}
	}
	return true
}

// stmt reports whether a single statement is read-only.
func (c *readOnlyCheck) stmt(s *syntax.Stmt) bool {
	if s == nil || s.Cmd == nil {
		return false
	}
	// Any redirection is refused, /dev/null included: echo and printf are
	// only read-only because they cannot be pointed at a file, and an fd dup
	// such as 2>&1 buys nothing worth auditing a redirect parser for.
	// Backgrounding detaches the command from the run being observed.
	if len(s.Redirs) > 0 || s.Background || s.Coprocess || s.Negated || s.Disown {
		return false
	}
	switch x := s.Cmd.(type) {
	case *syntax.CallExpr:
		return c.call(x)
	case *syntax.BinaryCmd:
		// A chain is judged by its parts: `&&` and `||` are read-only
		// exactly when both sides are. Pipes are refused even between
		// read-only commands, which keeps auto-approval to commands whose
		// whole input is spelled out on the command line.
		if x.Op != syntax.AndStmt && x.Op != syntax.OrStmt {
			return false
		}
		return c.stmt(x.X) && c.stmt(x.Y)
	case *syntax.Subshell:
		// A subshell changes where the commands run, not what they may do.
		return c.stmts(x.Stmts)
	case *syntax.TimeClause:
		// `time` is a shell keyword rather than a command. It only measures
		// what it wraps, so defer to that.
		return x.Stmt != nil && c.stmt(x.Stmt)
	}
	// Declarations (export, declare, local), arithmetic commands, tests,
	// conditionals, loops, blocks and function definitions are all refused.
	return false
}

// call reports whether a simple command is read-only.
func (c *readOnlyCheck) call(call *syntax.CallExpr) bool {
	// `FOO=bar cmd` can steer the command through its environment (PATH,
	// LD_PRELOAD, GIT_*), so it never auto-approves, and a bare assignment
	// is not a read.
	if len(call.Assigns) > 0 || len(call.Args) == 0 {
		return false
	}
	args, ok := literalArgs(call.Args)
	if !ok {
		return false
	}
	leading := !c.sawCommand
	c.sawCommand = true
	if args[0].text == "cd" {
		return leading && literalCd(args)
	}
	return safeArgv(args, 0)
}

// literalCd reports whether args is `cd <dir>` with one plain directory
// operand. A cd is only accepted as the first command of the whole
// invocation, where it sets the directory everything after it reads from.
func literalCd(args []word) bool {
	if len(args) != 2 {
		return false
	}
	dir := args[1]
	return !dir.glob && dir.text != "" && !strings.HasPrefix(dir.text, "-")
}

// safeArgv reports whether a fully literal argv is a safe command,
// unwrapping command wrappers up to maxWrapperDepth.
func safeArgv(args []word, depth int) bool {
	if len(args) == 0 {
		return false
	}
	if inner, ok := peelWrapper(texts(args)); ok {
		if depth >= maxWrapperDepth {
			return false
		}
		// A glob in the wrapper's own arguments can expand into extra
		// tokens and move where the inner command begins: with files named
		// `5` and `rm` present, `timeout * ls` runs `timeout 5 rm ls`.
		consumed := len(args) - len(inner)
		if slices.ContainsFunc(args[:consumed], func(a word) bool { return a.glob }) {
			return false
		}
		return safeArgv(args[consumed:], depth+1)
	}
	args = stripGitNoPager(args)
	return slices.ContainsFunc(safeCommands, func(sc safeCommand) bool {
		return sc.matches(args)
	})
}

// stripGitNoPager drops git's --no-pager / -P global option, which only
// disables the pager, so `git --no-pager log` is judged as `git log`. Every
// other global option (-c, -C, --exec-path, …) is left in place, where it
// makes the argv match no entry.
func stripGitNoPager(args []word) []word {
	if args[0].text != "git" {
		return args
	}
	for len(args) > 1 && (args[1].text == "--no-pager" || args[1].text == "-P") {
		args = append([]word{args[0]}, args[2:]...)
	}
	return args
}

// peelWrapper strips a leading command wrapper and returns the command it
// would run. The second result is false when argv is not a wrapper, is a
// wrapper with no inner command (`env` alone just prints the environment),
// or carries a flag the wrapper is not known to accept. In each of those
// cases argv falls through to ordinary matching, where no wrapper has an
// entry other than bare `env`.
func peelWrapper(argv []string) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	idx := slices.IndexFunc(commandWrappers, func(w commandWrapper) bool {
		return w.name == argv[0]
	})
	if idx < 0 {
		return nil, false
	}
	w := commandWrappers[idx]

	rest := argv[1:]
	operandsSkipped := 0
	for len(rest) > 0 {
		tok := rest[0]
		switch {
		case tok == "--":
			// Everything after -- is the inner command.
			rest = rest[1:]
			if len(rest) == 0 {
				return nil, false
			}
			return rest, true
		case isFlag(tok):
			n, ok := w.flagTokens(rest)
			if !ok {
				return nil, false
			}
			rest = rest[n:]
		case operandsSkipped < w.skipOperands:
			operandsSkipped++
			rest = rest[1:]
		default:
			// First token that is not part of the wrapper's own
			// arguments: this is the command being wrapped.
			return rest, true
		}
	}
	// Wrapper with no inner command.
	return nil, false
}

// flagTokens reports how many tokens the flag at the head of rest spans,
// and false when it is not a flag this wrapper accepts or its value is
// missing.
func (w commandWrapper) flagTokens(rest []string) (int, bool) {
	tok := rest[0]
	if slices.Contains(w.boolFlags, tok) {
		return 1, true
	}
	if strings.HasPrefix(tok, "--") {
		name, _, attached := strings.Cut(tok, "=")
		if !slices.Contains(w.valueFlags, name) {
			return 0, false
		}
		if attached {
			return 1, true
		}
		return 2, len(rest) >= 2
	}
	// A short value flag takes its value attached (-n10) or as the next
	// token (-n 10).
	if !slices.Contains(w.valueFlags, tok[:2]) {
		return 0, false
	}
	if len(tok) > 2 {
		return 1, true
	}
	return 2, len(rest) >= 2
}

// matches reports whether args is an instance of this safe command form.
func (sc safeCommand) matches(args []word) bool {
	if len(args) < len(sc.argv) {
		return false
	}
	for i, tok := range sc.argv {
		if args[i].text != tok {
			return false
		}
	}
	operandsOnly := false
	sawFlag := false
	operands := 0
	for _, arg := range args[len(sc.argv):] {
		if arg.text == "--" && !sc.flagsAfterDoubleDash {
			operandsOnly = true
			continue
		}
		if !operandsOnly && (isFlag(arg.text) || arg.flagGlob) {
			if arg.glob {
				// The shell picks the flags this expands to, so only a
				// command whose every flag is harmless can take it.
				if !sc.flagsAreInert() {
					return false
				}
			} else if sc.restrictFlags {
				// Matched exactly: an unrecognized spelling, including a
				// cluster of otherwise-allowed short flags or an
				// abbreviation of an allowed long flag, fails closed.
				if !slices.Contains(sc.allowFlags, flagName(arg.text)) {
					return false
				}
			} else if flagDenied(arg.text, sc.denyFlags) {
				return false
			}
			sawFlag = true
			continue
		}
		if !sc.allowOperands {
			return false
		}
		if arg.glob && sc.countsOperands() {
			return false
		}
		if slices.Contains(sc.denyOperands, arg.text) {
			return false
		}
		operands++
		if sc.maxOperands > 0 && operands > sc.maxOperands {
			return false
		}
	}
	return sawFlag || !sc.requireFlag
}

// isFlag reports whether a token is a flag rather than an operand. A lone
// "-" is conventionally stdin, and a lone "--" is a separator; neither is a
// flag.
func isFlag(tok string) bool {
	return len(tok) > 1 && strings.HasPrefix(tok, "-") && tok != "--"
}

// flagDenied reports whether tok is a denied flag, or carries one.
//
// A long flag matches by name, so `--output=x` is caught by an entry of
// "--output". getopt_long and git's parse-options also accept any
// unambiguous prefix of a long option (`sort --outp=x` writes x, and
// `git grep --open-files=cmd` runs cmd), so a long flag that abbreviates a
// denied one is denied too.
//
// A short flag needs more than a name match: getopt accepts its value
// attached (`date -s2020-01-01`) and accepts clusters (`date -us`), so
// neither spelling is equal to "-s". Every character of a single-dash
// token is therefore checked against the deny set.
func flagDenied(tok string, deny []string) bool {
	name := flagName(tok)
	if slices.Contains(deny, name) {
		return true
	}
	if strings.HasPrefix(tok, "--") {
		return slices.ContainsFunc(deny, func(d string) bool {
			return strings.HasPrefix(d, "--") && strings.HasPrefix(d, name)
		})
	}
	for _, r := range tok[1:] {
		if slices.Contains(deny, "-"+string(r)) {
			return true
		}
	}
	return false
}

// flagName strips any =value suffix so `--format=%H` is matched by an
// entry of "--format".
func flagName(tok string) string {
	name, _, _ := strings.Cut(tok, "=")
	return name
}

// word is one argument with its shell quoting removed.
type word struct {
	text string
	// glob reports an unquoted *, ? or [...] that the shell expands against
	// the filesystem, so the argv that runs may hold different words than
	// this one, and more or fewer of them.
	glob bool
	// flagGlob reports a glob whose expansions may begin with "-", which
	// the command would then read as flags: `*` may expand to a file named
	// `--pre=sh`. A glob behind a literal prefix (`src/*.go`) cannot.
	flagGlob bool
}

func texts(args []word) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a.text
	}
	return out
}

// literalArgs converts parsed words to plain words, reporting false if any
// word is not entirely literal.
func literalArgs(words []*syntax.Word) ([]word, bool) {
	out := make([]word, 0, len(words))
	for _, w := range words {
		lit, ok := literalWord(w)
		if !ok {
			return nil, false
		}
		out = append(out, lit)
	}
	return out, true
}

// literalWord flattens a word to its literal text, reporting false if any
// part of it is resolved at runtime.
//
// Command substitution, parameter expansion, arithmetic expansion and
// process substitution are all rejected rather than evaluated: their value
// is not knowable here, and `ls $(rm -rf /)` must never be treated as an
// `ls`. So is brace expansion, which turns one word into several and so
// lets `--{ext-diff,stat}` reach git as a denied flag. Glob characters are
// kept and reported, because the shell expands them against the filesystem
// into words this check never sees; see word.flagGlob.
//
// A literal containing a backslash is also rejected. syntax.Lit.Value keeps
// unquoted escape sequences verbatim, so the text here is not the argv the
// shell will pass: `--ext\-diff` reads as the denied flag `--ext-diff` only
// after the shell strips the backslash, which would let a protected flag
// slip past denyFlags. Failing closed on any backslash is cheaper than
// re-implementing the shell's escape rules.
func literalWord(w *syntax.Word) (word, bool) {
	// SplitBraces replaces each real expansion with a BraceExp part, which
	// the default case below refuses. Its result is not enough on its own:
	// it reports true for any `{`, including `HEAD@{1}`, which bash leaves
	// alone.
	syntax.SplitBraces(w)
	var sb strings.Builder
	// The first unquoted * or ?, and the first unquoted [, by offset in the
	// unquoted text.
	starAt, bracketAt := -1, -1
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if strings.Contains(p.Value, `\`) {
				return word{}, false
			}
			if i := strings.IndexAny(p.Value, "*?"); i >= 0 && starAt < 0 {
				starAt = sb.Len() + i
			}
			if i := strings.IndexByte(p.Value, '['); i >= 0 && bracketAt < 0 {
				bracketAt = sb.Len() + i
			}
			sb.WriteString(p.Value)
		case *syntax.SglQuoted:
			// $'…' applies escape sequences, so Value is not the final
			// text; only plain '…' is taken at face value.
			if p.Dollar {
				return word{}, false
			}
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return word{}, false
			}
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				// Double-quoted literals keep backslash escapes verbatim
				// too; reject them for the same reason as bare literals.
				if !ok || strings.Contains(lit.Value, `\`) {
					return word{}, false
				}
				sb.WriteString(lit.Value)
			}
		default:
			return word{}, false
		}
	}
	text := sb.String()
	// A [ only opens a bracket expression when a ] follows it, so the `[`
	// command and its closing `]` stay literal.
	if bracketAt >= 0 && !strings.Contains(text[bracketAt+1:], "]") {
		bracketAt = -1
	}
	globAt := starAt
	if bracketAt >= 0 && (globAt < 0 || bracketAt < globAt) {
		globAt = bracketAt
	}
	if globAt < 0 {
		return word{text: text}, true
	}
	prefix := text[:globAt]
	return word{
		text:     text,
		glob:     true,
		flagGlob: prefix == "" || strings.HasPrefix(prefix, "-"),
	}, true
}
