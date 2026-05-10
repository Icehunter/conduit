# conduit — AI Assistant Rules

Conduit is a local-first, provider-aware terminal agent. It started as a Go port of Claude Code v2.1.126 and has grown into its own runtime. It maintains Claude Code **wire compatibility** — OAuth, API headers, billing block, request shape, plugin format — but is free to innovate in behavior, features, and architecture.

- Module: `github.com/icehunter/conduit`
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

- **`STATUS.md`** — Conduit capability matrix and roadmap (✅/🔶/🔲/🚫). Check before assuming something works — some features are intentional stubs or planned C-Ox milestones.
- **`COMPATIBILITY.md`** — Claude wire/auth compatibility contract. Read this when touching OAuth, API headers, beta flags, or anything that must stay compatible with Claude Max/Pro subscriptions.
- **`PARITY.md`** — frozen historical CC TS source → Go mapping. Useful if you need to understand CC's original behavior for a specific feature; not the active product tracker.
- **`README.md`** — feature overview and installation.

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

## Which Doc To Update

| Change type | Update |
|-------------|--------|
| New capability, feature complete, partial, stub discovered | `STATUS.md` capability table + roadmap open-items section |
| OAuth client ID, API headers, beta flags, wire constants, CC version bump | `COMPATIBILITY.md` |
| CC TS → Go behavioral mapping note, historical divergence reference | `PARITY.md` (frozen; prefer a Go comment in source instead) |
| OpenCode-inspired milestone deliverable or gap | `STATUS.md` open-items section for that milestone (C-O0 through C-O7) |

## Hard Rules

- **Wire compatibility over cleverness** — the Anthropic API wire format (billing header, request shape, OAuth headers) must remain compliant. For behavior and features, conduit is free to diverge; note intentional divergences in `COMPATIBILITY.md`.
- **Update `STATUS.md`** whenever you complete, partially complete, or discover a stub for any tracked capability. It's how humans know what works.
- **RTK filtering is in-process** (`internal/rtk/`). Don't shell out to the standalone `rtk` binary. Don't trim bash output ad-hoc at call sites — add a classifier in `internal/rtk/` instead.
- **`make verify` must pass** before any task is done. Zero lint errors, tests pass with `-race`.
- **`./conduit`** at repo root is a build artifact. `make build` overwrites it. Don't edit it.

## Detailed Standards

Standards live in `.claude/rules/` and are loaded automatically:

| Rule file | Scope | What it covers |
|-----------|-------|----------------|
| `testing.md` | `*_test.go` | Table-driven tests, fuzz targets, no `time.Sleep` |
| `error-handling.md` | `*.go` | `%w` wrapping, no silent swallows, context propagation |
| `tui.md` | `internal/tui/` | Bubble Tea v2 — no blocking in Update, model/update/view discipline |
| `porting.md` | `*.go` | Using CC TS source as a behavioral reference; when and how to diverge; PARITY.md updates |
