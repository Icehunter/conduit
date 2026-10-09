package shellsafe

import (
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// AllowedUnderImplGuard reports whether cmd may run while the implement role
// owns file changes. It is an allowlist: a command passes only when it is
// read-only per IsReadOnly, or when every segment is a read-only command, a
// leading `cd <dir>`, or an allowlisted test/build/lint runner (see
// runnerOK). Segments may be chained with &&, ||, ; and pipes.
//
// Everything else fails closed: redirects other than to /dev/null or fd
// dups, heredocs, here-strings, command and process substitution, variable
// assignments, backgrounding, expansions, inline-script flags, and any
// command not named exactly (lowercase, no path) in the allowlist.
func AllowedUnderImplGuard(cmd string) bool {
	if IsReadOnly(cmd) {
		return true
	}
	f, ok := parse(cmd)
	if !ok {
		return false
	}
	var c guardCheck
	return c.stmts(f.Stmts)
}

// AllowedUnderImplGuardPowerShell is the PowerShell (Shell tool) counterpart
// of AllowedUnderImplGuard. A command is a single simple command made only of
// plain words and single-quoted strings; any operator, variable, subexpression
// or redirect rejects it. It must be one of the allowlisted runners or a
// read-only cmdlet.
func AllowedUnderImplGuardPowerShell(cmd string) bool {
	args, ok := tokenizePowerShell(cmd)
	if !ok || len(args) == 0 {
		return false
	}
	name := strings.TrimSuffix(strings.ToLower(args[0].text), ".exe")
	switch name {
	case "get-childitem", "gci", "ls", "dir", "get-content", "gc", "cat",
		"select-string", "sls", "get-location", "gl", "pwd":
		return true
	}
	args[0].text = name
	if name == "git" {
		// git is judged by the read-only classifier, which allows only
		// status, diff, log, show and the like, never a leading -c.
		return safeArgv(args, 0)
	}
	return runnerOK(args)
}

// tokenizePowerShell splits a PowerShell command into words, accepting only
// plain words (letters, digits and a few path characters) and single-quoted
// literals. It reports false on anything else, including every operator.
func tokenizePowerShell(cmd string) ([]word, bool) {
	if !isASCII(cmd) {
		return nil, false // smart quotes and the like can close or open strings
	}
	var out []word
	r := []rune(strings.TrimSpace(cmd))
	for i := 0; i < len(r); {
		switch c := r[i]; {
		case c == ' ' || c == '\t':
			i++
		case c == '\'':
			j := i + 1
			for j < len(r) && r[j] != '\'' {
				j++
			}
			if j >= len(r) {
				return nil, false
			}
			if j+1 < len(r) && r[j+1] != ' ' && r[j+1] != '\t' {
				return nil, false // adjacent text or an escaped '' quote
			}
			out = append(out, word{text: string(r[i+1 : j])})
			i = j + 1
		default:
			j := i
			glob := false
			for j < len(r) && r[j] != ' ' && r[j] != '\t' {
				switch {
				case r[j] == '*' || r[j] == '?':
					glob = true
				case !isPSWordRune(r[j]):
					return nil, false
				}
				j++
			}
			out = append(out, word{text: string(r[i:j]), glob: glob, flagGlob: glob})
			i = j
		}
	}
	return out, true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isPSWordRune(c rune) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.ContainsRune("_./\\:=+-", c)
}

// guardCheck walks a parsed command for AllowedUnderImplGuard.
type guardCheck struct {
	sawCommand bool
}

func (c *guardCheck) stmts(stmts []*syntax.Stmt) bool {
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

func (c *guardCheck) stmt(s *syntax.Stmt) bool {
	if s == nil || s.Cmd == nil || s.Background || s.Coprocess || s.Negated || s.Disown {
		return false
	}
	for _, r := range s.Redirs {
		if !discardRedirect(r) {
			return false
		}
	}
	switch x := s.Cmd.(type) {
	case *syntax.CallExpr:
		return c.call(x)
	case *syntax.BinaryCmd:
		switch x.Op {
		case syntax.AndStmt, syntax.OrStmt, syntax.Pipe, syntax.PipeAll:
			return c.stmt(x.X) && c.stmt(x.Y)
		}
	case *syntax.Subshell:
		return c.stmts(x.Stmts)
	}
	return false
}

func (c *guardCheck) call(call *syntax.CallExpr) bool {
	if isEchoStatus(call) {
		return true
	}
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
	if args[0].text == "sed" {
		return sedPrintOK(args)
	}
	if isRunner(args[0].text) {
		return runnerOK(args)
	}
	return safeArgv(args, 0)
}

// discardRedirect reports whether a redirect only discards output to
// /dev/null or duplicates/closes a file descriptor.
func discardRedirect(r *syntax.Redirect) bool {
	if r.Word == nil {
		return false
	}
	t := r.Word.Lit()
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob:
		return t == "/dev/null"
	case syntax.DplOut, syntax.DplIn:
		return t == "-" || (t != "" && strings.Trim(t, "0123456789") == "")
	}
	return false
}

var runnerNames = []string{"make", "go", "npm", "pnpm", "yarn", "npx", "cargo", "pytest", "python", "python3", "golangci-lint"}

func isRunner(name string) bool { return slices.Contains(runnerNames, name) }

// isEchoStatus reports whether call is exactly `echo $?`, the one expansion
// allowed (it prints the previous exit status and cannot run anything).
func isEchoStatus(call *syntax.CallExpr) bool {
	if len(call.Assigns) > 0 || len(call.Args) != 2 || call.Args[0].Lit() != "echo" || len(call.Args[1].Parts) != 1 {
		return false
	}
	pe, ok := call.Args[1].Parts[0].(*syntax.ParamExp)
	return ok && pe.Param != nil && pe.Param.Value == "?" &&
		!pe.Excl && !pe.Length && !pe.Width && pe.Index == nil && pe.Slice == nil && pe.Repl == nil && pe.Exp == nil
}

var sedPrintRe = regexp.MustCompile(`^([0-9]+|\$)(,([0-9]+|\$))?p$`)

// sedPrintOK accepts only `sed -n '<addr>p' file...`, where addr is a line
// number or $ or a range of them. Any other script can write (w, e, s///w).
func sedPrintOK(args []word) bool {
	if len(args) < 4 || args[1].text != "-n" || !sedPrintRe.MatchString(args[2].text) {
		return false
	}
	for _, a := range args[2:] {
		if a.glob {
			return false
		}
	}
	for _, f := range args[3:] {
		if f.text == "" || isFlag(f.text) {
			return false
		}
	}
	return true
}

// runnerOK reports whether args (args[0] already matched exactly, lowercase,
// without a path) is an allowlisted test/build/lint invocation. Globs are
// refused outright because their expansion can inject flags.
func runnerOK(args []word) bool {
	if len(args) == 0 || slices.ContainsFunc(args, func(a word) bool { return a.glob || !isASCII(a.text) }) {
		return false
	}
	at := func(i int) string {
		if i < len(args) {
			return args[i].text
		}
		return ""
	}
	switch args[0].text {
	case "make":
		return makeOK(args[1:])
	case "go":
		return goOK(args)
	case "npm":
		switch at(1) {
		case "test", "lint", "typecheck":
			return flagsAllowed(args[2:], scriptSpec)
		case "run", "run-script":
			return targetNameOK(at(2)) && flagsAllowed(args[3:], scriptSpec)
		}
	case "pnpm", "yarn":
		switch at(1) {
		case "run", "run-script":
			return targetNameOK(at(2)) && flagsAllowed(args[3:], scriptSpec)
		}
		return targetNameOK(at(1)) && flagsAllowed(args[2:], scriptSpec)
	case "npx":
		switch at(1) {
		case "vitest":
			v := jestSpec
			v.positional = vitestPositional
			return flagsAllowed(args[2:], v)
		case "jest":
			return flagsAllowed(args[2:], jestSpec)
		case "eslint":
			return flagsAllowed(args[2:], eslintSpec)
		case "tsc":
			// tsc emits JavaScript next to the sources unless told not to.
			return slices.ContainsFunc(args[2:], func(a word) bool { return a.text == "--noEmit" }) &&
				flagsAllowed(args[2:], tscSpec)
		}
	case "cargo":
		switch at(1) {
		case "test", "check", "build", "clippy":
			return flagsAllowed(args[2:], cargoSpec)
		}
	case "pytest":
		return flagsAllowed(args[1:], pytestSpec)
	case "python", "python3":
		if at(1) == "-m" && (at(2) == "pytest" || at(2) == "unittest") {
			return flagsAllowed(args[3:], pytestSpec)
		}
	case "golangci-lint":
		if at(1) == "run" {
			return flagsAllowed(args[2:], golangciSpec)
		}
	}
	return false
}

// targetDenyWords mark make targets and package scripts that write files or
// change dependencies, whatever else their name says (test-cover, lint:fix).
var targetDenyWords = []string{"fix", "fmt", "format", "write", "cover", "update", "install", "clean", "generate", "gen", "tidy"}

var targetCharsRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// targetNameOK reports whether a make target or package script name is a
// plain test, check or build target. Names are an allowlist because the
// target's recipe is arbitrary and the name is the only signal.
func targetNameOK(name string) bool {
	// Plain characters only: no "=" (make evaluates NAME=value assignments,
	// even on the end of a target), no quotes, no $.
	if !targetCharsRe.MatchString(name) {
		return false
	}
	n := strings.ToLower(name)
	for _, w := range targetDenyWords {
		if strings.Contains(n, w) {
			return false
		}
	}
	switch n {
	case "test", "check", "verify", "lint", "vet", "typecheck", "type-check", "build":
		return true
	}
	return strings.HasPrefix(n, "test-") || strings.HasPrefix(n, "test:")
}

var makeFlagRe = regexp.MustCompile(`^(-[ksnBiqw]+|-j[0-9]*|--(keep-going|silent|quiet|just-print|dry-run|no-print-directory)|--jobs=[0-9]+)$`)

// makeOK accepts allowlisted target names and a few harmless flags.
// Variable assignments (FOO=bar), -f/-C/-e and every other flag are refused.
func makeOK(rest []word) bool {
	for _, a := range rest {
		if isFlag(a.text) {
			if !makeFlagRe.MatchString(a.text) {
				return false
			}
			continue
		}
		if !targetNameOK(a.text) {
			return false
		}
	}
	return true
}

var goSubcommands = []string{"test", "vet", "build", "list", "version", "env"}

func goOK(args []word) bool {
	if len(args) < 2 || !slices.Contains(goSubcommands, args[1].text) {
		return false
	}
	rest := args[2:]
	if !goModFuzzOK(rest) {
		return false
	}
	if args[1].text == "build" {
		var devNull bool
		rest, devNull = dropDevNullOutput(rest)
		// A single main package writes a binary into the working directory,
		// which only a ./... pattern (or discarding the output) avoids.
		if !devNull && !allEllipsisPatterns(rest) {
			return false
		}
	}
	return flagsOK(rest)
}

// goModFuzzOK refuses -fuzz* and any -mod value other than readonly/vendor
// (-mod=mod rewrites go.mod and go.sum).
func goModFuzzOK(rest []word) bool {
	for i, a := range rest {
		if !isFlag(a.text) {
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a.text, "-"), "=")
		name = strings.TrimPrefix(strings.ToLower(name), "test.")
		if strings.HasPrefix(name, "fuzz") {
			return false
		}
		if name != "mod" {
			continue
		}
		if !hasVal {
			if i+1 >= len(rest) {
				return false
			}
			val = rest[i+1].text
		}
		if val != "readonly" && val != "vendor" {
			return false
		}
	}
	return true
}

// allEllipsisPatterns reports whether every non-flag argument is `./...` (or
// `...`) and there is at least one.
func allEllipsisPatterns(rest []word) bool {
	n := 0
	for _, a := range rest {
		if isFlag(a.text) {
			continue
		}
		if a.text != "./..." && a.text != "..." {
			return false
		}
		n++
	}
	return n > 0
}

// dropDevNullOutput removes `-o /dev/null` so an otherwise denied -o passes
// only in that form, and reports whether it was present.
func dropDevNullOutput(rest []word) ([]word, bool) {
	out := make([]word, 0, len(rest))
	found := false
	for i := 0; i < len(rest); i++ {
		switch t := rest[i].text; {
		case (t == "-o" || t == "--o") && i+1 < len(rest) && rest[i+1].text == "/dev/null":
			i++
			found = true
		case t == "-o=/dev/null" || t == "--o=/dev/null":
			found = true
		default:
			out = append(out, rest[i])
		}
	}
	return out, found
}

// goDeny lists go flags that run a program, name a file to write, or write
// sources, in normalized form (lowercase, no dashes, no "test." prefix).
var goDeny = []string{
	"exec", "toolexec", "vettool", "o", "c", "w", "u", "update", "updatesnapshot", "output",
	"ldflags", "gcflags", "asmflags", "gccgoflags", "trace", "overlay", "pkgdir", "prefix",
	"cwd", "dir", "config", "call", "eval",
}

// goDeniedSuffixes catch the many spellings of "write this path": coverprofile,
// cpuprofile, memprofile, modfile, outputdir, ...
var goDeniedSuffixes = []string{"path", "file", "dir", "directory", "location", "profile"}

func flagsOK(rest []word) bool {
	for _, a := range rest {
		if !isFlag(a.text) {
			continue
		}
		name := strings.ToLower(strings.TrimLeft(flagName(a.text), "-"))
		name = strings.TrimPrefix(name, "test.")
		if slices.Contains(goDeny, name) || strings.HasPrefix(name, "fix") || strings.HasPrefix(name, "output") {
			return false
		}
		for _, s := range goDeniedSuffixes {
			if strings.HasSuffix(name, s) {
				return false
			}
		}
	}
	return true
}

// flagSpec is a per-runner flag allowlist. Anything not listed is refused,
// including after `--`. Non-flag arguments (paths, package patterns, test
// names) pass.
type flagSpec struct {
	bare   []string
	values map[string]func(string) bool // flag -> validator for its value
	// positional, when set, vets each non-flag argument (idx counts them from
	// zero). Tools that have subcommands (vitest init) or treat words as files
	// (eslint, tsc) use it to accept paths only.
	positional func(idx int, v string) bool
}

func (a flagSpec) merge(b flagSpec) flagSpec {
	out := flagSpec{bare: append(slices.Clone(a.bare), b.bare...), values: map[string]func(string) bool{}, positional: a.positional}
	for k, v := range a.values {
		out.values[k] = v
	}
	for k, v := range b.values {
		out.values[k] = v
	}
	return out
}

var pathRe = regexp.MustCompile(`^[A-Za-z0-9_./@+-]+$`)

// pathLike reports whether v looks like a file path: it contains a slash or a
// dot, so a bare word such as a subcommand name is not one.
func pathLike(v string) bool {
	return pathRe.MatchString(v) && !strings.HasPrefix(v, "-") && strings.ContainsAny(v, "/.")
}

func pathsOnly(_ int, v string) bool { return pathLike(v) }

// vitestPositional allows `run` as the only subcommand, then paths.
func vitestPositional(idx int, v string) bool { return (idx == 0 && v == "run") || pathLike(v) }

func anyValue(v string) bool { return v != "" }
func digits(v string) bool   { return v != "" && strings.Trim(v, "0123456789") == "" }
func oneOf(opts ...string) func(string) bool {
	return func(v string) bool { return slices.Contains(opts, v) }
}

// flagsAllowed reports whether every flag in rest is in spec with an
// acceptable value, given as `--flag=value` or `--flag value`.
func flagsAllowed(rest []word, spec flagSpec) bool {
	npos := 0
	for i := 0; i < len(rest); i++ {
		tok := rest[i].text
		if tok == "--" {
			continue
		}
		if !isFlag(tok) {
			if spec.positional != nil && !spec.positional(npos, tok) {
				return false
			}
			npos++
			continue
		}
		name, val, hasVal := strings.Cut(tok, "=")
		if validate, ok := spec.values[name]; ok {
			if !hasVal {
				if i+1 >= len(rest) || isFlag(rest[i+1].text) {
					return false
				}
				i++
				val = rest[i].text
			}
			if !validate(val) {
				return false
			}
			continue
		}
		if hasVal || !slices.Contains(spec.bare, name) {
			return false
		}
	}
	return true
}

var (
	builtinReporters = oneOf("default", "verbose", "dot", "basic", "tap", "tap-flat", "summary")
	pytestSpec       = flagSpec{
		bare: []string{"-q", "-v", "-vv", "-vvv", "-x", "-s"},
		values: map[string]func(string) bool{
			"-k": anyValue, "-m": anyValue,
			"-n":        func(v string) bool { return v == "auto" || digits(v) },
			"--maxfail": digits,
			"--tb":      oneOf("auto", "long", "short", "line", "native", "no"),
			"-p":        oneOf("no:cacheprovider"),
		},
	}
	jestSpec = flagSpec{
		bare:       []string{"--run"},
		positional: pathsOnly,
		values: map[string]func(string) bool{
			"-t": anyValue, "--testNamePattern": anyValue,
			"--reporter": builtinReporters, "--reporters": builtinReporters,
		},
	}
	eslintSpec = flagSpec{positional: pathsOnly, values: map[string]func(string) bool{
		"--max-warnings": digits,
		"--format":       oneOf("stylish", "compact", "json", "unix", "tap", "table", "visualstudio", "codeframe"),
	}}
	tscSpec = flagSpec{
		positional: pathsOnly,
		bare:       []string{"--noEmit"},
		values:     map[string]func(string) bool{"-p": anyValue, "--project": anyValue},
	}
	golangciSpec = flagSpec{values: map[string]func(string) bool{"--timeout": anyValue}}
	cargoSpec    = flagSpec{
		bare: []string{
			"--workspace", "--all", "--all-targets", "--lib", "--bins", "--tests", "--release",
			"-q", "--quiet", "-v", "--verbose", "--locked", "--offline", "--frozen",
			"--no-default-features", "--all-features", "--no-run",
			// After `--` (test binary and lint flags).
			"--nocapture", "--ignored", "--include-ignored", "--exact", "--show-output",
		},
		values: map[string]func(string) bool{
			"-p": anyValue, "--package": anyValue, "-F": anyValue, "--features": anyValue,
			"--test": anyValue, "--bin": anyValue, "-j": digits, "--jobs": digits,
			"--test-threads": digits, "-D": anyValue, "-W": anyValue, "-A": anyValue,
		},
	}
	// npm/pnpm/yarn scripts forward their arguments to an unknown test or lint
	// tool, so they get the union of the safe flags of all of them.
	scriptSpec = pytestSpec.merge(jestSpec).merge(eslintSpec)
)
