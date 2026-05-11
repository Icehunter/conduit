# Conduit — Capability Matrix

Last updated: 2026-05-11

This document answers product questions about Conduit: what works, what's
coming, and what's intentionally out of scope.

- For Claude wire/auth compatibility details see `COMPATIBILITY.md`.
- For historical CC source → Go mapping see `PARITY.md` (frozen reference; not
  the active product tracker).
- Update rules: capability changes → this file; OAuth/header/wire changes →
  `COMPATIBILITY.md`; CC behavioral reference notes → `PARITY.md`.

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
| OpenAI API key (C-O3) | ✅ | `internal/providerauth/`; Accounts tab connect/rotate/disconnect |
| Gemini API key (C-O3) | ✅ | `internal/providerauth/`; Accounts tab connect/rotate/disconnect |
| OpenRouter API key (C-O3) | ✅ | `internal/providerauth/`; Accounts tab connect/rotate/disconnect |
| Provider auth interface (C-O3) | ✅ | `internal/providerauth/` — `Method`, `Config`, `Authorizer`, `APIKeyAuthorizer` |
| Provider entries reference accounts (C-O3) | ✅ | Provider entries store a credential alias that resolves through secure providerauth storage; known providers reuse `openai`, `gemini`, and `openrouter` credentials |
| GitHub Copilot auth | 🔶 | Experimental: device-code login, Copilot token exchange, model discovery, and OpenAI-compatible chat routing with Copilot headers; gracefully reports entitlement/discovery failures |
| OpenAI ChatGPT Plus/Pro OAuth | 🔲 | C-O3 follow-on: experimental product-account provider; see `docs/provider-account-oauth-plan.md` |

---

## Model & Provider Discovery

| Capability | Status | Notes |
|-----------|--------|-------|
| Manual provider entry (URL + key) | ✅ | Settings Providers tab |
| Provider validation on save | ✅ | Test-call before persisting |
| Model picker (`/model`) | ✅ | Grouped by provider; Ctrl+M assigns roles; type to filter model rows while retaining matching provider headers |
| Model capability display | ✅ | Context window, cost, tool-use, vision, thinking — shown in model picker with `?` key toggle |
| Catalog fetch/cache package (C-O2) | ✅ | `internal/catalog/` — OpenRouter fetch + 24h disk cache + built-in Anthropic snapshot |
| Catalog refresh command (C-O2) | ✅ | `/models --refresh` fetches & caches; shows count flash on completion |
| Catalog-assisted provider setup (C-O2) | ✅ | Provider form opens with picker (OpenAI/Gemini/OpenRouter/Custom); pre-fills base URL and credential alias; reuses saved providerauth key; model choice happens in `/models` |
| Provider credential unlocks catalog models (C-O2/C-O3) | ✅ | One OpenAI/Gemini/OpenRouter credential exposes all matching catalog models in `/models`; selections synthesize provider+model entries for role assignment; provider accounts can be edited/renamed as a group |
| Catalog override URL / local JSON path (C-O2) | ✅ | `CONDUIT_CATALOG_URL` overrides endpoint; `CONDUIT_CATALOG_FILE` loads local JSON instead |
| Network-failure / bad-JSON fetch tests (C-O2) | ✅ | `TestFetch_httpError`, `TestFetch_badJSON`, `TestFetch_timeout`, `TestFetch_localFile` in `catalog_test.go` |

---

## Agent Runtime

| Capability | Status | Notes |
|-----------|--------|-------|
| Streaming SSE agent loop | ✅ | `internal/agent/loop.go` |
| Multi-turn tool dispatch | ✅ | Parallel pool (max 4 concurrent) |
| Auto-compact | ✅ | Fires near context limit; input + cache tokens counted; compacts through the active role's selected client/model |
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
| LSPTool | ✅ | hover, definition, references, diagnostics, documentSymbol, workspaceSymbol, implementation, callHierarchy; `internal/tools/lsp/lsp_test.go` |
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
| Tool-level tests (C-O1) | ✅ | `internal/tools/lsp/lsp_test.go` — 6 tests covering existing operations |
| Package unit tests (C-O1) | ✅ | `internal/lsp/client_test.go`, `manager_test.go` — client behavior + server-matching |
| Expanded server registry (C-O1) | ✅ | 15+ langs: Vue, Svelte, Astro, YAML, Lua, C#, Java, Bash, Dockerfile, Terraform, Nix + Go/TS/JS/Py/Rust |
| Config overrides (C-O1) | ✅ | `conduit.json` `lspServers` map; `NewManagerWithOverrides`; cmd/args/env/disabled |
| documentSymbol / workspaceSymbol (C-O1) | ✅ | Tree + flat form; workspace/symbol query |
| implementation / call hierarchy (C-O1) | ✅ | `implementation`, `callHierarchyIncoming`, `callHierarchyOutgoing` |
| ServerStatus API (C-O1) | ✅ | `Manager.Status(langKey)` → unknown/connecting/connected/broken/disabled |
| LSP status surfaced in TUI (C-O1) | ✅ | Status tab shows per-language server health via `m.lspManager.Statuses()`; "none active" when idle |
| Focused restart / diagnostics tool actions (C-O1) | 🔲 | Evaluated; deferred — current broad LSP surface is sufficient for now |
| Auto-download language servers | 🚫 | Intentionally excluded; prefer installed binaries or explicit config |

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
| Settings panel | ✅ | Status, Config, Stats, Usage, Providers, Accounts tabs |
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

Each milestone entry links its open deliverables and the corresponding section
in `docs/conduit-enhancements-plan.md` where requirements and constraints are
defined in full.

| Milestone | Focus | Status | Open items |
|-----------|-------|--------|------------|
| C-O0: Documentation governance | STATUS.md → capability matrix; COMPATIBILITY.md for wire/auth; PARITY.md frozen | ✅ | See [C-O0 closure](#c-o0-closure) |
| C-O1: LSP confidence | Tool tests; 15+ server registry; config overrides; documentSymbol/workspaceSymbol/implementation/callHierarchy | ✅ | Focused restart/diagnostics actions deferred |
| C-O2: Model catalog | Catalog fetch/cache; `/models --refresh`; catalog-assisted provider form; provider credentials expose catalog models; capability display in picker | ✅ | Catwalk evaluation recorded below |
| C-O3: Provider auth | Provider-auth interface; Accounts tab; API-key flows (OpenAI/Gemini/OpenRouter); provider entries reference secure credential aliases | ✅ | Copilot/ChatGPT deferred by plan constraint |
| C-O4: Local server spine | `conduit serve`; `conduit attach`; session/message/permission/event/file-read endpoints | 🔲 | Plan §4; file-read tracker is a C-O4 deliverable, not C-O4.5 |
| C-O4.5: Runtime polish | Config JSON schema; background job tracking; broader project instruction files; prompt-submit hook | 🔲 | Plan §4.5 |
| C-O5: Multi-session UI | Session switcher; new session creation; background status; fork/parent navigation | 🔲 | Needs C-O4 |
| C-O6: Share & import | Local bundle export/import; optional remote endpoint; redaction checklist | 🔲 | Plan §6; two-phase: local bundle first, remote second |
| C-O7: Editor & desktop | `conduit acp`; Zed extension; VS Code/Cursor/Windsurf; desktop sidecar | 🔲 | Plan §7; needs C-O4 server API stable |

---

## C-O0 Closure

C-O0 delivered the three-doc split (STATUS / COMPATIBILITY / PARITY). CLAUDE.md
now tells contributors which doc to update, and STATUS.md names COMPATIBILITY.md
as the owner for OAuth/header/wire-check details.

---

## C-O1 Open Items

Per `docs/conduit-enhancements-plan.md §1`:

- **LSP status in TUI** — ✅ resolved. `Manager.Statuses()` added to `internal/lsp/manager.go`; `RunOptions.LSPManager` threads the manager into the TUI; the Status settings tab renders per-language server health.
- **Focused restart/diagnostics actions** — the plan says "consider"; evaluated and deferred. The current broad LSP surface is sufficient. Revisit if model reliability issues arise.
- **Constraints confirmed:** no auto-download of language servers; LSP failures are non-fatal.

---

## C-O2 Open Items

Per `docs/conduit-enhancements-plan.md §2`:

- **Catalog-assisted provider form** — ✅ resolved. Provider form now opens with a picker step (OpenAI, Gemini, OpenRouter, Custom). Selecting a known provider pre-fills base URL and credential alias. If a providerauth credential is already saved for that provider, the API key step is skipped and the stored key is reused. It does not ask for a model; model choice happens in Ctrl+M `/models`.
- **Provider credential → models** — ✅ resolved. `/models` expands saved OpenAI-compatible provider credentials against the catalog. OpenAI and Gemini direct providers show their provider-specific models with stripped model IDs; OpenRouter shows the full catalog with provider-prefixed IDs. Typing in the picker filters model rows and keeps provider headers that still have matches. Selecting any generated row creates the provider+model binding for that role without requiring a separate provider form entry first.
- **Catalog override URL / local JSON path** — ✅ resolved. `CONDUIT_CATALOG_URL` overrides the fetch endpoint; `CONDUIT_CATALOG_FILE` loads a local JSON file instead of making an HTTP request.
- **Network-failure fetch tests** — ✅ resolved. `TestFetch_httpError`, `TestFetch_badJSON`, `TestFetch_timeout`, `TestFetch_localFile`, `TestFetch_localFile_badJSON` added to `catalog_test.go`.
- **Catwalk evaluation** — OpenRouter was chosen as catalog source. Catwalk was evaluated conceptually: it serves the same use case but lacks a stable unauthenticated JSON endpoint, making OpenRouter the better default. This is a closed decision; no further action required.

---

## C-O3 Open Items

Per `docs/conduit-enhancements-plan.md §3`:

- **Provider entries referencing accounts** — ✅ resolved for C-O3 API-key providers. Provider entries store credential aliases rather than raw secrets, known provider aliases line up with providerauth IDs (`openai`, `gemini`, `openrouter`), and runtime client construction resolves the secret through secure storage.
- **Accounts tab UX** — ✅ resolved. Unconfigured providers now show dim "not set up" instead of red "api key ✗".
- **Copilot / ChatGPT OAuth** — deferred pending auth-path verification (plan constraint).

---

## C-O4 Scope Note

The plan places **file-read tracking** inside C-O4 ("Add file-read tracking
alongside file status so attached clients can show what context the agent has
actually consumed"), not C-O4.5. STATUS.md previously listed it under C-O4.5.
Corrected above.

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
- Auto-download of LSP language servers
