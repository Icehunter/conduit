# conduit Implementation Status

Last updated: 2026-05-01

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
| BashTool | ✅ | internal/tools/bashtool/ |
| FileReadTool | ✅ | internal/tools/filereadtool/ |
| FileWriteTool | ✅ | internal/tools/filewritetool/ |
| FileEditTool | ✅ | internal/tools/fileedittool/ |
| GrepTool | ✅ | internal/tools/greptool/ |
| GlobTool | ✅ | internal/tools/globtool/ |
| Cost tracker | ✅ | tallied in model.go |

---

## M3 — TUI ✅

| Component | Status | Notes |
|-----------|--------|-------|
| Bubble Tea main loop | ✅ | internal/tui/ |
| Message viewport | ✅ | |
| Input box | ✅ | |
| Slash command picker (fuzzy) | ✅ | |
| Tab completion | ✅ | |
| Input history (↑↓) | ✅ | |
| Vim mode | ❌ STUB | toggled but no actual vim keybindings |
| Status bar | ✅ | model, context%, cost |
| Welcome card (two-panel) | ✅ | profile fetched from oauth/profile |
| Permission prompt modal | ✅ | |
| Login picker modal | ✅ | |
| Ctrl+Y copy code block | ✅ | |
| Interrupt (Ctrl+C) | ✅ | |
| Markdown rendering | 🔶 | basic — no syntax highlighting |
| Code block highlighting | ❌ | plain text only |

---

## M4 — All tools 🔶

| Tool | Status | Notes |
|------|--------|-------|
| BashTool | ✅ | |
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
| AgentTool | 🔲 M9 | subagent dispatch |
| LSPTool | 🔲 M7 | needs LSP client |
| MCPTool | 🔲 M7 | needs MCP host |
| REPLTool | ✅ | node/python3/bash via temp file (no shell injection) |
| SkillTool | 🔲 M8 | |
| TaskCreateTool | ✅ | in-process store |
| TaskGetTool | ✅ | |
| TaskListTool | ✅ | |
| TaskOutputTool | ✅ | |
| TaskStopTool | ✅ | |
| TaskUpdateTool | ✅ | |
| RemoteTriggerTool | 🔲 M9 | |
| SendMessageTool | 🔲 M9 | |
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
| /clear | ✅ | |
| /exit, /quit | ✅ | |
| /model | ✅ | lists and switches models |
| /compact | ✅ | calls Haiku to summarize |
| /permissions | ✅ | shows gate state |
| /hooks | ✅ | shows configured hooks |
| /login | ✅ | inline OAuth picker |
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
| /usage | ❌ STUB | just links to settings page |
| /vim | ❌ STUB | toggles flag but no actual vim mode |
| /resume | ✅ | lists previous sessions; use --continue to restore |
| /rewind | ❌ STUB | needs conversation snapshots |
| /rename | ❌ STUB | needs session persistence (titles) |
| /theme | ❌ STUB | needs theme system |
| /plan | ❌ STUB | needs plan mode |
| /branch | ❌ STUB | needs conversation branching |
| /mcp | 🔲 M7 | needs MCP host |
| /agents | 🔲 M9 | needs multi-agent coordinator |
| /skills | 🔲 M8 | needs skill system |

---

## M6 — RTK in-process 🔶

| Component | Status | Notes |
|-----------|--------|-------|
| ANSI stripping | ✅ | internal/rtk/ansi.go |
| Command classifier | ✅ | 75 rules matching upstream registry.rs |
| BashTool integration | ✅ | filter applied to all bash output |
| **git** filter | 🔶 | log/diff/status — faithful port |
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
| RTK gain/discover commands | 🔲 | |
| SQLite tracking | 🔲 | |

---

## M7 — MCP host 🔲

| Component | Status |
|-----------|--------|
| stdio transport | 🔲 |
| SSE transport | 🔲 |
| HTTP transport | 🔲 |
| Connection manager | 🔲 |
| OAuth for MCP servers | 🔲 |
| Server discovery | 🔲 |
| /mcp command | 🔲 |

---

## M8 — Plugins + Skills + Session persistence 🔶

| Component | Status | Notes |
|-----------|--------|-------|
| Session transcript saving (JSONL) | ✅ | internal/session/, mirrors TS sessionStorage.ts |
| Session path encoding (djb2+sanitize) | ✅ | exact port of sessionStoragePortable.ts |
| --continue flag (resume latest session) | ✅ | cmd/claude/main.go |
| /resume command (list sessions) | ✅ | shows previous sessions with age |
| /export (markdown export) | ✅ | writes to disk |
| Conversation snapshots (/rewind) | 🔲 | needs snapshot entries in JSONL |
| Session title persistence (/rename) | 🔲 | JSONL entry type exists, not wired |
| MEMORY.md auto-memory | 🔲 | |
| Skill discovery + execution | 🔲 | |
| Plugin loader | 🔲 | |

---

## M9 — Multi-agent + Coordinator 🔲

| Component | Status |
|-----------|--------|
| AgentTool (subagents) | 🔲 |
| Swarm/coordinator | 🔲 |
| SendMessageTool | 🔲 |
| RemoteTriggerTool | 🔲 |

---

## M10 — Bridge (IDE) 🔲

Descoped for now — not part of the "orchestration and brains" core.

---

## M11 — Cosmetic parity 🔲

| Component | Status |
|-----------|--------|
| Image paste / drag-drop | 🔲 |
| [N lines pasted] shortening | 🔲 |
| Syntax highlighting | 🔲 |
| Buddy / KAIROS | 🔲 |
| Voice STT | 🔲 |

---

## Known lies / misleading behavior

| Issue | Location | Fix milestone |
|-------|----------|---------------|
| /vim toggles a flag but input isn't actually vim | session.go | M11 |
| /export returns a path but doesn't write the file | session.go | M8 |
| RTK shows no savings metric to user | bashtool | M6 |
| Markdown code blocks have no syntax highlighting | render.go | M11 |
| /doctor says "use /login" but doesn't show actual auth state | session.go | M8 |
