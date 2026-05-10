# Conduit — Capability Matrix

Last updated: 2026-05-11

This document answers product questions about Conduit: what works, what's
coming, and what's intentionally out of scope. For Claude wire/auth
compatibility details see `COMPATIBILITY.md`. For historical CC source mapping
see `PARITY.md` (frozen reference).

**Legend:** ✅ Done · 🔶 Partial · 🔲 Planned · 🚫 Descoped

---

## Providers & Accounts

| Capability | Status | Notes |
|-----------|--------|-------|
| Claude Max/Pro subscription (OAuth) | ✅ | PKCE flow, token refresh, keychain storage |
| Anthropic API key | ✅ | Direct API key auth |
| Multi-account switching | ✅ | `/account` panel, `/login --switch <email>` |
| OpenAI-compatible providers (Gemini, etc.) | ✅ | API key stored in secure storage; role-assignable |
| Local models via MCP | ✅ | Route any role to an MCP-backed local provider |
| Role-based model routing | ✅ | Default, Main, Background, Planning, Implement roles via `/model` Ctrl+M |
| GitHub Copilot auth | 🔲 | C-O3: planned after `/accounts` expansion |
| OpenAI ChatGPT Plus/Pro OAuth | 🔲 | C-O3: investigate once stable flow confirmed |
| OpenRouter API key | 🔲 | C-O3: API-key flow, catalog-assisted |
| Provider auth plugin interface | 🔲 | C-O3: `Methods`/`Authorize`/`Callback` |

---

## Model & Provider Discovery

| Capability | Status | Notes |
|-----------|--------|-------|
| Manual provider entry (URL + key) | ✅ | Settings Providers tab |
| Provider validation on save | ✅ | Test-call before persisting |
| Model picker (`/model`) | ✅ | Grouped by provider; Ctrl+M assigns roles |
| Model capability display | 🔲 | C-O2: context window, cost, tool-use, vision flags |
| Models.dev / Catwalk catalog | 🔲 | C-O2: cached catalog; pre-fills provider setup |
| Catalog refresh command | 🔲 | C-O2: `update-providers` or `/models --refresh` |

---

## Agent Runtime

| Capability | Status | Notes |
|-----------|--------|-------|
| Streaming SSE agent loop | ✅ | `internal/agent/loop.go` |
| Multi-turn tool dispatch | ✅ | Parallel pool (max 4 concurrent) |
| Auto-compact | ✅ | Fires near context limit; input + cache tokens counted |
| Micro-compaction | ✅ | Clears old tool results after 60 min idle |
| Extended thinking (`/effort`) | ✅ | low/medium/high/max budgets |
| Fast mode (`/fast`) | ✅ | Toggles Haiku; ⚡ badge |
| Exponential backoff (429) | ✅ | Base 1s, 2×, max 32s, jitter, 5 retries |
| Conversation recovery | ✅ | Partial blocks persisted; orphan tool_use filtered on resume |
| Mid-turn steering | ✅ | Message injected between tool batches; agent pivots without losing context |
| Sub-agents (Task tool) | ✅ | `internal/tools/agenttool/` |
| Coordinator mode | ✅ | Claude as orchestrator; task-notification XML |
| Council mode | ✅ | Parallel multi-model debate, synthesis, convergence detection, roles, voting |
| Plan mode / ExitPlanMode approval | ✅ | Scrollable modal; auto/accept-edits/live-review/default/chat options |
| Diff-first review gate | ✅ | Hunk-level Myers diff; per-hunk approve/reject/note; `acceptEditsLive` mid-turn pause |
| Decision journal | ✅ | Append-only JSONL; `RecordDecision` tool; council auto-records verdicts |

---

## Tools (35 built-in)

| Tool | Status | Notes |
|------|--------|-------|
| BashTool | ✅ | RTK filtering; Unix/macOS. Windows: Shell (PowerShell) instead |
| FileReadTool | ✅ | Line-range support |
| FileWriteTool | ✅ | |
| FileEditTool | ✅ | Exact-string replacement |
| GrepTool | ✅ | ripgrep backend |
| GlobTool | ✅ | |
| AgentTool (Task) | ✅ | Sub-agent spawning |
| WebFetchTool | ✅ | HTML→markdown |
| WebSearchTool | ✅ | |
| NotebookEditTool | ✅ | Jupyter cell edit |
| REPLTool | 🔶 | Functional; no tool-level tests yet |
| SleepTool | ✅ | |
| TodoWriteTool | ✅ | |
| TaskCreate/Get/List/Update/Stop | ✅ | In-process task store |
| EnterPlanMode / ExitPlanMode | ✅ | |
| EnterWorktree / ExitWorktree | ✅ | git worktree add/remove |
| EnterAutoMode / ExitAutoMode | ✅ | Conduit-original; bypassPermissions with user consent |
| AskUserQuestion | ✅ | |
| ConfigTool | ✅ | get/set/allow/deny/env |
| MCPTool | ✅ | MCP tool proxy |
| ListMcpResources / ReadMcpResource | ✅ | |
| SkillTool | ✅ | |
| LSPTool | 🔶 | hover, definition, references, diagnostics; Go/TS/JS/Py/Rust; no `internal/lsp/` package tests |
| ToolSearchTool | ✅ | Live registry search |
| SyntheticOutputTool | ✅ | Coordinator signalling |
| LocalImplement | ✅ | Conduit-original; MCP-backed bounded implementation offload |
| RecordDecision | ✅ | Conduit-original; decision journal |

---

## LSP

| Capability | Status | Notes |
|-----------|--------|-------|
| JSON-RPC/stdio client | ✅ | `internal/lsp/client.go` |
| Server auto-detect (Go, TS/JS, Python, Rust) | ✅ | `internal/lsp/manager.go` |
| pylsp → pyright fallback | ✅ | |
| hover / definition / references / diagnostics | ✅ | |
| Tool-level tests (6) | ✅ | `internal/tools/lsp/lsp_test.go` |
| `internal/lsp/` package unit tests | ✅ | `internal/lsp/client_test.go`, `manager_test.go` |
| LSP status indicator | ✅ | `Manager.Status(langKey)` → unknown/connecting/connected/broken/disabled |
| Expanded server registry (15+ langs) | ✅ | Vue, Svelte, Astro, YAML, Lua, C#, Java, Bash, Dockerfile, Terraform, Nix + existing Go/TS/JS/Py/Rust |
| Config overrides (cmd, args, env, disabled) | ✅ | `conduit.json` `lspServers` map; `NewManagerWithOverrides` |
| documentSymbol / workspaceSymbol | ✅ | Tree + flat form; workspace/symbol query |
| implementation / call hierarchy | ✅ | `implementation`, `callHierarchyIncoming`, `callHierarchyOutgoing` |

---

## MCP

| Capability | Status | Notes |
|-----------|--------|-------|
| stdio / HTTP / SSE / WebSocket transports | ✅ | |
| OAuth for MCP servers | ✅ | RFC 8414 + RFC 7591 DCR + PKCE |
| Server discovery (Claude/project/plugin/conduit overlays) | ✅ | |
| `conduit mcp add/list/get/remove/add-json` CLI | ✅ | Claude-parity surface |
| `/mcp` panel + slash commands | ✅ | |
| Project-scope approval gate | ✅ | Startup picker; persisted |
| Resource listing and reading | ✅ | |

---

## Session & Memory

| Capability | Status | Notes |
|-----------|--------|-------|
| JSONL session transcripts | ✅ | `~/.conduit/projects/` |
| Session resume (`--continue`, `/resume`) | ✅ | Claude history fallback/import |
| Session rewind | ✅ | JSONL snapshots |
| Session search | ✅ | Fuzzy over JSONL transcripts |
| Session export (markdown) | ✅ | |
| Session rename / tag | ✅ | |
| Cost persistence | ✅ | AppendCost per turn; LoadCost on resume |
| File access history | ✅ | |
| MEMORY.md auto-memory | ✅ | user/feedback/project/reference types |
| Memory extraction (auto) | ✅ | Sub-agent fires on each end_turn |
| Session memory summaries | ✅ | `summary.md` per session; loaded on resume |
| Auto-dream consolidation | ✅ | 24h + 5 session gate |
| Session worktrees | ✅ | EnterWorktree / ExitWorktree |

---

## Plugins & Skills

| Capability | Status | Notes |
|-----------|--------|-------|
| Plugin install/uninstall (git clone) | ✅ | `~/.conduit/plugins/` |
| Plugin enable/disable | ✅ | |
| Plugin marketplace discovery | ✅ | `known_marketplaces.json` |
| Plugin skills (SKILL.md) | ✅ | Frontmatter + tool allowlist enforced |
| Plugin hooks (hooks.json) | ✅ | Merged into session hook list |
| Plugin agents (agents/*.md) | ✅ | Task `subagent_type` dispatch |
| Plugin MCP server sync | ✅ | |
| Plugin output styles | ✅ | |
| Bundled skills (`/simplify`, `/remember`) | ✅ | |

---

## Hooks

| Capability | Status | Notes |
|-----------|--------|-------|
| PreToolUse / PostToolUse / SessionStart / Stop | ✅ | |
| Shell, HTTP, prompt, agent hook types | ✅ | |
| Async hooks | ✅ | Non-blocking goroutine |
| Hook approve / block directives | ✅ | |
| Desktop notifications | ✅ | macOS osascript / Linux notify-send |

---

## TUI & Clients

| Capability | Status | Notes |
|-----------|--------|-------|
| Bubble Tea v2 terminal TUI | ✅ | Primary client |
| Ultraviolet compositor (floating panels) | ✅ | No viewport shrink |
| Full GFM markdown rendering | ✅ | Tables, headings, task lists, strikethrough, blockquotes, italic |
| Syntax highlighting | ✅ | Hand-rolled; no Chroma dependency |
| Animated work indicator | ✅ | Gradient scramble row |
| Image/PDF paste & drag-drop | ✅ | |
| @file mention parsing | ✅ | Line ranges, dirs, base64 blocks |
| Custom keybindings | ✅ | `~/.conduit/keybindings.json` |
| Multi-account UI (`/account`) | ✅ | |
| Themes (`/theme`) | ✅ | 4 built-ins + user themes; hot-swap |
| Plugin panel (`/plugin`) | ✅ | |
| MCP panel (`/mcp`) | ✅ | |
| Settings panel | ✅ | Providers, Stats, Accounts tabs |
| Rate limit display | ✅ | `anthropic-ratelimit-*` headers; <20% warning |
| Claude plan usage footer | ✅ | 5h/7d windows; `/toggle-usage` |
| `conduit serve` (local server) | 🔲 | C-O4: session/message/event endpoints |
| `conduit attach` (attach client) | 🔲 | C-O4 |
| Multi-session switcher panel | 🔲 | C-O5: depends on server spine |
| Desktop app | 🚫 | Deferred until server API is stable |
| IDE extension | 🚫 | Deferred; Zed first via ACP |

---

## Config & Settings

| Capability | Status | Notes |
|-----------|--------|-------|
| `~/.conduit/conduit.json` (Conduit config) | ✅ | Provider roles, active provider, council settings |
| Claude-compatible `settings.json` | ✅ | Loaded alongside conduit.json |
| `.mcp.json` project config | ✅ | |
| Env var injection | ✅ | `ApplyEnv`; dangerous keys filtered |
| XDG / Windows paths | ✅ | |
| Config migrations | ✅ | `internal/migrations/` |
| JSON schema for config files | 🔲 | C-O4.5: schema generation for provider/MCP/LSP/hook/account settings |

---

## RTK (In-Process Token Compression)

| Capability | Status | Notes |
|-----------|--------|-------|
| 75 command classifiers | ✅ | `internal/rtk/registry.go` |
| ANSI stripping | ✅ | |
| Filters: git, go, cargo, npm, pytest, eslint, docker, terraform, aws, make, … | ✅ | |
| SQLite analytics (`/rtk gain`) | ✅ | |
| `rtk discover` (unclassified command scan) | ✅ | |

---

## Distribution

| Capability | Status | Notes |
|-----------|--------|-------|
| Homebrew (`icehunter/tap/conduit`) | ✅ | `brew install icehunter/tap/conduit` |
| Scoop (Windows) | ✅ | |
| GitHub Releases (GoReleaser) | ✅ | macOS/Linux/Windows amd64+arm64 |
| Passive update notifier | ✅ | 24h cache; install-method-aware hint |
| winget (Microsoft) | 🔲 | Manual review pending; deferred until demand |

---

## OpenCode-Inspired Roadmap

| Milestone | Focus | Status |
|-----------|-------|--------|
| C-O0: Documentation governance | STATUS.md + PARITY.md → capability matrix + compatibility contract | ✅ This update |
| C-O1: LSP confidence | `internal/lsp/` tests; status indicator; 15+ server registry; config overrides; documentSymbol/workspaceSymbol | ✅ |
| C-O2: Model catalog | Models.dev / Catwalk catalog package; `/models --refresh`; capability display in picker | 🔲 |
| C-O3: Provider auth | Provider-auth interface; `/accounts` expansion; API-key flows; Copilot/OpenAI OAuth investigation | 🔲 |
| C-O4: Local server spine | `conduit serve`; `conduit attach`; session/message/permission/event endpoints; file-read tracker | 🔲 |
| C-O4.5: Runtime polish | Config JSON schema; background job tracking; broader project instruction file support | 🔲 |
| C-O5: Multi-session UI | Session switcher; new session creation; background session status; fork/parent navigation | 🔲 (needs C-O4) |
| C-O6: Share & import | Local share bundle export/import; optional remote endpoint; redaction checklist | 🔲 |

---

## Intentionally Out of Scope

- Bridge / IDE JSON-RPC integration (users use the real CC extension)
- Remote session management and ULTRAPLAN
- Team swarm messaging (SendMessageTool, teammate mailbox)
- Voice / STT (local whisper.cpp deferred; Anthropic private endpoint unavailable)
- KAIROS / GrowthBook-gated features
- Anthropic-internal analytics, telemetry, and billing dialogs
- Dynamic provider execution that bypasses typed streaming adapters
- Remote sharing enabled by default
