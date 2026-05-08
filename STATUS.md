# conduit Implementation Status

Last updated: 2026-05-08

## How to read this

- ✅ DONE — fully implemented, tested, works
- 🔶 PARTIAL — implemented but incomplete (see notes)
- ❌ STUB — registered/present but returns placeholder text
- 🚫 REMOVED — descoped (Claude product integrations)
- 🔲 TODO — not started, planned for milestone Mx

---

## M1 — Auth + bare API call ✅

| Component | Status | Notes |
|-----------|--------|-------|
| OAuth PKCE flow (claude.ai) | ✅ | internal/auth/flow.go |
| OAuth PKCE flow (Console) | ✅ | internal/auth/flow.go |
| Token refresh | ✅ | internal/auth/persist.go |
| Keychain storage (macOS) | ✅ | internal/secure/ |
| /v1/messages POST | ✅ | internal/api/client.go |
| Wire headers (UA, beta, session-id) | ✅ | internal/api/client.go |

---

## M2 — Streaming + core tools ✅

| Component | Status | Notes |
|-----------|--------|-------|
| SSE parser | ✅ | internal/sse/ |
| Agent loop | ✅ | internal/agent/loop.go |
| System prompt assembly | 🔶 | internal/agent/systemprompt.go; content complete but not byte-identical to TS (conduit-authored to avoid IP) |
| BashTool | ✅ | internal/tools/bashtool/ — Unix/macOS only; Windows replaced by Shell (PowerShell) |
| FileReadTool | ✅ | internal/tools/filereadtool/ |
| FileWriteTool | ✅ | internal/tools/filewritetool/ |
| FileEditTool | ✅ | internal/tools/fileedittool/ |
| GrepTool | ✅ | internal/tools/greptool/ |
| GlobTool | ✅ | internal/tools/globtool/ |
| Cost tracker | ✅ | tallied in internal/tui/model.go (tallyTokens/syncLive) |

---

## M3 — TUI ✅

| Component | Status | Notes |
|-----------|--------|-------|
| Bubble Tea main loop | ✅ | internal/tui/ |
| Message viewport | ✅ | |
| Input box | ✅ | |
| Slash command picker (fuzzy) | ✅ | |
| Tab completion | ✅ | |
| Input history (↑/↓) | ✅ | |
| Vim mode | 🚫 REMOVED | descoped — large effort (1513 LOC); use keybindings system instead |
| Status bar | ✅ | model, context%, cost; context meter uses current prompt/window tokens, provider `contextWindow` overrides, not summed billing tokens |
| TUI compositor | ✅ | ultraviolet screen-buffer layers; panels/pickers/modals draw over chat without shrinking viewport; floating window layer clamps overlay width/height |
| Animated working indicator | ✅ | Crush-inspired gradient scramble row replaces plain Thinking spinner |
| Terminal window title (OSC 2) | ✅ | internal/tui/title.go — sets "conduit · working" on task start, resets to "conduit" on completion/interrupt |
| Plan-approval picker | ✅ | internal/tui/planapproval.go — inset take-over modal with scrollable plan viewport, 4 options (auto/accept-edits/default/chat), mouse-selectable text, Tab focus toggle |
| Assistant info row | ✅ | model, duration, per-turn cost after completed responses |
| Tool message rendering | ✅ | one-line live/archive rows with tool-specific verbs + input/result summary; resumed tool_results pair back to tool_use; errors show details |
| Welcome card (two-panel) | ✅ | profile fetched from oauth/profile |
| Permission prompt modal | ✅ | |
| Login picker modal | ✅ | |
| Ctrl+Y copy code block | ✅ | |
| Interrupt (Ctrl+C) | ✅ | |
| Markdown rendering | ✅ | full GFM: tables, headings, italic, strikethrough, task lists, blockquotes |
| Code block highlighting | ✅ | hand-rolled syntax highlighting (no Chroma dependency) |

---

## M4 — All tools 🔶

| Tool | Status | Notes |
|------|--------|-------|
| BashTool | ✅ | Unix/macOS only; on Windows the Shell (PowerShell) tool is registered instead |
| Shell (PowerShell) | ✅ | internal/tools/winshelltool/ — Windows-only; registered instead of BashTool |
| EnterAutoMode | ✅ | internal/tools/automodetool/ — conduit extension; no CC counterpart |
| ExitAutoMode | ✅ | internal/tools/automodetool/ — conduit extension; no CC counterpart |
| FileReadTool | ✅ | |
| FileWriteTool | ✅ | |
| FileEditTool | ✅ | |
| GrepTool | ✅ | |
| GlobTool | ✅ | |
| WebFetchTool | ✅ | internal/tools/webfetchtool/ |
| WebSearchTool | ✅ | internal/tools/websearchtool/ |
| NotebookEditTool | ✅ | internal/tools/notebookedittool/ |
| SleepTool | ✅ | internal/tools/sleeptool/ |
| TodoWriteTool | ✅ | internal/tools/todowritetool/ |
| AgentTool | ✅ | internal/tools/agenttool/ |
| LocalImplement | ✅ | conduit-only wrapper: lets the main agent offload bounded diff drafts to configured MCP `local_implement` |
| LSPTool | 🔶 | internal/tools/lsp/; hover, definition, references, diagnostics; no tool-level tests |
| MCPTool | ✅ | internal/tools/mcptool/ |
| REPLTool | 🔶 | node/python3/bash via temp file (no shell injection); no tool-level tests (PARITY was wrong to mark ✅) |
| SkillTool | ✅ | internal/tools/skilltool/ |
| TaskCreateTool | ✅ | in-process store |
| TaskGetTool | ✅ | |
| TaskListTool | ✅ | |
| TaskOutputTool | ✅ | |
| TaskStopTool | ✅ | |
| TaskUpdateTool | ✅ | |
| RemoteTriggerTool | 🚫 REMOVED | Remote-only / M10; descoped (matches PARITY ⬛) |
| SendMessageTool | 🚫 REMOVED | Team messaging; descoped (matches PARITY ⬛) |
| ToolSearchTool | ✅ | searches live registry |

---

## M5 — Permissions + Hooks + Commands ✅

| Component | Status | Notes |
|-----------|--------|-------|
| Permission gate | ✅ | internal/permissions/ |
| Rule matching (exact/glob/prefix) | ✅ | |
| Session allow list | ✅ | |
| Interactive permission prompt | ✅ | |
| PreToolUse hooks | ✅ | internal/hooks/ |
| PostToolUse hooks | ✅ | |
| SessionStart hooks | ✅ | |

### Commands

| Command | Status | Notes |
|---------|--------|-------|
| /help | ✅ | |
| /commands | ✅ | opens slash command picker |
| /clear | ✅ | |
| /exit, /quit | ✅ | |
| /model | ✅ | grouped provider picker; switches Claude/MCP/OpenAI-compatible via `activeProvider`, mirrors to `providers` + `roles.default`; Ctrl+M assigns Default/Main/Background/Planning/Implement roles; Gemini/OpenAI-compatible providers are validated, assignable, text-streamable, and tool-call translated |
| /providers | ✅ | Settings Providers tab can add/edit/delete Gemini/OpenAI-compatible providers, set optional context windows, rotate API keys via secure storage, canonicalize provider keys, and surface provider/role config validation; broader provider setup UI is future feature work |
| /models | ✅ | alias for /model picker |
| /compact | ✅ | calls Haiku to summarize |
| /permissions | ✅ | shows gate state |
| /hooks | ✅ | shows configured hooks |
| /login | ✅ | inline OAuth picker |
| /account, /accounts | ✅ | settings accounts panel alias; account metadata stored in `~/.conduit/conduit.json` |
| /logout | ✅ | clears keychain |
| /cost | ✅ | tokens + estimated cost |
| /diff | ✅ | git diff --stat |
| /doctor | ✅ | binary/platform/git check |
| /files | ✅ | scans history for paths |
| /context | ✅ | token usage bar |
| /stats | ✅ | alias for /cost |
| /keybindings | ✅ | |
| /effort | ✅ | sets effort header |
| /fast | ✅ | toggles Haiku |
| /privacy-settings | ✅ | |
| /memory | ✅ | opens MEMORY.md path |
| /feedback | ✅ | opens GitHub issues |
| /release-notes | ✅ | opens releases page |
| /add-dir | ✅ | |
| /init | ✅ | prompt-inject to create CLAUDE.md |
| /review | ✅ | prompt-inject PR review |
| /commit | ✅ | prompt-inject git commit |
| /pr-comments | ✅ | prompt-inject PR comment fix |
| /fix | ✅ | prompt-inject issue fix |
| /export | ✅ | writes markdown file to disk |
| /usage | ✅ | token/cost breakdown by turn |
| /toggle-usage | ✅ | conduit-only: toggles Claude plan usage footer + fetcher |
| /vim | 🚫 REMOVED | descoped with vim mode — command removed from registry |
| /resume | ✅ | lists Conduit sessions with Claude history fallback/import; use --continue to restore |
| /rewind | ✅ | conversation snapshots via JSONL |
| /rename | ✅ | renames current session |
| /theme | ✅ | hot-swap palettes; persisted to conduit.json |
| /plan | ✅ | sets plan mode; EnterPlanMode tool wired |
| /council | ✅ | one-shot debate regardless of permission mode; /council-history lists transcripts |
| /branch | 🚫 REMOVED | conversation branching deferred; command removed from registry |
| /mcp | ✅ | internal/commands/mcp.go |
| /agents | ✅ | lists active sub-agents |
| /skills | ✅ | internal/commands/skills.go |
| /local | ✅ | hidden debug command: calls active MCP provider direct tool without changing default provider |
| /local-implement | ✅ | hidden debug command: calls active MCP provider implement tool with `output_format=diff` |
| /local-mode | ✅ | hidden compatibility command: toggles `activeProvider` between Claude and MCP |

---

## M6 — RTK in-process 🔶

| Component | Status | Notes |
|-----------|--------|-------|
| ANSI stripping | ✅ | internal/rtk/ansi.go |
| Command classifier | ✅ | 75 rules matching upstream registry.rs |
| BashTool integration | ✅ | filter applied to all bash output |
| **git** filter | ✅ | log/diff/status — faithful port |
| **gh / glab** filter | ✅ | run + general truncation |
| **go test/build/vet** filter | ✅ | failure extraction |
| **golangci-lint** filter | ✅ | |
| **cargo** filter | ✅ | test/build/clippy |
| **pytest** filter | ✅ | failure + summary extraction |
| **ruff/mypy** filter | ✅ | |
| **npm/pnpm/yarn/bun** filter | ✅ | |
| **vitest/jest** filter | ✅ | |
| **eslint/tsc** filter | ✅ | |
| **playwright** filter | ✅ | |
| **ruby/rspec/rubocop** filter | ✅ | |
| **dotnet build/test** filter | ✅ | |
| **docker/kubectl** filter | ✅ | |
| **terraform/tofu** filter | ✅ | |
| **aws** filter | ✅ | secret redaction included |
| **make/maven/swift** filter | ✅ | |
| **curl/wget/ping** filter | ✅ | |
| **ls/find/tree/grep** filter | ✅ | |
| **shellcheck/yamllint/etc** filter | ✅ | |
| RTK gain/discover commands | ✅ | internal/commands/rtk.go |
| SQLite tracking | ✅ | internal/rtk/track/track.go |

---

## M7 — MCP host ✅

| Component | Status |
|-----------|--------|
| stdio transport | ✅ |
| SSE transport | ✅ |
| HTTP transport | ✅ |
| Connection manager | ✅ |
| OAuth for MCP servers | ✅ |
| Server discovery | ✅ Claude/project/plugin + Conduit `~/.conduit/mcp.json` overlay |
| /mcp command | ✅ |

---

## M8 — Plugins + Skills + Session persistence 🔶

| Component | Status | Notes |
|-----------|--------|-------|
| Session transcript saving (JSONL) | ✅ | internal/session/, writes to ~/.conduit/projects with Claude fallback import |
| Session path encoding (djb2+sanitize) | ✅ | exact port of sessionStoragePortable.ts |
| --continue flag (resume latest session) | ✅ | cmd/claude/main.go |
| /resume command (list sessions) | ✅ | shows previous sessions with age |
| /export (markdown export) | ✅ | writes to disk |
| Conversation snapshots (/rewind) | ✅ | JSONL-based; /rewind wired |
| Session title persistence (/rename) | ✅ | shown in status bar; /rename persists |
| MEMORY.md auto-memory | ✅ | ScanMemories, /memory list/show/scan |
| Skill discovery + execution | ✅ | internal/tools/skilltool/ + internal/plugins/loader.go |
| Plugin loader | ✅ | internal/plugins/loader.go — manifest loading, install, uninstall, enable/disable, MCP sync; Conduit-owned storage under ~/.conduit/plugins with Claude import/fallback |
| Plugin skills (SKILL.md) | ✅ | internal/plugins/loader.go loadSkills + internal/plugins/skills.go — all skills/*/SKILL.md files from installed plugins loaded into SkillLoader; surfaced in system prompt; tools: frontmatter enforced |
| Plugin hooks (hooks.json) | ✅ | internal/plugins/loader.go loadHooks + internal/plugins/hooks.go MergeHooksFrom — hooks/hooks.json merged into session hook list; CLAUDE_PLUGIN_ROOT injected into subprocess env |
| Plugin agents (agents/*.md) | ✅ | internal/plugins/loader.go loadAgents + internal/plugins/agents.go + agenttool — Task subagent_type dispatches to named agents with system prompt, model override, and tool allowlist |

---

## M9 — Multi-agent + Coordinator 🔶

| Component | Status | Notes |
|-----------|--------|-------|
| AgentTool (subagents) | ✅ | internal/tools/agenttool/; RunSubAgent implemented |
| Swarm/coordinator | ✅ | internal/coordinator/coordinator.go; system prompt injection, task-notification XML, MCP context |
| SendMessageTool | 🔲 | Team messaging feature; descoped |
| RemoteTriggerTool | 🔲 | Remote-only (M10); descoped |
| **Council mode** | ✅ | internal/tui/council.go — conduit-original; parallel debate + synthesis across N models; chat path and ExitPlanMode path both open plan-approval picker |
| · Parallel critique rounds | ✅ | goroutine pool; no sequential bottleneck |
| · Cancellable context / Esc | ✅ | ctx derived from context.Background(); Ctrl+C cancels all in-flight members |
| · Per-member timeout | ✅ | 30s default; `councilMemberTimeoutSec` in conduit.json |
| · Convergence detection | ✅ | Jaccard similarity + `<council-agree/>` tag; threshold via `councilConvergenceThreshold` |
| · Real Usage / cost tracking | ✅ | EventUsage accumulation in RunSubAgentTyped; displayed in footer |
| · Round/active progress badge | ✅ | ⚖ council · round N/M · K active |
| · Debate transcript persistence | ✅ | ~/.conduit/projects/<hash>/council/<timestamp>.md |
| · Dedicated synthesizer model | ✅ | `councilSynthesizer` setting selects a specific provider |
| · Member roles | ✅ | architect/skeptic/perf-reviewer via `councilRoles` map |
| · Voting / weighted synthesis | ✅ | scoring pass orders plans by peer votes before synthesis |
| · /council slash command | ✅ | mode-independent one-shot debate; /council-history lists transcripts |
| · council_test.go | ✅ | 15 table-driven tests; regex, roster, convergence, roles, voting, cost |
| · Council read-only gate | ✅ | ModeCouncil denies (not asks) non-read-only tools; EnterPlanMode returns error; directive updated to skip EnterPlanMode step |
| **Plan-approval modal** | ✅ | Inset take-over overlay (1 row / 3 col padding from viewport); shows full plan in scrollable viewport; 4 options incl. "chat about this"; mouse drag-to-copy works inside modal |

---

## M10 — Bridge (IDE) 🔲

Descoped for now — not part of the "orchestration and brains" core.

---

## M11 — Cosmetic parity 🔶

| Component | Status | Notes |
|-----------|--------|-------|
| Image paste / drag-drop | ✅ | internal/attach/clipboard.go, dragdrop.go, resize.go |
| PDF paste / @file handling | ✅ | internal/attach/pdf.go, atmention.go |
| [N lines pasted] shortening | ✅ | internal/tui/updatehandlers.go stores large pastes as `[Pasted text #N +X lines]` placeholders and expands them before submit |
| Syntax highlighting | ✅ | internal/tui/syntaxhighlight.go |
| Buddy | ✅ | permanent /buddy command + companion rendering; one-time KAIROS promo descoped |
| Voice STT | 🚫 REMOVED | local STT deferred; Anthropic private endpoint unavailable |

---

## M12 — Security Hardening + Conformance Tests 🔶

| Component | Status | Notes |
|-----------|--------|-------|
| Conduit global/project state | ✅ | `internal/globalconfig/` + `~/.conduit/conduit.json` — trust state, numStartups |
| Workspace trust dialog | ✅ | `internal/tui/trustpanel.go` — mirrors decoded/5053.js |
| Trust ancestor walk | ✅ | Parent trust implies child; CLAUDE_CODE_SANDBOXED bypass |
| `SuggestRule` path traversal hardening | ✅ | `filepath.Clean` before glob computation |
| `permissionMode` display synced from gate | ✅ | Fixed in `tui.New()`; Conduit writes active mode to `~/.conduit/conduit.json` and mode changes re-resolve role provider/model |
| Message assembly conformance test | ✅ | `TestLoop_MessageAssembly_ToolUseResultPairing` |
| Ask-mode permission flow test | ✅ | `TestLoop_AskMode_AlwaysAllowAddsSessionRule` |
| PostToolUse hook conformance tests | ✅ | Output field, non-matching skip |
| HTTP hook conformance tests | ✅ | `internal/hooks/hooks_http_test.go` — block/approve/server-error |
| Async hook non-blocking test | ✅ | `TestRunHook_AsyncReturnsImmediately` |
| Fuzz targets (permission rules, JSON-RPC) | 🔲 | SSE parser fuzz exists; others deferred |
| Trust-gating for hooks/plugins at load time | ✅ | `settings.FilterUntrustedHooks` strips project-local hooks when cwd is untrusted; applied in main.go before loop + TUI start |

---

## Tooling

| Tool | Status | Notes |
|------|--------|-------|
| Wire-fingerprint drift detector | ✅ | `scripts/wire-check/` — `make verify-wire` decodes the installed claude binary (via bun-demincer), extracts headers/betas/cch/OAuth/tools by string-anchor pattern matching, diffs against conduit's pinned constants. Tracks history in `scripts/wire-check/history/`. Last tracked upstream: **v2.1.133** (cch=`00000`). |
| GoReleaser config | ✅ | `.goreleaser.yml` — darwin/linux/windows × amd64/arm64, macOS universal binary, ad-hoc codesign on darwin, SBOMs (syft), source archive. Publishers: Homebrew tap, Scoop bucket, winget PR. |
| Update notifier | ✅ | `internal/updater/` — GitHub Releases polling, 24h cache, install-method detection (brew/scoop/winget/go-install/direct). Async startup check + `conduit update` subcommand. Skipped on `AppVersion=="dev"`. |
| Distribution channels | 🔶 | GitHub Releases ✅. Homebrew tap (`Icehunter/homebrew-tap`) and Scoop bucket (`Icehunter/scoop-bucket`) require GitHub repo creation + `HOMEBREW_TAP_TOKEN`/`SCOOP_TOKEN`/`WINGET_TOKEN` secrets before next release. See `docs/release.md`. |

---

## Known lies / misleading behavior

| Issue | Location | Fix milestone |
|-------|----------|---------------|
| Conversation branching is not implemented | internal/commands/ | deferred; `/branch` is not registered |
