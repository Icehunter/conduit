# conduit — AI Assistant Rules

conduit is a 1:1 Go port of Codex v2.1.126 with RTK token-savings folded in-process. It's a working TUI agent — every change must keep the real binary functional.

- Module: `github.com/icehunter/conduit` (repo dir is `Codex-go` — don't rename)
- Go **1.26** (pinned in go.mod and CI)
- Entrypoint: `cmd/conduit/main.go` → `bin/conduit`, also copied to `./conduit` at repo root

## Workflow — Every Change

```bash
make build      # compile + copy to ./conduit
make test-race  # go test -race -count=1 ./...  ← what CI runs
make lint       # golangci-lint (pinned via go tool — no global install needed)
make verify     # fmt-check + vet + lint + test  ← run this before finishing
```

Single package: `go test ./internal/<pkg>/...`
Fuzz: `go test -run=^$ -fuzz=Fuzz<Name> -fuzztime=1m ./<pkg>`

CI runs vet + race tests on linux/macos/windows + golangci-lint + govulncheck. Don't add platform-specific code without `_<goos>.go` build tags.

## Architecture — Read These First

Before making non-trivial changes, read:

- **`PARITY.md`** — 1:1 map of TS source → Go packages. Use when porting behavior; note divergences here.
- **`STATUS.md`** — milestone status for every tool, command, and subsystem (✅/🔶/❌/🔲). Check before assuming something works — many are intentional stubs.
- **`README.md`** — points to the reference sources on the local filesystem.

## Package Layout

```
cmd/conduit/main.go       # CLI entry, flag parsing, tool registration
internal/agent/           # Agent loop — central message/tool orchestration
internal/api/             # Anthropic API client + wire headers
internal/sse/             # SSE parser
internal/auth/            # OAuth PKCE + keychain
internal/secure/          # Platform keychain (macOS/Linux/Windows)
internal/tools/<X>tool/   # One package per tool (BashTool, FileEditTool, …)
internal/tui/             # Bubble Tea v2 TUI
internal/rtk/             # In-process RTK output filters (per-command classifiers)
internal/permissions/     # Permission gate (useCanUseTool)
internal/hooks/           # PreToolUse / PostToolUse / SessionStart hook runners
internal/commands/        # Slash command handlers
```

## Hard Rules

- **Fidelity over cleverness** — this is a port. Match TS behavior exactly, including quirks. Note divergences in `PARITY.md`.
- **Update `STATUS.md`** whenever you complete, partially complete, or discover a stub for any tracked component. It's how humans know what works.
- **RTK filtering is in-process** (`internal/rtk/`). Don't shell out to the standalone `rtk` binary. Don't trim bash output ad-hoc at call sites — add a classifier in `internal/rtk/` instead.
- **`make verify` must pass** before any task is done. Zero lint errors, tests pass with `-race`.
- **`./conduit`** at repo root is a build artifact. `make build` overwrites it. Don't edit it.

## Detailed Standards

Standards live in `.Codex/rules/` and are loaded automatically:

| Rule file | Scope | What it covers |
|-----------|-------|----------------|
| `testing.md` | `*_test.go` | Table-driven tests, fuzz targets, no `time.Sleep` |
| `error-handling.md` | `*.go` | `%w` wrapping, no silent swallows, context propagation |
| `tui.md` | `internal/tui/` | Bubble Tea v2 — no blocking in Update, model/update/view discipline |
| `porting.md` | `*.go` | Reading TS source + decoded bundle, cross-referencing, PARITY.md updates |
