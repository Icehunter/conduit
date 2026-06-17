# Conduit — Capability Matrix

Last updated: 2026-06-17

This document answers product questions about Conduit: what works, what's
coming, and what's intentionally out of scope.

- For Claude wire/auth compatibility details see `COMPATIBILITY.md`.
- For Copilot and ChatGPT/Codex provider-account wire details see
  `PROVIDER_COMPATIBILITY.md`.
- For historical CC source → Go mapping see `PARITY.md` (frozen reference; not
  the active product tracker).
- Update rules: capability changes → this file; OAuth/header/wire changes →
  `COMPATIBILITY.md` for Claude or `PROVIDER_COMPATIBILITY.md` for
  provider-account paths; CC behavioral reference notes → `PARITY.md`.

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
| GitHub Copilot auth | 🔶 | Experimental: device-code login, Copilot token exchange, model discovery, `/chat/completions`, `/responses`, and Claude `/v1/messages` routing; gracefully reports entitlement/discovery failures |
| OpenAI ChatGPT Plus/Pro OAuth | 🔶 | Experimental: browser PKCE login, secure refresh-token storage, ChatGPT/Codex model rows, and Codex Responses routing through `https://chatgpt.com/backend-api/codex/responses`; manual verification still required |
| Provider-account wire checks | ✅ | `PROVIDER_COMPATIBILITY.md` plus `make wire` cover Copilot and ChatGPT/Codex drift guards; Claude remains under `make wire-claude` |

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
| Retry-compact on stream failure | ✅ | Before each stream-failure retry the history payload is microcompacted when estimated size exceeds cw/4, preventing unbounded token amplification across retries |
| Non-streaming request timeout | ✅ | `CreateMessage` wraps every call in a 5-minute `context.WithTimeout`; a trickling server cannot stall the loop indefinitely |
| Conversation recovery | ✅ | Partial blocks persisted; orphan tool_use filtered on resume; auto-retries after tool_use/tool_result chain validation errors by sanitizing history |
| Mid-turn steering | ✅ | Message injected between tool batches; agent pivots without losing context |
| Sub-agents (Task tool) | ✅ | `internal/tools/agenttool/`; sub-agent token usage recorded in session JSONL via `OnSubAgentUsage`; child loops capped at 50 turns (`DefaultSubAgentMaxTurns`); cap returns `ErrMaxTurnsExceeded` — TUI shows a soft notice, subagent result carries an incomplete marker so the parent model knows the child was truncated rather than finished |
| Coordinator mode | ✅ | Claude as orchestrator; task-notification XML |
| Council mode | ✅ | Parallel multi-model debate, synthesis, convergence detection, roles, voting |
| Agent Teams (experimental) | ✅ | `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` or `conduit.json` `agentTeams: true`; in-process goroutine teammates (divergence: no tmux/OS-process); lead+teammate multi-pane compositor layout; task store with Assignee/Dependencies + Claim/NextClaimable/auto-unblock; plan-approval handshake via PlanReply channel; shutdown-request/approve/reject protocol; lead inbox pump goroutines; live EventText streaming to TUI panes; `internal/team/`, `internal/agent/loopteammate.go` |
| Plan mode / ExitPlanMode approval | ✅ | Scrollable modal; auto/accept-edits/live-review/default/chat options; "chat about this" (Discuss:true) yields the turn — agent stops and waits for user input |
| Diff-first review gate | ✅ | Hunk-level Myers diff; per-hunk approve/reject/note; `acceptEditsLive` mid-turn pause |
| Decision journal | ✅ | Append-only JSONL; `RecordDecision` tool; council auto-records verdicts |
| Proactive health checks | ✅ | Git/deps pre-flight at session start; warnings in system context |
| Provider failover chains | ✅ | `internal/providerrotation/`; configure `providerChains.role: [key1, key2]` in conduit.json; 429/503/529 rotate to next provider in chain; cooldowns tracked in-process; `EventProviderFailover` emitted on swap |
| Token-Time Stopping Rules (TTSR) | ✅ | `internal/ttsr/`; regex rules in `.conduit/ttsr/*.md` frontmatter; 4KB sliding tail buffer; per-rule `MaxFires` cap; global 3-fires/turn circuit breaker; `EventTTSR` emitted on fire |
| Model-per-role sub-agent dispatch | ✅ | `SubAgentSpec.Role`; `LoopConfig.RoleResolver` resolves role→(model, client); `Task(role: "planning")` routes through configured provider; plugin agents declare `role:` in frontmatter; all roles configured via ctrl+m are now honoured by sub-agents |

---

## Tools (37 built-in)

| Tool | Status | Notes |
|------|--------|-------|
| BashTool | ✅ | RTK filtering; Unix/macOS. Windows: Shell (PowerShell) instead; logs to stderr when raw output exceeds the 120 KB pre-RTK capture cap so silent tail-drops are visible |
| FileReadTool | ✅ | Line-range support; URL scheme dispatch: `pr://owner/repo/N` and `issue://owner/repo/N` (gh CLI, RTK-filtered), `http(s)://` (webfetchtool); `anchors: true` prefixes each line with a 7-char content-hash anchor via `hashline.Compute` |
| FileWriteTool | ✅ | |
| FileEditTool | ✅ | Exact-string replacement |
| HashEditTool | ✅ | Content-hash-anchored editing; `internal/tools/hashedittool/`; survives line-number drift; multi-op bottom-to-top application; stages through diff gate |
| GrepTool | ✅ | ripgrep backend |
| AstGrep | ✅ | `internal/tools/astgreptool/`; structural search/rewrite via installed ast-grep/sg; rewrites stage through diff gate; no auto-download |
| GlobTool | ✅ | |
| AgentTool (Task) | ✅ | Sub-agent spawning |
| WebFetchTool | ✅ | HTML→markdown |
| WebSearchTool | ✅ | Multi-provider: Brave Search (API key via secure storage or `BRAVE_API_KEY`) with Anthropic-native `web_search_20250305` fallback; providers tried in order, first non-empty result wins; `internal/websearch/` + `internal/websearch/brave/` |
| NotebookEditTool | ✅ | Jupyter cell edit |
| REPLTool | ✅ | persistent kernel per (session, lang); idle reaper; python + node; bash stays subprocess-per-call; `internal/kernel/` |
| SleepTool | ✅ | |
| TodoWriteTool | ✅ | |
| TaskCreate/Get/List/Update/Stop | ✅ | In-process task store; Assignee + Dependencies fields; Claim/NextClaimable (self-claim); Complete auto-unblocks dependents; OnCreated/OnCompleted hook callbacks |
| SendMessage | ✅ | `internal/tools/sendmessagetool/`; registered for lead when agent teams active; per-teammate sender identity baked via `NewFor`; kinds: message, plan-approve/reject, shutdown-request/approve/reject |
| EnterPlanMode / ExitPlanMode | ✅ | |
| EnterWorktree / ExitWorktree | ✅ | git worktree add/remove |
| EnterAutoMode / ExitAutoMode | ✅ | Conduit-original; bypassPermissions with user consent |
| AskUserQuestion | ✅ | Dismiss (Esc/ctrl+c) yields the turn (agent stops, waits for user); digit keys focus option only, Enter confirms; all async-opened overlays (permission prompt, plan approval, diff review, AskUserQuestion) swallow the first non-Esc keystroke to prevent buffered keystrokes from auto-accepting dialogs |
| ConfigTool | ✅ | get/set/allow/deny/env |
| MCPTool | ✅ | MCP tool proxy |
| ListMcpResources / ReadMcpResource | ✅ | |
| SkillTool | ✅ | |
| LSPTool | ✅ | hover, definition, references, diagnostics, documentSymbol, workspaceSymbol, implementation, callHierarchy; `internal/tools/lsp/lsp_test.go` |
| ToolSearchTool | ✅ | Live registry search |
| SyntheticOutputTool | ✅ | Coordinator signalling |
| LocalImplement | ✅ | Conduit-original; MCP-backed bounded implementation offload |
| RecordDecision | ✅ | Conduit-original; decision journal |
| CCRRetrieve | ✅ | Conduit-original; retrieves content-addressed originals from the CCR store; supports offset/limit line-range and pattern grep; output re-truncated via `truncate.Apply` |

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
| MEMORY.md auto-memory | ✅ | user/feedback/project/reference types; compact prompt mode is default to reduce token overhead (`CONDUIT_MEMORY_PROMPT_FULL=1` restores full taxonomy block) |
| Memory extraction (auto) | ✅ | Sub-agent fires on each end_turn |
| Session memory summaries | ✅ | `summary.md` per session; injected on resume only when transcript history is unavailable (and size-capped) |
| Auto-dream consolidation | ✅ | 24h + 5 session gate |
| Curator (weekly memory+skill hygiene) | ✅ | fires after 7d or 10 sessions; snapshot + deterministic transitions (active→stale 30d→archived 90d) before LLM consolidation; only touches agent-created non-pinned skills; promotes general project skills to global-conduit; `internal/memdir/curator.go` |
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
| FS-based skill discovery (agentskills.io) | ✅ | `~/.conduit/skills/`, `~/.claude/skills/`, `<cwd>/.claude/skills/`; YAML frontmatter + `references/` support; `internal/skills/fsloader.go` |
| Custom user slash commands | ✅ | `~/.claude/commands/*.md` + `<cwd>/.claude/commands/*.md`; YAML frontmatter `description:`; `$ARGUMENTS` substitution; `internal/commands/custom.go` |
| Background memory/skill nudge prompts | ✅ | `bgreview` fires every 5/7 end_turns; skill review is gap-driven (detects missing capability, creates skill if needed), scope-classified (project vs global-conduit), and hardened with "capture the fix not the failure" guardrails |
| SkillManage tool | ✅ | `internal/tools/skillmanagetool/`; create/view/update/list/promote; project + global-conduit + global scopes; `WithAgentProvenance()` option marks agent-created skills; telemetry hooks into `internal/skillusage/` |
| Skill usage telemetry | ✅ | `internal/skillusage/`; per-skill use/view/patch counts, provenance (agent vs user), lifecycle state, pinning; JSON store at `~/.conduit/skill-usage.json` |
| Skill lifecycle transitions | ✅ | deterministic active→stale (30d) →archived (90d) idle transitions; pinned and user-authored skills exempt; runs in curator before LLM pass |
| Skill backups + rollback | ✅ | `internal/skillusage/backup.go`; tar.gz snapshot of `~/.conduit/skills/` + `~/.claude/skills/` before every curator mutation; prune to 5 most recent; rollback with pre-rollback snapshot |
| `/skills` subcommands | ✅ | `status`, `pin`, `unpin`, `backups`, `rollback` backed by `internal/skillusage/`; `internal/commands/skills.go` |

---

## Hooks

| Capability | Status | Notes |
|-----------|--------|-------|
| PreToolUse / PostToolUse / SessionStart / Stop | ✅ | |
| Shell, HTTP, prompt, agent hook types | ✅ | |
| Async hooks | ✅ | Non-blocking goroutine |
| Hook approve / block directives | ✅ | |
| Desktop notifications | ✅ | macOS osascript / Linux notify-send |
| TeammateIdle / TaskCreated / TaskCompleted | ✅ | Agent-teams hooks; `internal/hooks/hooks.go`; wired from GlobalStore callbacks and SpawnTeammate idle path |

---

## TUI & Clients

| Capability | Status | Notes |
|-----------|--------|-------|
| Bubble Tea v2 terminal TUI | ✅ | Primary client |
| Ultraviolet compositor (floating panels) | ✅ | No viewport shrink |
| Full GFM markdown rendering | ✅ | Tables, headings, task lists, strikethrough, blockquotes, italic |
| Syntax highlighting | ✅ | Hand-rolled; no Chroma dependency |
| Animated work indicator | ✅ | Gradient scramble row |
| Streaming cost display | ✅ | Live `$0.0042` as tokens stream; `workinganim.SetStatus()` |
| Collapsible tool outputs | ✅ | ▼/▶ indicators; space to toggle; persists per-message |
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
| Async-dialog keystroke guard | ✅ | Permission prompt, plan approval, diff review, and AskUserQuestion overlays each carry `guardFirstKey`; the first non-Esc key after async open is swallowed, preventing buffered Enter/Space from auto-accepting |
| Mid-agent draft preservation | ✅ | Queued messages drain to the input box only when it is empty; an arriving `agentDone` event never clobbers an in-progress draft |
| Agent Teams multi-pane layout | ✅ | `internal/tui/teampanes.go`; horizontal→vertical→fallback grid via compositor; Shift+Down cycles pane focus; Ctrl+T toggles task list strip; Enter routes input to focused teammate; live EventText streaming via TeammateNotifyHook; 500ms roster-refresh tick |
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
| CCR store | ✅ | `internal/ccr/`; content-addressed, SHA-256-keyed, 7-day TTL; deduplicates identical content; RTK attaches handle to filtered output so compression is recoverable |
| SmartCrusher | ✅ | `internal/rtk/smartcrusher.go`; structural JSON compression: array-of-homogeneous-objects → schema+sample, deep nested object large-leaf collapse; fires as content-based fallback after command classifiers; original stored in CCR for recovery |

---

## Token Optimization

| Capability | Status | Notes |
|-----------|--------|-------|
| Truncate-to-disk | ✅ | Saves large outputs to `~/.conduit/truncated/`, returns preview + hint; 7-day retention; configurable limits |
| Long-line truncation | ✅ | Max 2000 chars/line in FileReadTool; prevents single-line binary expansion |
| Structured compaction prompt | ✅ | 5 sections: Current State, Files & Changes, Technical Context, Strategy, Next Steps |
| Incremental compaction | ✅ | Merges `<previous-summary>` for multi-compaction sessions |
| Error tool_result protection | ✅ | Microcompact never clears error results (debugging info is sacred) |
| Model-aware overflow detection | ✅ | `UsableContext()`, `CheckOverflow()`; 80% micro, 95% full compact thresholds |
| Configurable limits | ✅ | `conduit.json` `toolOutput.maxLines/maxBytes`, `compaction.keepRecent` |
| Token savings metrics | ✅ | `sessionstats.SessionMetrics`: RTK, truncate, microcompact, compact counters |
| Usage observability | ✅ | stderr logs on `message_delta` decode failure (turn token counts lost) and on OpenAI-compat streams that end without usage data; silent zero-counts are now visible |
| Live-zone compaction | ✅ | Micro-compaction skips messages inside the Anthropic prompt-cache prefix (0..`liveZoneBoundary`); only the live zone (after the last `cache_control` breakpoint) is compacted, keeping the cached prefix byte-identical and avoiding gratuitous cache misses |
| Cache-stable tool ordering | ✅ | `buildToolDefs` sorts all tool definitions alphabetically before building the defs slice; cache_control breakpoint always lands on the same (last alphabetical) tool regardless of registry iteration order |
| Volatile-prefix detection | ✅ | FNV-1a hash of system blocks + tools + cached history messages stored on the Loop struct; advisory `log.Printf` warning emitted when the hash changes between turns (signals a potential silent cache miss) |

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
| C-O4.5: Runtime polish | Config JSON schema; background job tracking; broader project instruction files (AGENTS.md, copilot-instructions, .cursor/rules ✅); prompt-submit hook | 🔶 | Plan §4.5; instruction files done in `internal/instructions` |
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
- **Provider credential → models** — ✅ resolved. `/models` expands saved OpenAI-compatible provider credentials against the catalog. OpenAI and Gemini direct providers show their provider-specific models with stripped model IDs; OpenRouter shows the full catalog with provider-prefixed IDs. Typing in the picker filters model rows and keeps provider headers that still have matches. Selecting any generated row creates the provider+model binding for that role without requiring a separate provider form entry first. (Gemini credential catalog expansion verified in tests.)
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
- Agent Teams remote/tmux display mode (conduit uses in-process compositor; tmux accepted without error but maps to in-process)
- Voice / STT (local whisper.cpp deferred; Anthropic private endpoint unavailable)
- KAIROS / GrowthBook-gated features
- Anthropic-internal analytics, telemetry, and billing dialogs
- Dynamic provider execution that bypasses typed streaming adapters
- Remote sharing enabled by default
- Auto-download of LSP language servers
