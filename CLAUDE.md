# CLAUDE.md

## Project

`conduit` — a 1:1 Go port of Claude Code v2.1.126, with RTK token-savings folded in-process.

- Module: `github.com/icehunter/conduit` (the repo dir is `claude-go`, but the Go module and binaries are `conduit`/`claude` — don't "fix" this)
- Go **1.26** (toolchain pinned in `go.mod` and CI)
- Entrypoint: `cmd/claude/main.go` → produces `bin/claude`, also copied to `./conduit` at repo root

## Build / test / lint

```bash
make build      # go build → bin/claude (also copies to ./conduit)
make test       # go test ./...
make test-race  # go test -race -count=1 ./...   (what CI runs)
make lint       # golangci-lint run
make vet        # go vet ./...
```

Single package: `go test ./internal/<pkg>/...`. Fuzz: `go test -run=^$ -fuzz=Fuzz<Name> -fuzztime=1m ./<pkg>`.

CI runs vet + race tests on linux/macos/windows, plus golangci-lint and govulncheck. Don't add platform-specific code without `_<goos>.go` build tags.

## Architecture orientation

Read these before making non-trivial changes — they encode decisions you can't recover from the code:

- **`STATUS.md`** — milestone-level status of every component, tool, and slash command. Marks ✅ DONE / 🔶 PARTIAL / ❌ STUB / 🔲 TODO. Check here before assuming a feature works; many slash commands and tools are intentional stubs awaiting later milestones (M7 MCP, M8 Skills, M9 Agents).
- **`PARITY.md`** — the 1:1 mapping between TS Claude Code source, the decoded v2.1.126 bundle, and Go packages here. Use this when porting new behavior.
- **`README.md`** — points to the TS source, decoded bundle, and RTK source on the local filesystem. Cross-reference these when porting.

Layout:

- `cmd/claude/` — CLI entry, flag parsing, tool registration
- `internal/agent/` — agent loop (the central message/tool orchestration)
- `internal/api/` — Anthropic API client + wire headers
- `internal/sse/` — SSE parser
- `internal/auth/`, `internal/secure/` — OAuth PKCE + keychain
- `internal/tools/<tool>tool/` — one package per tool (BashTool, FileEditTool, …)
- `internal/tui/` — Bubble Tea TUI
- `internal/rtk/` — in-process RTK output filters (per-command classifiers)
- `internal/permissions/`, `internal/hooks/`, `internal/commands/` — gates, hooks, slash commands

## Conventions / gotchas

- **Fidelity over cleverness.** This is a port. When in doubt, match the TS source's behavior exactly (including quirks). Note divergences in `PARITY.md`.
- **Update STATUS.md** when you finish, partially complete, or discover a stub for any tracked component. `STATUS.md` is what humans grep to know what works.
- **RTK output filtering** runs inside `BashTool` — every bash output passes through `internal/rtk/`. New CLIs that produce noisy output should get a classifier in `internal/rtk/` rather than ad-hoc trimming at the call site.
- **No external CLI for RTK.** Don't shell out to `rtk`; the filtering is in-process. The `~/.claude/RTK.md` global instructions about `rtk gain` / `rtk discover` apply to the *standalone* RTK tool, not to this binary.
- **`gosec` G104 is excluded** project-wide (see `.golangci.yml`). Other `errcheck`/`revive` rules are enforced — handle errors explicitly.
- The `./conduit` binary at the repo root is a build artifact, not source. `make build` overwrites it.
