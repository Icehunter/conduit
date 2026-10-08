package shellsafe

// safeCommand describes one command form that is read-only enough to run
// without a permission prompt.
//
// argv is matched as an exact leading token sequence, never as a string
// prefix: an entry for {"git", "status"} matches the command `git status`
// and not `git status-something`, and, because the tokens come from the
// parsed AST, not `git -c core.pager=... status` either, since that argv
// begins with different tokens. A path-qualified name (`/bin/ls`, `./cat`)
// matches nothing, so a repository-local binary can never borrow an entry.
type safeCommand struct {
	// argv is the exact leading token sequence this entry matches.
	argv []string
	// restrictFlags limits the accepted flags to allowFlags. When false,
	// any flag not in denyFlags is accepted. It exists so that a command
	// whose every flag is harmless (`ls`, `df`) does not have to enumerate
	// them, while one that can mutate under a flag (`git branch`) does.
	restrictFlags bool
	// allowFlags is the set of accepted flag names when restrictFlags is
	// set. Compared against the flag name only, so `--format=x` is matched
	// by an entry of "--format".
	allowFlags []string
	// denyFlags is the set of rejected flag names when restrictFlags is
	// not set. See flagDenied for how clustered short flags and abbreviated
	// long flags are caught.
	denyFlags []string
	// requireFlag demands that at least one flag be present. It exists
	// for `git config`, where the read-only forms are distinguished from
	// the write form by a flag rather than by a subcommand token:
	// `git config --get x` reads, `git config x y` writes.
	requireFlag bool
	// allowOperands reports whether non-flag arguments may follow argv.
	// It is false for commands that mutate when handed an operand, such
	// as `git branch <name>` (creates) or `git remote add` (writes).
	allowOperands bool
	// maxOperands caps the number of operands when non-zero. `uniq in out`
	// writes its second operand, so uniq is limited to one.
	maxOperands int
	// denyOperands are operands that turn the command into a write, such
	// as the expire/delete/drop subcommands of `git reflog`. They are
	// refused in any operand position.
	denyOperands []string
	// flagsAfterDoubleDash marks a command that does not read `--` as the
	// end of its flags, so tokens after it are still checked as flags. find
	// takes `--` before its paths and still runs the expression after them
	// (`find -- . -delete`), and test treats `--` as a plain string.
	flagsAfterDoubleDash bool
}

// flagsAreInert reports whether every flag the command could be handed is
// harmless. Only such commands accept a glob that may expand into a flag:
// for the rest, a file named `--pre=sh` turns `rg foo *` into code
// execution once the shell expands the glob.
func (sc safeCommand) flagsAreInert() bool {
	return !sc.restrictFlags && len(sc.denyFlags) == 0
}

// countsOperands reports whether the entry inspects its operands, in which
// case a glob (which expands into an unknown number of unknown operands)
// cannot be judged.
func (sc safeCommand) countsOperands() bool {
	return sc.maxOperands > 0 || len(sc.denyOperands) > 0
}

// gitCodeExecFlags are flags that make an otherwise read-only git
// subcommand run a program or write a file: external diff drivers, pager
// handoff, and explicit output redirection. They are rejected everywhere
// they are accepted as flags.
//
// --textconv sits here for the same reason as --ext-diff: both run a
// program named by the repository's own config, so a repository the user
// cloned but does not control chooses what executes.
var gitCodeExecFlags = []string{
	"--ext-diff",
	"--textconv",
	"--open-files-in-pager",
	"-O",
	"--output",
	"--output-indicator-new",
	"--upload-pack",
	"--receive-pack",
	"--exec",
}

// safeCommands are the command forms that skip the permission prompt.
//
// The bar for membership is that the command cannot write to the
// filesystem, mutate repository or system state, or execute another
// program. Anything that fails that bar, including a read-only command that
// becomes destructive under a flag, either gets a restricted flag set here
// or stays off the list entirely.
//
// Deliberately absent:
//
//   - env, nice, nohup, timeout: these execute an arbitrary command given
//     to them. They are handled by peelWrapper instead, which unwraps them
//     and re-checks whatever is inside. (`time` needs no entry: it is a
//     shell keyword, handled by the statement walk.)
//   - cd: allowed only as the leading command; see readOnlyCheck.call.
//   - awk and sed: both run programs (`system()`, s///e) that cannot be
//     judged without a full interpreter.
//   - less, more and bat: pagers that can run commands and write log files.
//   - kill, killall, set, unset: signal processes or mutate shell state.
//   - git branch/tag/remote with operands, and any git subcommand that
//     writes: see the restricted entries below.
//   - git ls-remote: reaches the network.
//   - go vet (-vettool runs any binary), npm audit (`fix` writes).
var safeCommands = []safeCommand{
	// Shell builtins and core utilities. Every flag these accept is
	// read-only, so they carry no flag restrictions.
	{argv: []string{"basename"}, allowOperands: true},
	{argv: []string{"cal"}, allowOperands: true},
	{argv: []string{"cat"}, allowOperands: true},
	{argv: []string{"cmp"}, allowOperands: true},
	{argv: []string{"cut"}, allowOperands: true},
	{argv: []string{"df"}, allowOperands: true},
	{argv: []string{"diff"}, allowOperands: true},
	{argv: []string{"dir"}, allowOperands: true},
	{argv: []string{"dirname"}, allowOperands: true},
	{argv: []string{"du"}, allowOperands: true},
	// echo is safe only because redirections are rejected outright; with
	// a redirect it becomes an arbitrary file write. Do not add redirect
	// support without revisiting this entry.
	{argv: []string{"echo"}, allowOperands: true},
	{argv: []string{"egrep"}, allowOperands: true},
	{argv: []string{"false"}, allowOperands: true},
	{argv: []string{"fgrep"}, allowOperands: true},
	{argv: []string{"free"}, allowOperands: true},
	{argv: []string{"grep"}, allowOperands: true},
	{argv: []string{"groups"}, allowOperands: true},
	{argv: []string{"head"}, allowOperands: true},
	{argv: []string{"id"}, allowOperands: true},
	{argv: []string{"jq"}, allowOperands: true},
	{argv: []string{"ls"}, allowOperands: true},
	{argv: []string{"printenv"}, allowOperands: true},
	{argv: []string{"ps"}, allowOperands: true},
	{argv: []string{"pwd"}, allowOperands: false},
	{argv: []string{"realpath"}, allowOperands: true},
	{argv: []string{"stat"}, allowOperands: true},
	{argv: []string{"tail"}, allowOperands: true},
	{argv: []string{"top"}, allowOperands: true},
	{argv: []string{"true"}, allowOperands: true},
	{argv: []string{"type"}, allowOperands: true},
	{argv: []string{"uname"}, allowOperands: true},
	{argv: []string{"uptime"}, allowOperands: true},
	{argv: []string{"wc"}, allowOperands: true},
	{argv: []string{"whatis"}, allowOperands: true},
	{argv: []string{"whereis"}, allowOperands: true},
	{argv: []string{"which"}, allowOperands: true},
	{argv: []string{"whoami"}, allowOperands: false},

	// Read-only commands that write, set state or run a program under a
	// specific flag.
	//
	// `date -s` sets the system clock.
	{argv: []string{"date"}, denyFlags: []string{"-s", "--set"}, allowOperands: true},
	// `file -C` compiles a magic file into the working directory.
	{argv: []string{"file"}, denyFlags: []string{"-C", "--compile"}, allowOperands: true},
	// `printf -v` assigns a shell variable, and an array subscript in the
	// name is evaluated as arithmetic, which runs command substitutions.
	{argv: []string{"printf"}, denyFlags: []string{"-v"}, allowOperands: true},
	// `test -v 'a[$(cmd)]'` evaluates the subscript the same way; -R
	// (nameref test) resolves a variable name too.
	{argv: []string{"test"}, denyFlags: []string{"-v", "-R"}, allowOperands: true, flagsAfterDoubleDash: true},
	{argv: []string{"["}, denyFlags: []string{"-v", "-R"}, allowOperands: true, flagsAfterDoubleDash: true},
	{argv: []string{"fd"}, denyFlags: []string{"-x", "-X", "--exec", "--exec-batch"}, allowOperands: true},
	{
		argv: []string{"find"},
		denyFlags: []string{
			"-exec", "-execdir", "-ok", "-okdir", "-delete",
			"-fprint", "-fprint0", "-fprintf", "-fls",
		},
		allowOperands:        true,
		flagsAfterDoubleDash: true,
	},
	// --pre runs a program on every searched file; --hostname-bin runs one
	// to name the host for hyperlinks.
	{argv: []string{"rg"}, denyFlags: []string{"--pre", "--hostname-bin"}, allowOperands: true},
	// --compress-program runs a program on the temporary files.
	{argv: []string{"sort"}, denyFlags: []string{"-o", "--output", "--compress-program"}, allowOperands: true},
	// -o writes the listing to a file; -R with -H writes an index file
	// into every directory. tree parses its own options, so `--` is not
	// trusted to end them.
	{argv: []string{"tree"}, denyFlags: []string{"-o", "-R"}, allowOperands: true, flagsAfterDoubleDash: true},
	{argv: []string{"uniq"}, allowOperands: true, maxOperands: 1},
	{
		argv:          []string{"yq"},
		denyFlags:     []string{"-i", "--inplace", "--in-place", "-s", "--split-exp", "--split-exp-file"},
		allowOperands: true,
	},
	// hostname's flags are enumerated rather than denied, because the ones
	// that write take their value attached to the flag: `hostname -F/etc/x`
	// sets the hostname from a file in a single token, so there is no
	// operand for allowOperands to reject. An allow list fails closed on
	// the whole shape, including clustered forms.
	{
		argv:          []string{"hostname"},
		restrictFlags: true,
		allowFlags: []string{
			"-a", "--alias", "-A", "--all-fqdns", "-d", "--domain",
			"-f", "--fqdn", "--long", "-i", "--ip-address",
			"-I", "--all-ip-addresses", "-s", "--short",
			"-y", "--yp", "--nis",
		},
		allowOperands: false,
	},

	// Git: read-only porcelain.
	{argv: []string{"git", "blame"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "describe"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "diff"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "grep"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "log"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "ls-files"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "rev-parse"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "shortlog"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "show"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "status"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	// `git reflog <ref>` reads; `git reflog expire|delete|drop` rewrite the
	// reflog.
	{
		argv:          []string{"git", "reflog"},
		denyFlags:     gitCodeExecFlags,
		allowOperands: true,
		denyOperands:  []string{"expire", "delete", "drop"},
	},
	// Only the listing forms of stash; a bare `git stash` pushes.
	{argv: []string{"git", "stash", "list"}, denyFlags: gitCodeExecFlags, allowOperands: false},
	{argv: []string{"git", "stash", "show"}, denyFlags: gitCodeExecFlags, allowOperands: true},

	// Git: read-only forms of otherwise-mutating subcommands. These take
	// no operands: `git branch <name>` creates, `git tag <name>` creates,
	// and `git remote add|set-url|remove` all write to the config.
	{
		argv:          []string{"git", "branch"},
		restrictFlags: true,
		allowFlags: []string{
			"-l", "--list", "-a", "--all", "-r", "--remotes",
			"-v", "-vv", "--verbose", "--show-current",
			"--format", "--sort",
		},
		allowOperands: false,
	},
	{
		argv:          []string{"git", "tag"},
		restrictFlags: true,
		allowFlags: []string{
			"-l", "--list", "-n", "--contains", "--no-contains",
			"--merged", "--no-merged", "--points-at",
			"--format", "--sort",
		},
		allowOperands: false,
	},
	// The filter flags above take a commit-ish operand. Requiring one of
	// them to be present is what keeps `git branch <name>` (creates) and
	// `git tag <name>` (creates) out, since those carry an operand and no
	// flag.
	{
		argv:          []string{"git", "branch"},
		restrictFlags: true,
		allowFlags:    []string{"--contains", "--no-contains", "--merged", "--no-merged", "--points-at"},
		requireFlag:   true,
		allowOperands: true,
	},
	{
		argv:          []string{"git", "tag"},
		restrictFlags: true,
		allowFlags:    []string{"--contains", "--no-contains", "--merged", "--no-merged", "--points-at"},
		requireFlag:   true,
		allowOperands: true,
	},
	{
		argv:          []string{"git", "remote"},
		restrictFlags: true,
		allowFlags:    []string{"-v", "--verbose"},
		allowOperands: false,
	},
	// Read-only remote subcommands, spelled out so the mutating siblings
	// (add, remove, rename, set-url, prune) stay off the list.
	//
	// `git remote show` queries the remote over the network unless -n is
	// given, so -n is required here; otherwise this entry would admit
	// exactly the network access that keeps `git ls-remote` off the list.
	{
		argv:          []string{"git", "remote", "show"},
		restrictFlags: true,
		allowFlags:    []string{"-n"},
		requireFlag:   true,
		allowOperands: true,
	},
	{argv: []string{"git", "remote", "get-url"}, allowOperands: true},
	// `git config --get <key>` reads one key. The flag is matched exactly,
	// so --get-urlmatch and friends, and any write form, fall through.
	{
		argv:          []string{"git", "config"},
		restrictFlags: true,
		allowFlags:    []string{"--get", "--get-all", "--list", "-l"},
		requireFlag:   true,
		allowOperands: true,
	},

	// Go: Go's flag package accepts one or two dashes for the same flag, so
	// each denied flag is listed in both spellings.
	//
	// `go doc -http` starts a documentation web server.
	{argv: []string{"go", "doc"}, denyFlags: []string{"-http", "--http"}, allowOperands: true},
	// `go env -w` and `-u` rewrite the go env file.
	{argv: []string{"go", "env"}, denyFlags: []string{"-w", "--w", "-u", "--u"}, allowOperands: true},
	// -toolexec and -exec run a program; -mod=mod and -modfile let the go
	// command rewrite go.mod.
	{
		argv: []string{"go", "list"},
		denyFlags: []string{
			"-toolexec", "--toolexec", "-exec", "--exec",
			"-mod", "--mod", "-modfile", "--modfile",
		},
		allowOperands: true,
	},
	{argv: []string{"go", "version"}, allowOperands: true},

	// npm: listing and registry queries only. These never run lifecycle
	// scripts.
	{argv: []string{"npm", "ls"}, allowOperands: true},
	{argv: []string{"npm", "outdated"}, allowOperands: true},
	{argv: []string{"npm", "view"}, allowOperands: true},

	// `env` with no command to run just prints the environment. The
	// wrapper peel only fires when there is an inner command, so this
	// entry is reached exactly when there is not.
	{argv: []string{"env"}, restrictFlags: true, allowOperands: false},
}

// commandWrapper describes a command that runs another command given to
// it. Auto-approving the wrapper itself would auto-approve anything it was
// handed, so instead the wrapper is peeled off and the command inside is
// checked against safeCommands on its own merits.
//
// The wrapper's own flags are an allow list. Flags that change which
// program runs, such as `env -S` (split a string into a new command) and
// BSD `env -P` (search a different PATH), would otherwise let the peel
// check one command while the shell runs another.
type commandWrapper struct {
	// name is the wrapper's command name.
	name string
	// boolFlags are the flags without a value this wrapper accepts.
	boolFlags []string
	// valueFlags are the flags with a value this wrapper accepts. The value
	// is the next token unless it is attached (`-n10`, `--signal=TERM`).
	valueFlags []string
	// skipOperands is how many non-flag operands belong to the wrapper
	// itself before the inner command begins (`timeout 5 ls` → 1).
	skipOperands int
}

var commandWrappers = []commandWrapper{
	// `env` takes NAME=VALUE assignments before its command, and they are
	// deliberately not skipped here: an assignment is the same environment
	// steering that a `FOO=bar cmd` prefix is, so it has to fail the same
	// way. The assignment is left in place as the inner argv's first
	// token, which matches no entry, so the command falls through to the
	// prompt.
	{name: "env", boolFlags: []string{"-i", "--ignore-environment"}, valueFlags: []string{"-u", "--unset"}},
	{name: "nohup"},
	{name: "nice", valueFlags: []string{"-n", "--adjustment"}},
	{
		name:         "timeout",
		boolFlags:    []string{"--preserve-status", "--foreground", "-v", "--verbose"},
		valueFlags:   []string{"-s", "--signal", "-k", "--kill-after"},
		skipOperands: 1,
	},
}

// maxWrapperDepth bounds how many nested wrappers are unwrapped, so a
// pathological `nohup nohup nohup …` cannot spin.
const maxWrapperDepth = 4
