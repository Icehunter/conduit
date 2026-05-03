# Conduit ↔ Claude Code v2.1.126 Parity Map

> **Purpose:** 1:1 functional mapping between the Claude Code TypeScript source,
> the decompiled v2.1.126 binary chunks, and the Go conduit implementation.
> Used for milestone planning, descoping decisions, and tracking new feature
> ingestion from future deobfuscated releases.
>
> **Status legend:**
> - ✅ Complete — implemented and tested in conduit
> - 🟡 Partial — exists but missing edge cases or sub-features
> - ❌ Missing — not yet implemented
> - ⬛ Descoped — intentionally excluded
> - 🔲 N/A — not applicable to Go binary (React/Ink UI idioms, etc.)

## Milestone completions (2026-05)

| Milestone | Status | Key deliverables |
|-----------|--------|-----------------|
| M-A CLAUDE.md loading | ✅ | Directory walk, @include, TypeUser/Project/Local, system block injection |
| M-B Agent/API gaps | ✅ | Exponential backoff (429), HTTP proxy, auto-compact, thinking budget, rate-limit display |
| M-C Missing tools | ✅ | EnterPlanMode, ExitPlanMode, AskUserQuestion, Config, StructuredOutput |
| M-D Missing slash commands | ✅ | /status, /tasks, /agents, /thinkback, /color, /copy, /search, LiveState fix |
| M-E Hook completion | ✅ | HTTP hooks, prompt hooks, agent hooks, async hooks, desktop notifications |
| M-F Session completion | ✅ | Cost persistence, session titles in /resume, /search, file access tracking |
| M-G Config completion | ✅ | env injection, XDG/Windows paths, claudeDir(), settings.ApplyEnv() |
| M-H MCP completion | ✅ | ListResources, ReadResource on stdio+HTTP clients; ListMcpResources/ReadMcpResource tools |
| M-I TUI polish | ✅ | Full GFM: headings, italic, strikethrough, task lists, tables, blockquotes, H-rules |
| M-J Worktree support | ✅ | EnterWorktree, ExitWorktree, git worktree add/remove, sanitizeSlug |
| M-K Rate limit display | ✅ | anthropic-ratelimit-* headers, warning <20%, EventRateLimit, status bar |
| M-L Token counting + fast mode | ✅ | /fast (⚡ badge), /effort (thinking budget), model.ThinkingBudgets map |
| M-N Memory completion | ✅ | ScanMemories, RelevantMemories, /memory list/show/scan, age tracking |

**Test count:** 414 passing, race clean (2026-05-02)

---

## 1. Auth & OAuth

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| OAuth 2.0 + PKCE flow | `utils/auth.ts` (2002 LOC) | `1220.js`, `0533.js` | `internal/auth/flow.go`, `pkce.go`, `authurl.go` | ✅ | |
| Token exchange (code → token) | `services/oauth/` | `1220.js` | `internal/auth/token.go` | ✅ | |
| Token refresh | `utils/auth.ts` | `1220.js` | `internal/auth/persist.go` `EnsureFresh` | ✅ | |
| Token persistence (keychain) | `utils/auth.ts` | `1220.js` | `internal/secure/`, `internal/auth/persist.go` | ✅ | Uses file-based + go-keyring |
| OAuth callback HTTP listener | `services/oauth/auth-code-listener.ts` | `0533.js` | `internal/auth/listener.go` | ✅ | |
| Browser launch for login | `utils/auth.ts` | `0533.js` | `internal/auth/browser.go` | ✅ | |
| Auth file descriptor tokens | `utils/authFileDescriptor.ts` | — | ❌ | ❌ | FD-based token passing |
| Portable auth | `utils/authPortable.ts` | — | ❌ | ❌ | |
| Profile fetch (name, email) | `utils/auth.ts` | `1220.js` | `internal/profile/profile.go` | ✅ | |
| Multi-account support | `utils/auth.ts` | — | ❌ | ❌ | Single account only |
| Token revocation | `utils/auth.ts` | — | ❌ | ❌ | |
| JWT utils (bridge auth) | `bridge/jwtUtils.ts` | — | ❌ | ⬛ | Bridge-only |
| Trusted device auth | `bridge/trustedDevice.ts` | — | ❌ | ⬛ | Bridge-only |
| Work secret | `bridge/workSecret.ts` | — | ❌ | ⬛ | Bridge-only |
| AWS auth status | `components/AwsAuthStatusBox.tsx` | — | ❌ | ⬛ | AWS-specific |
| mTLS certificate handling | `utils/mtls.ts` | — | ❌ | ⬛ | Enterprise-only |
| CA certificate management | `utils/caCerts.ts`, `caCertsConfig.ts` | — | ❌ | ⬛ | Enterprise-only |

---

## 2. API Client & SSE Streaming

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| HTTP POST /v1/messages | `utils/api.ts` | `0158.js`, `4500.js` | `internal/api/client.go` | ✅ | |
| Streaming SSE parsing | `services/api/` | `0137.js`, `0156.js` | `internal/sse/parser.go`, `internal/api/stream.go` | ✅ | |
| Required headers (User-Agent, x-app, beta) | `utils/api.ts` | `4500.js` | `internal/api/client.go` | ✅ | Exact headers matched |
| Cache-control on system blocks | `services/api/` | `2831.js` | `internal/api/client.go` | ✅ | ephemeral + scope=global |
| 401 refresh + retry | `utils/api.ts` | `4500.js` | `internal/api/client.go` | ✅ | |
| 429 retry-after handling | `utils/api.ts` | `4500.js` | `internal/api/retry.go` | ✅ | Exp backoff: base 1s, 2×, max 32s, jitter, 5 retries |
| Request timeout (600s) | `utils/api.ts` | `4500.js` | `internal/api/client.go` | ✅ | X-Stainless-Timeout: 600 |
| Stainless headers | `utils/api.ts` | `4500.js` | `internal/api/client.go` | ✅ | |
| HTTP proxy support | `utils/proxy.ts` | — | `internal/api/retry.go` | ✅ | HTTPS_PROXY/HTTP_PROXY via NewClientWithProxy |
| VCR request recording/playback | `services/vcr.ts` | — | ❌ | ⬛ | Debug/testing only |
| API pre-connect optimization | `utils/apiPreconnect.ts` | — | `cmd/conduit/main.go` | ✅ | Background HEAD to api.anthropic.com during startup |
| Rate limit quota tracking | `services/claudeAiLimits.ts` | — | `internal/ratelimit/ratelimit.go` | ✅ | Parse anthropic-ratelimit-* headers, <20% warning in status bar |
| Rate limit messages | `services/rateLimitMessages.ts` | — | `internal/ratelimit/ratelimit.go` | ✅ | WarningMessage() in status bar |
| Token estimation | `services/tokenEstimation.ts` | — | `internal/tui/model.go` | 🟡 | Char/4 estimate; /context shows breakdown |
| Cost tracking | `cost-tracker.ts` (323 LOC) | — | `internal/session/extras.go` | ✅ | AppendCost per turn, LoadCost on resume |

---

## 3. Agent Loop & Query Engine

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Main agentic loop (query → tool → response) | `QueryEngine.ts` (1295 LOC), `query.ts` (1729 LOC) | `3585.js`, `3918.js`, `4091.js` | `internal/agent/loop.go` | ✅ | |
| Multi-turn tool dispatch | `QueryEngine.ts` | `3585.js` | `internal/agent/loop.go` `executeTools` | ✅ | |
| Parallel tool execution (bounded pool) | `coordinator/coordinatorMode.ts` | — | `internal/agent/loop.go` | ✅ | maxConcurrentTools=4 |
| System prompt assembly | `constants/prompts.ts` | `2831.js` | `internal/agent/systemprompt.go` | ✅ | |
| Billing header injection | — | `2831.js` | `internal/agent/systemprompt.go` | ✅ | |
| Sub-agent spawning | `tools/AgentTool/` | — | `internal/agent/loop.go` `RunSubAgent` | ✅ | |
| Max turns limit | `query.ts` | — | `internal/agent/loop.go` | ✅ | |
| Context compaction (auto) | `services/compact/autoCompact.ts` | — | `internal/agent/loop.go` | ✅ | Fires at 80% inputTokens/MaxTokens |
| Micro-compaction | `services/compact/microCompact.ts` | — | ❌ | ❌ | |
| Session memory compaction | `services/compact/sessionMemoryCompact.ts` | — | ❌ | ❌ | |
| Token budget tracking | `query/tokenBudget.ts` | — | `internal/tui/model.go`, `internal/tui/livestate.go` | 🟡 | Shows ctx%, no hard limits |
| Extended thinking / effort modes | `utils/effort.ts` | — | `internal/model/model.go`, `internal/agent/loop.go` | ✅ | ThinkingBudgets map; /effort low\|medium\|high\|max; CLAUDE_THINKING_BUDGET env |
| Interleaved thinking | `constants/betas.ts` | — | `internal/agent/systemprompt.go` | ✅ | Beta header included |
| Stop hooks (clean shutdown) | `query/stopHooks.ts` | — | `internal/hooks/hooks.go` `RunStop` | ✅ | |
| Query profiler | `utils/queryProfiler.ts` | — | ❌ | ⬛ | Debug only |
| Conversation recovery on error | `utils/conversationRecovery.ts` | — | ❌ | ❌ | No mid-turn recovery |

---

## 4. Tools

### 4a. Core Tool Framework

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Tool interface & registry | `Tool.ts` (792 LOC), `tools.ts` (389 LOC) | — | `internal/tool/tool.go` | ✅ | |
| Permission gate per tool | `hooks/toolPermission/` | — | `internal/permissions/permissions.go` | ✅ | |
| Tool result envelope | `Tool.ts` | — | `internal/tool/tool.go` `Result` | ✅ | |
| ReadOnly + ConcurrencySafe flags | `Tool.ts` | — | `internal/tool/tool.go` | ✅ | |
| Tool schema (JSON Schema) | `Tool.ts` | — | `internal/tool/*.go` | ✅ | |
| Tool search / deferred loading | `tools/ToolSearchTool/` | — | `internal/tools/toolsearchtool/` | ✅ | |
| Synthetic output tool | `tools/SyntheticOutputTool/` | — | `internal/tools/syntheticoutputtool/` | ✅ | |

### 4b. Individual Tools

| Tool | TS Source | Decoded Chunk | Go (conduit) | Status | Notes |
|------|-----------|--------------|--------------|--------|-------|
| AgentTool (Task) | `tools/AgentTool/` | — | `internal/tools/agenttool/` | ✅ | Wire name "Task" |
| AskUserQuestion | `tools/AskUserQuestionTool/` | — | `internal/tools/askusertool/` | ✅ | Blocks on TUI permissionAskMsg |
| BashTool | `tools/BashTool/` (18 files) | — | `internal/tools/bashtool/` | ✅ | RTK filtering wired |
| BriefTool | `tools/BriefTool/` | — | ❌ | ⬛ | KAIROS-gated (GrowthBook build flag) |
| ConfigTool | `tools/ConfigTool/` | — | `internal/tools/configtool/` | ✅ | get/set model, modes, allow/deny, env |
| EnterPlanMode | `tools/EnterPlanModeTool/` | — | `internal/tools/planmodetool/` | ✅ | AskEnter callback, sets plan mode |
| EnterWorktree | `tools/EnterWorktreeTool/` | — | `internal/tools/worktreeTool/` | ✅ | git worktree add, switches cwd |
| ExitPlanMode | `tools/ExitPlanModeTool/` | — | `internal/tools/planmodetool/` | ✅ | AskApprove callback, resets mode |
| ExitWorktree | `tools/ExitWorktreeTool/` | — | `internal/tools/worktreeTool/` | ✅ | keep/remove action, branch cleanup |
| FileEditTool | `tools/FileEditTool/` | — | `internal/tools/fileedittool/` | ✅ | |
| FileReadTool | `tools/FileReadTool/` | — | `internal/tools/filereadtool/` | ✅ | |
| FileWriteTool | `tools/FileWriteTool/` | — | `internal/tools/filewritetool/` | ✅ | |
| GlobTool | `tools/GlobTool/` | — | `internal/tools/globtool/` | ✅ | |
| GrepTool | `tools/GrepTool/` | — | `internal/tools/greptool/` | ✅ | rg backend |
| ListMcpResources | `tools/ListMcpResourcesTool/` | — | `internal/tools/mcpresourcetool/` | ✅ | Lists from all connected servers |
| LSPTool | `tools/LSPTool/` | — | ❌ | ❌ | Language server |
| McpAuthTool | `tools/McpAuthTool/` | — | ❌ | ❌ | MCP OAuth |
| MCPTool | `tools/MCPTool/` | — | `internal/tools/mcptool/` | ✅ | MCP tool proxy |
| NotebookEditTool | `tools/NotebookEditTool/` | — | `internal/tools/notebookedittool/` | ✅ | |
| PowerShellTool | `tools/PowerShellTool/` | — | ❌ | ⬛ | Windows-only |
| ReadMcpResource | `tools/ReadMcpResourceTool/` | — | `internal/tools/mcpresourcetool/` | ✅ | Reads one resource by URI |
| RemoteTriggerTool | `tools/RemoteTriggerTool/` | — | ❌ | ⬛ | Remote-only (M10) |
| REPLTool | `tools/REPLTool/` | — | `internal/tools/repltool/` | ✅ | |
| ScheduleCronTool | `tools/ScheduleCronTool/` | — | ❌ | ⬛ | KAIROS-gated (isKairosCronEnabled) |
| SendMessageTool | `tools/SendMessageTool/` | — | ❌ | ⬛ | Team messaging (descoped) |
| SkillTool | `tools/SkillTool/` | — | `internal/tools/skilltool/` | ✅ | |
| SleepTool | `tools/SleepTool/` | — | `internal/tools/sleeptool/` | ✅ | |
| SyntheticOutputTool | `tools/SyntheticOutputTool/` | — | `internal/tools/syntheticoutputtool/` | ✅ | In-band coordinator signaling |
| TaskCreateTool | `tools/TaskCreateTool/` | — | `internal/tools/tasktool/` | ✅ | |
| TaskGetTool | `tools/TaskGetTool/` | — | `internal/tools/tasktool/` | ✅ | |
| TaskListTool | `tools/TaskListTool/` | — | `internal/tools/tasktool/` | ✅ | |
| TaskOutputTool | `tools/TaskOutputTool/` | — | `internal/tools/tasktool/` | ✅ | |
| TaskStopTool | `tools/TaskStopTool/` | — | `internal/tools/tasktool/` | ✅ | |
| TaskUpdateTool | `tools/TaskUpdateTool/` | — | `internal/tools/tasktool/` | ✅ | |
| TeamCreateTool | `tools/TeamCreateTool/` | — | ❌ | ❌ | Multi-agent teams |
| TeamDeleteTool | `tools/TeamDeleteTool/` | — | ❌ | ❌ | |
| TodoWriteTool | `tools/TodoWriteTool/` | — | `internal/tools/todowritetool/` | ✅ | |
| ToolSearchTool | `tools/ToolSearchTool/` | — | `internal/tools/toolsearchtool/` | ✅ | |
| WebFetchTool | `tools/WebFetchTool/` | — | `internal/tools/webfetchtool/` | ✅ | HTML→markdown |
| WebSearchTool | `tools/WebSearchTool/` | — | `internal/tools/websearchtool/` | ✅ | |

---

## 5. Permissions & Hooks

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Permission modes (default/acceptEdits/plan/bypass) | `utils/permissions/` | — | `internal/permissions/permissions.go` | ✅ | |
| Allow/deny/ask rule matching | `utils/permissions/` | — | `internal/permissions/permissions.go` | ✅ | Glob + exact match |
| Session-scoped allow | `utils/permissions/` | — | `internal/permissions/permissions.go` | ✅ | |
| Shift+Tab mode cycling | `keybindings/defaultBindings.ts` | — | `internal/tui/model.go` | ✅ | |
| Classifier approvals | `utils/classifierApprovals.ts` | — | ❌ | ❌ | ML-based approval |
| PreToolUse hooks | `utils/hooks/` | — | `internal/hooks/hooks.go` | ✅ | |
| PostToolUse hooks | `utils/hooks/` | — | `internal/hooks/hooks.go` | ✅ | |
| SessionStart hooks | `utils/hooks/` | — | `internal/hooks/hooks.go` | ✅ | |
| Stop hooks | `utils/hooks/` | — | `internal/hooks/hooks.go` | ✅ | |
| Hook approve directive | `utils/hooks/` | — | `internal/hooks/hooks.go` | ✅ | Bypasses AskPermission |
| Hook block directive | `utils/hooks/` | — | `internal/hooks/hooks.go` | ✅ | |
| HTTP hooks | `utils/hooks/execHttpHook.ts` | — | `internal/hooks/http.go` | ✅ | POST JSON, parse decision |
| Prompt hooks | `utils/hooks/execPromptHook.ts` | — | `internal/hooks/llm.go` | ✅ | Sub-agent prompt, inject result |
| Agent hooks | `utils/hooks/execAgentHook.ts` | — | `internal/hooks/llm.go` | ✅ | Spawns full agent loop |
| Async hook registry | `utils/hooks/AsyncHookRegistry.ts` | — | `internal/hooks/hooks.go` | ✅ | Non-blocking goroutine for async=true |
| Notification hooks | `hooks/notifs/` (16 files) | — | `internal/hooks/notify.go` | ✅ | macOS osascript / Linux notify-send |
| Bridge permission callbacks | `bridge/bridgePermissionCallbacks.ts` | — | ❌ | ⬛ | Bridge-only |
| Interactive permission prompt (TUI) | `screens/REPL.tsx` | — | `internal/tui/model.go` | ✅ | |

---

## 6. TUI & Rendering

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Main REPL screen | `screens/REPL.tsx` (5005 LOC) | `0219.js`+ | `internal/tui/model.go` | ✅ | Bubble Tea vs React/Ink |
| Message display (streaming) | `components/Messages.tsx` | — | `internal/tui/render.go` | ✅ | |
| Markdown rendering | `components/Markdown.tsx` | — | `internal/tui/render.go` | ✅ | Full GFM: tables, headings, task lists, strikethrough, blockquotes, italic |
| Syntax highlighting | `components/HighlightedCode.tsx` | — | `internal/tui/render.go` | 🟡 | Chroma-based |
| Spinner / thinking indicator | `components/Spinner.tsx` | — | `internal/tui/model.go` | ✅ | |
| Status bar | `components/StatusLine.tsx` | — | `internal/tui/model.go` | ✅ | |
| Permission mode badge | `components/StatusLine.tsx` | — | `internal/tui/model.go` | ✅ | |
| Input box (textarea) | `components/PromptInput/` | — | `internal/tui/model.go` | ✅ | |
| Input history (up/down) | `screens/REPL.tsx` | — | `internal/tui/model.go` | ✅ | |
| Slash command picker | `screens/REPL.tsx` | — | `internal/tui/model.go` | ✅ | |
| Tab completion | `screens/REPL.tsx` | — | `internal/tui/model.go` | ✅ | |
| Session resume picker | `screens/ResumeConversation.tsx` | — | `internal/tui/model.go` | ✅ | |
| MCP panel | — | — | `internal/tui/model.go` (panel) | ✅ | conduit-only |
| Plugin panel (full) | `commands/plugin/` | — | `internal/tui/pluginpanel.go` | ✅ | conduit-only |
| Login flow UI | `components/ConsoleOAuthFlow.tsx` | — | `internal/tui/login.go` | 🟡 | Basic, no fancy UI |
| Cost display | `components/Stats.tsx` | — | `internal/tui/model.go` | 🟡 | Status bar only |
| Context visualization | `components/ContextVisualization.tsx` | — | `/context` command | ✅ | Bar chart of tokens: system/history/tools/remaining |
| Virtual message list / scroll | `components/VirtualMessageList.tsx` | — | `internal/tui/model.go` (viewport) | 🟡 | No true virtualization |
| Code copy (Ctrl+Y) | `screens/REPL.tsx` | — | `internal/tui/model.go` | ✅ | |
| Ctrl+C interrupt | `screens/REPL.tsx` | — | `internal/tui/model.go` | ✅ | |
| Flash messages | — | — | `internal/tui/model.go` | ✅ | conduit-only |
| Doctor screen | `screens/Doctor.tsx` | — | `/doctor` command (text) | 🟡 | Text output; no full TUI panel |
| Stats screen | `components/Stats.tsx` | — | `internal/tui/settingspanel.go` | ✅ | /stats opens Settings panel → Stats tab; Overview + Models + asciigraph chart |
| Log selector | `components/LogSelector.tsx` | — | ❌ | ❌ | |
| Global search dialog | `components/GlobalSearchDialog.tsx` | — | ❌ | ❌ | |
| Model picker dialog | `components/ModelPicker.tsx` | — | `internal/tui/model.go` (pickerState) | ✅ | /model with no args opens picker; ↑↓/jk Enter; current marked ● |
| Theme picker | `components/ThemePicker.tsx` | — | `internal/tui/model.go` (pickerState) | ✅ | /theme with no args opens picker; lists built-ins + user themes |
| Output style picker | `components/OutputStylePicker.tsx` | — | `internal/tui/model.go` (pickerState) | ✅ | /output-style with no args opens picker |
| Feedback dialog | `components/Feedback.tsx` | — | ❌ | ⬛ | Anthropic-internal |
| Onboarding flow | `components/OnboardingComponent.tsx` | — | `internal/tui/run.go` | 🟡 | No-auth hint injected into message list; no full wizard |
| Coordinator agent status | `components/CoordinatorAgentStatus.tsx` | — | ❌ | ❌ | |
| Vim mode | `vim/` (5 files, 1513 LOC) | — | ❌ | ❌ | |
| Custom keybindings | `keybindings/` (14 files) | — | ❌ | ❌ | Hardcoded only |
| Image/PDF paste | `utils/imagePaste.ts`, `pdf.ts` | — | ❌ | ❌ | M13 |
| Drag-drop attachments | `utils/attachments.ts` | — | ❌ | ❌ | M13 |
| Ink rendering engine | `ink/` (96 files, 13306 LOC) | — | Bubble Tea | 🔲 | Different framework |

---

## 7. Slash Commands

| Command | TS Source | Go (conduit) | Status | Notes |
|---------|-----------|--------------|--------|-------|
| /help | `commands/help/` | `internal/commands/builtin.go` | ✅ | |
| /clear | `commands/clear/` | `internal/commands/builtin.go` | ✅ | |
| /exit | `commands/exit/` | `internal/commands/misc.go` | ✅ | |
| /model | `commands/model/` | `internal/commands/builtin.go` | ✅ | |
| /compact | `commands/compact/` | `internal/commands/builtin.go` | ✅ | |
| /permissions | `commands/permissions/` | `internal/commands/builtin.go` | ✅ | |
| /hooks | `commands/hooks/` | `internal/commands/builtin.go` | ✅ | |
| /login | `commands/login/` | `internal/commands/misc.go` | ✅ | |
| /logout | `commands/logout/` | `internal/commands/session.go` | ✅ | |
| /resume | `commands/resume/` | `internal/commands/session.go` | ✅ | |
| /rewind | `commands/rewind/` | `internal/commands/session.go` | ✅ | |
| /cost | `commands/cost/` | `internal/commands/session.go` | ✅ | |
| /export | `commands/export/` | `internal/commands/session.go` | ✅ | |
| /add-dir | `commands/add-dir/` | `internal/commands/misc.go` | ✅ | |
| /privacy-settings | `commands/privacy-settings/` | `internal/commands/misc.go` | ✅ | |
| /mcp | `commands/mcp/` | `internal/commands/mcp.go` | ✅ | |
| /plugin | `commands/plugin/` (17 files) | `internal/commands/plugins.go` | ✅ | |
| /reload-plugins | `commands/reload-plugins/` | `internal/commands/plugins.go` | ✅ | |
| /skills | `commands/skills/` | `internal/commands/skills.go` | ✅ | |
| /output-style | `commands/output-style/` | `internal/commands/outputstyle.go` | ✅ | |
| /rtk gain | — | `internal/commands/rtk.go` | ✅ | conduit-only |
| /buddy | — | `internal/commands/buddy.go` | ✅ | conduit-only |
| /keybindings | `commands/keybindings/` | `internal/commands/session.go` | 🟡 | Shows hardcoded list |
| /plan | `commands/plan/` | `internal/commands/misc.go` | ✅ | Sets plan mode; EnterPlanMode tool wired |
| /memory | `commands/memory/` | `internal/commands/session.go` | ✅ | list\|show\|scan subcommands; memdir.ScanMemories |
| /config | `commands/config/` | `internal/commands/session.go` | ✅ | list / get <key> / set <key> <value>; raw-map write |
| /context | `commands/context/` | `internal/commands/session.go` | ✅ | Bar chart + token breakdown + remaining |
| /diff | `commands/diff/` | `internal/commands/session.go` | ✅ | git diff of files edited this session |
| /doctor | `commands/doctor/` | `internal/commands/session.go` | ✅ | auth, MCP, settings health check |
| /effort | `commands/effort/` | `internal/commands/session.go` | ✅ | low\|medium\|high\|max thinking budget |
| /fast | `commands/fast/` | `internal/commands/session.go` | ✅ | Toggles model; ⚡ in status bar |
| /feedback | `commands/feedback/` | ❌ | ⬛ | Anthropic-internal |
| /files | `commands/files/` | `internal/commands/session.go` | ✅ | Files read/written this session |
| /ide | `commands/ide/` | ❌ | ⬛ | Bridge-only |
| /review | `commands/review/` | `internal/commands/session.go` | ✅ | /compact + summary |
| /session | `commands/session/` | `internal/commands/session.go` | ✅ | ID, path, message count, duration |
| /stats | `commands/stats/` | `internal/commands/session.go` + settings panel | ✅ | Opens Stats panel (Overview + Models tabs) |
| /status | `commands/status/` | `internal/commands/session.go` | ✅ | Model, mode, session ID, cost, context% |
| /tag | `commands/tag/` | `internal/commands/session.go` | ✅ | Tag/clear current session; tag shown in /session and /resume picker |
| /tasks | `commands/tasks/` | `internal/commands/session.go` | ✅ | Lists active TaskTool tasks |
| /theme | `commands/theme/` | `internal/commands/misc.go` + `internal/theme/` | ✅ | Switch palette: dark/light/dark-accessible/light-accessible; hot-swap via OnChange listeners; persisted to settings.json |
| /usage | `commands/usage/` | `internal/commands/session.go` | ✅ | Token/cost breakdown by turn |
| /vim | `commands/vim/` | ❌ | ❌ | Vim mode not implemented |
| /voice | `commands/voice/` | ❌ | ⬛ | Requires cgo audio |
| /rename | `commands/rename/` | `internal/commands/session.go` | ✅ | Renames current session |
| /sandbox-toggle | `commands/sandbox-toggle/` | ❌ | ⬛ | Anthropic-internal |
| /install-github-app | `commands/install-github-app/` | ❌ | ⬛ | Anthropic-internal |
| /install-slack-app | `commands/install-slack-app/` | ❌ | ⬛ | Anthropic-internal |
| /bridge | `commands/bridge/` | ❌ | ⬛ | Bridge-only |
| /remote-env | `commands/remote-env/` | ❌ | ⬛ | Remote-only |
| /remote-setup | `commands/remote-setup/` | ❌ | ⬛ | Remote-only |
| /agents | `commands/agents/` | `internal/commands/session.go` | ✅ | Lists active sub-agents |
| /stickers | `commands/stickers/` | ❌ | ⬛ | Cosmetic |
| /thinkback | `commands/thinkback/` | `internal/commands/session.go` | ✅ | Shows last thinking blocks |
| /thinkback-play | `commands/thinkback-play/` | ❌ | ❌ | Replay thinking animation |
| /upgrade | `commands/upgrade/` | ❌ | ⬛ | Auto-update |
| /color | `commands/color/` | `internal/commands/session.go` | ✅ | Toggle ANSI color output |
| /copy | `commands/copy/` | `internal/commands/session.go` | ✅ | Copies last response to clipboard |
| /search | — | `internal/commands/session.go` | ✅ | conduit-only; scans JSONL transcripts |
| /pr-comments | `commands/pr_comments/` | `internal/commands/session.go` | ✅ | conduit-only; PR review workflow |
| /passes | `commands/passes/` | ❌ | ❌ | Multi-pass analysis |
| /rate-limit-options | `commands/rate-limit-options/` | ❌ | ❌ | Rate limit config |
| /release-notes | `commands/release-notes/` | ❌ | ⬛ | Anthropic-internal |
| /extra-usage | `commands/extra-usage/` | ❌ | ❌ | Extended usage breakdown |
| /terminalSetup | `commands/terminalSetup/` | ❌ | ❌ | Shell integration setup |

---

## 8. MCP Host

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| stdio transport | `services/mcp/` | `0154.js` | `internal/mcp/client.go` | ✅ | |
| HTTP/SSE transport | `services/mcp/` | `0154.js` | `internal/mcp/client.go` | ✅ | |
| Tool discovery & proxy | `services/mcp/`, `tools/MCPTool/` | `0402.js` | `internal/tools/mcptool/` | ✅ | |
| Config loading (claude.json) | `services/mcp/` | — | `internal/mcp/config.go` | ✅ | |
| Config loading (.mcp.json) | `services/mcp/` | — | `internal/mcp/config.go` | ✅ | |
| Plugin MCP server registration | `services/mcp/` | — | `internal/mcp/manager.go` `SyncPluginServers` | ✅ | |
| Server lifecycle (connect/disconnect) | `services/mcp/` | — | `internal/mcp/manager.go` | ✅ | |
| MCP server approval dialog | `components/MCPServerApprovalDialog.tsx` | — | ❌ | ❌ | No per-server approval prompt |
| OAuth for MCP servers | `services/mcp/` | — | ❌ | ❌ | McpAuthTool not implemented |
| MCP resource listing | `tools/ListMcpResourcesTool/` | — | `internal/mcp/manager.go`, `internal/tools/mcpresourcetool/` | ✅ | resources/list JSON-RPC |
| MCP resource reading | `tools/ReadMcpResourceTool/` | — | `internal/mcp/manager.go`, `internal/tools/mcpresourcetool/` | ✅ | resources/read JSON-RPC |
| MCP WebSocket transport | `utils/mcpWebSocketTransport.ts` | — | `internal/mcp/client_ws.go` | ✅ | nhooyr.io/websocket; type="ws"\|"websocket" in server config |
| MCP instructions delta | `utils/mcpInstructionsDelta.ts` | — | `internal/mcp/manager.go` + `cmd/conduit/main.go` | ✅ | Server instructions from initialize response injected into system prompt |
| LSP integration | `services/lsp/` (7 files) | — | ❌ | ❌ | |

---

## 9. Plugins & Skills

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Plugin manifest loading | `types/plugin.ts`, `services/plugins/` | `2484.js` | `internal/plugins/loader.go` | ✅ | |
| Plugin installation (git clone) | `commands/plugin/` | — | `internal/plugins/install.go` | ✅ | |
| Plugin uninstallation | `commands/plugin/` | — | `internal/plugins/install.go` | ✅ | |
| Marketplace discovery | `commands/plugin/` | — | `internal/plugins/discover.go` | ✅ | |
| Install counts (GitHub stats) | `commands/plugin/` | — | `internal/plugins/discover.go` | ✅ | |
| Plugin enable/disable | `services/plugins/` | — | `internal/settings/settings.go` | ✅ | |
| Plugin MCP server sync | `services/mcp/` | — | `internal/mcp/manager.go` | ✅ | |
| Plugin output styles | `utils/plugins/loadPluginOutputStyles.ts` | — | `internal/outputstyles/outputstyles.go` | ✅ | LoadFromPluginDirs; merged at startup, plugin < user/project priority |
| Plugin slash commands | `services/plugins/` | — | `internal/plugins/loader.go` | ✅ | |
| Skill discovery from plugins | `skills/loadSkillsDir.ts` | — | `internal/tools/skilltool/` | ✅ | |
| Bundled/built-in skills | `skills/bundledSkills.ts` | — | `internal/skills/bundled.go` | ✅ | /simplify (3-agent code review) and /remember (memory promotion review) |
| Skill listing in system prompt | `skills/` | — | `internal/agent/systemprompt.go` | ✅ | |
| SkillTool invocation | `tools/SkillTool/` | — | `internal/tools/skilltool/` | ✅ | |
| Plugin marketplace management | `commands/plugin/` | — | `internal/plugins/marketplace.go` | ✅ | |
| Plugin signature verification | — | — | ❌ | ❌ | |
| Plugin version management | — | — | ❌ | ❌ | |
| Built-in plugins list | `plugins/builtinPlugins.ts` | — | ❌ | ❌ | |
| MagicDocs | `services/MagicDocs/` | — | ❌ | ⬛ | |

---

## 10. Memory System

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Memory directory path | `memdir/paths.ts` | `2829.js` | `internal/memdir/memdir.go` `Path` | ✅ | |
| MEMORY.md loading + truncation | `memdir/memdir.ts` | `2830.js` | `internal/memdir/memdir.go` | ✅ | 200-line / 25KB caps |
| Memory type taxonomy (4 types) | `memdir/memoryTypes.ts` | — | `internal/memdir/memdir.go` | ✅ | user/feedback/project/reference |
| Full behavioral instructions in prompt | `memdir/memdir.ts` | — | `internal/memdir/memdir.go` | ✅ | |
| Auto-dream gate (24h + 5 sessions) | `services/autoDream/` | `3899.js` | `internal/memdir/dream.go` | ✅ | |
| Auto-dream consolidation prompt | `services/autoDream/consolidationPrompt.ts` | — | `internal/memdir/dream.go` | ✅ | 4-phase prompt |
| Dream lock file | `services/autoDream/` | — | `internal/memdir/dream.go` | ✅ | |
| Memory scanning utilities | `memdir/memoryScan.ts` | — | `internal/memdir/scan.go` | ✅ | ScanMemories, FormatMemoryList, formatAge |
| Memory age tracking | `memdir/memoryAge.ts` | — | `internal/memdir/scan.go` | ✅ | ModTime tracking, human-readable age |
| Relevant memory search | `memdir/findRelevantMemories.ts` | — | `internal/memdir/scan.go` | ✅ | Keyword matching (no embeddings) |
| Memory extraction from conversations | `services/extractMemories/` | — | ❌ | ❌ | |
| Session memory management | `services/SessionMemory/` | `3901.js` | ❌ | ❌ | |
| Team memory paths | `memdir/teamMemPaths.ts` | — | ❌ | ⬛ | Team feature |
| Team memory prompts | `memdir/teamMemPrompts.ts` | — | ❌ | ⬛ | Team feature |
| Team memory sync | `services/teamMemorySync/` | — | ❌ | ⬛ | Team feature |

---

## 11. RTK (Token Compression)

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Command classification (75 rules) | RTK Rust source | — | `internal/rtk/registry.go` | ✅ | |
| Git filters | RTK Rust source | — | `internal/rtk/filters.go` | ✅ | |
| Go test filters | RTK Rust source | — | `internal/rtk/filters.go` | ✅ | |
| Cargo/Rust filters | RTK Rust source | — | `internal/rtk/filters.go` | ✅ | |
| npm/pnpm/yarn filters | RTK Rust source | — | `internal/rtk/filters.go` | ✅ | |
| Python/pytest filters | RTK Rust source | — | `internal/rtk/filters.go` | ✅ | |
| JS/TS (eslint, tsc, vitest) filters | RTK Rust source | — | `internal/rtk/filters.go` | ✅ | |
| AWS filters + secret redaction | RTK Rust source | — | `internal/rtk/filters.go` | ✅ | |
| Docker/kubectl/terraform filters | RTK Rust source | — | `internal/rtk/filters.go` | ✅ | |
| ANSI stripping | RTK Rust source | — | `internal/rtk/ansi.go` | ✅ | |
| SQLite tracking (history.db) | RTK Rust source | — | `internal/rtk/track/track.go` | ✅ | modernc.org/sqlite |
| /rtk gain command | RTK Rust binary | — | `internal/commands/rtk.go` | ✅ | |
| rtk discover (transcript scan) | RTK Rust binary | — | `internal/commands/rtk.go` | ✅ | Scans JSONL sessions for unclassified Bash commands; ranks by frequency |
| BashTool integration | — | — | `internal/tools/bashtool/` | ✅ | Filter on every exec |
| Env var stripping in classify | RTK Rust source | — | `internal/rtk/registry.go` | ✅ | |

---

## 12. Session & History

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| JSONL session transcript | `utils/sessionStorage.ts` | `3096.js` | `internal/session/session.go` | ✅ | |
| Session path sanitization | `utils/sessionStoragePortable.ts` | — | `internal/session/session.go` | ✅ | djb2 hash fallback |
| Session list (newest first) | `utils/sessionStorage.ts` | — | `internal/session/session.go` | ✅ | |
| Session resume (--continue) | `utils/sessionRestore.ts` | — | `cmd/claude/main.go` | ✅ | |
| Session title | `utils/sessionTitle.ts` | — | `internal/session/session.go`, `internal/tui/model.go` | ✅ | Shown in status bar; /rename persists; auto-title from first message |
| Session summary (compact) | `utils/sessionStorage.ts` | — | `internal/session/session.go` | 🟡 | SetSummary() exists |
| Message compaction | `services/compact/compact.ts` | — | `internal/compact/compact.go` | ✅ | |
| Auto-compaction | `services/compact/autoCompact.ts` | — | `internal/agent/loop.go` | ✅ | Fires at 80% inputTokens/MaxTokens |
| Conversation recovery | `utils/conversationRecovery.ts` | — | ❌ | ❌ | |
| File access history | `utils/fileHistory.ts` | — | `internal/session/extras.go` | ✅ | AppendFileAccess / LoadFileAccess |
| Session activity tracking | `utils/sessionActivity.ts` | — | `internal/session/extras.go` | 🟡 | LoadActivity returns first/last/idle from JSONL timestamps; remote keepalive heartbeat is descoped (bridge-only) |
| Session environment setup | `utils/sessionEnvironment.ts` | — | `internal/settings/env.go` | ✅ | ApplyEnv + cleanup restore |
| Session URL handling | `utils/sessionUrl.ts` | — | ❌ | ❌ | |
| Cost tracking persistence | `cost-tracker.ts` | — | `internal/session/extras.go` | ✅ | AppendCost per turn, LoadCost on resume |
| Transcript search | `utils/transcriptSearch.ts` | — | `internal/session/extras.go` | ✅ | Case-insensitive JSONL search |

---

## 13. Config & Settings

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Settings load (global + project) | `utils/config.ts` (1817 LOC) | `0628.js` | `internal/settings/settings.go` | ✅ | |
| Settings merge (global → project) | `utils/config.ts` | — | `internal/settings/settings.go` | ✅ | |
| Hook settings parsing | `schemas/hooks.ts` | — | `internal/settings/settings.go` | ✅ | |
| Plugin enable/disable | `utils/config.ts` | — | `internal/settings/settings.go` | ✅ | |
| Settings file preservation on update | `utils/config.ts` | — | `internal/settings/settings.go` | ✅ | Raw JSON map |
| CLAUDE.md loading | `utils/claudemd.ts` (1479 LOC) | — | `internal/claudemd/claudemd.go` | ✅ | Dir walk, @include, .claudeignore, per-session cache |
| Output style setting | `utils/config.ts` | — | `internal/settings/settings.go`, `internal/tui/run.go` | ✅ | Persisted to settings.json; loaded on startup |
| Environment variable management | `utils/env.ts`, `envDynamic.ts` | — | `internal/settings/env.go` | ✅ | ApplyEnv; session.Env injected into BashTool subprocess |
| Managed env constants | `utils/managedEnvConstants.ts` | — | ❌ | ❌ | |
| Remote managed settings | `services/remoteManagedSettings/` | — | ❌ | ⬛ | Anthropic-internal |
| Settings sync service | `services/settingsSync/` | — | ❌ | ⬛ | Anthropic-internal |
| Platform-specific settings | `utils/platformSettings.ts` | — | ❌ | ❌ | |
| GrowthBook feature flags | `utils/featureFlags.ts` | — | ❌ | ⬛ | Anthropic-internal |
| Migrations | `migrations/` (11 files) | — | ❌ | ❌ | Schema migration system |
| XDG base directory | `utils/xdg.ts` | — | `internal/settings/env.go` `claudeDir()` | ✅ | XDG_CONFIG_HOME/claude on Linux |
| Windows paths | `utils/windowsPaths.ts` | — | `internal/settings/env.go` `claudeDir()` | ✅ | %APPDATA%/claude on Windows |

---

## 14. Bridge (IDE Integration) — M10

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| VS Code bridge (JSON-RPC) | `bridge/bridgeMain.ts` (2999 LOC) | `4910.js` | ❌ | ❌ | M10 |
| JetBrains bridge | `utils/jetbrains.ts` | — | ❌ | ❌ | M10 |
| REPL bridge | `bridge/replBridge.ts` (2406 LOC) | `5155.js` | ❌ | ❌ | M10 |
| Remote bridge core | `bridge/remoteBridgeCore.ts` (1008 LOC) | — | ❌ | ❌ | M10 |
| Bridge messaging | `bridge/bridgeMessaging.ts` | — | ❌ | ❌ | M10 |
| Session runner | `bridge/sessionRunner.ts` | — | ❌ | ❌ | M10 |
| Bridge session creation | `bridge/createSession.ts` | — | ❌ | ❌ | M10 |
| Bridge API | `bridge/bridgeApi.ts` | — | ❌ | ❌ | M10 |
| Inbound attachments (IDE) | `bridge/inboundAttachments.ts` | — | ❌ | ❌ | M10 |
| IDE path conversion | `utils/idePathConversion.ts` | — | ❌ | ❌ | M10 |
| Bridge UI dialogs | `components/BridgeDialog.tsx` etc. | — | ❌ | ❌ | M10 |
| All 31 bridge/* files | `bridge/` (31 files, 12613 LOC) | — | ❌ | ❌ | M10 |

---

## 15. Remote & ULTRAPLAN — M10

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Remote session manager | `remote/RemoteSessionManager.ts` | — | ❌ | ❌ | M10 |
| Remote WebSocket sessions | `remote/SessionsWebSocket.ts` | — | ❌ | ❌ | M10 |
| Remote permission bridge | `remote/remotePermissionBridge.ts` | — | ❌ | ❌ | M10 |
| Upstream proxy relay | `upstreamproxy/relay.ts` | — | ❌ | ❌ | M10 |
| Teleport (session migration) | `utils/teleport.tsx` (1225 LOC) | — | ❌ | ❌ | M10 |
| RemoteTriggerTool | `tools/RemoteTriggerTool/` | — | ❌ | ❌ | M10 |
| Direct connect server | `server/directConnectManager.ts` | — | ❌ | ❌ | M10 |

---

## 16. Coordinator / Agent Swarms

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Coordinator mode | `coordinator/coordinatorMode.ts` (369 LOC) | — | `internal/agent/loop.go` (partial) | 🟡 | Parallel tools only, no full coordinator |
| TeamCreateTool | `tools/TeamCreateTool/` | — | ❌ | ❌ | |
| TeamDeleteTool | `tools/TeamDeleteTool/` | — | ❌ | ❌ | |
| SendMessageTool | `tools/SendMessageTool/` | — | ❌ | ❌ | |
| Teammate mailbox | `utils/teammateMailbox.ts` (1183 LOC) | — | ❌ | ❌ | |
| Team memory ops | `utils/teamMemoryOps.ts` | — | ❌ | ❌ | |
| Team discovery | `utils/teamDiscovery.ts` | — | ❌ | ❌ | |
| ULTRAPLAN | `services/ultraplan/` | — | ❌ | ❌ | |
| Agent listing delta | `utils/agentContext.ts` | — | ❌ | ❌ | |
| Coordinator agent status UI | `components/CoordinatorAgentStatus.tsx` | — | ❌ | ❌ | |

---

## 17. Attachments & File Paste — M13

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Image paste from clipboard | `utils/imagePaste.ts` (416 LOC) | — | ❌ | ❌ | M13 |
| Image resize | `utils/imageResizer.ts` (880 LOC) | — | ❌ | ❌ | M13 |
| Image storage | `utils/imageStore.ts` | — | ❌ | ❌ | M13 |
| PDF handling | `utils/pdf.ts` (300 LOC) | — | ❌ | ❌ | M13 |
| File drag-drop | `utils/attachments.ts` (3997 LOC) | — | ❌ | ❌ | M13 |
| ANSI to PNG | `utils/ansiToPng.ts` (334 LOC) | — | ❌ | ❌ | M13 |
| ANSI to SVG | `utils/ansiToSvg.ts` | — | ❌ | ❌ | M13 |
| Screenshot clipboard | `utils/screenshotClipboard.ts` | — | ❌ | ❌ | M13 |
| Asciinema recording | `utils/asciicast.ts` | — | ❌ | ❌ | M13 |
| @file mention parsing | `utils/attachments.ts` | — | ❌ | ❌ | M13 |
| IDE inbound attachments | `bridge/inboundAttachments.ts` | — | ❌ | ❌ | M13+bridge |

---

## 18. Buddy / Voice / KAIROS

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Companion generation (Mulberry32) | `buddy/companion.ts` | — | `internal/buddy/buddy.go` | ✅ | |
| 18 species + 5 rarities | `buddy/types.ts` | — | `internal/buddy/buddy.go` | ✅ | |
| ASCII sprite renderer | `buddy/sprites.ts` (514 LOC) | — | `internal/buddy/buddy.go` | 🟡 | Simplified sprites |
| Companion soul persistence | `buddy/companion.ts` | — | `internal/buddy/store.go` | ✅ | |
| /buddy command | `buddy/useBuddyNotification.tsx` | — | `internal/commands/buddy.go` | ✅ | |
| Companion speech bubble / animation | `buddy/CompanionSprite.tsx` (370 LOC) | — | ❌ | ❌ | No live animation |
| Companion intro injection | `buddy/prompt.ts` | — | ❌ | ❌ | |
| Buddy notification (rainbow teaser) | `buddy/useBuddyNotification.tsx` | — | ❌ | ❌ | |
| Voice recording (CoreAudio/ALSA) | `services/voice.ts` (525 LOC) | — | ❌ | ❌ | Requires cgo |
| Voice STT (WebSocket) | `services/voiceStreamSTT.ts` (544 LOC) | — | ❌ | ❌ | Requires cgo |
| Voice keyterms | `services/voiceKeyterms.ts` | — | ❌ | ❌ | |
| KAIROS (assistant mode) | `assistant/` | — | ❌ | ⬛ | GrowthBook-gated |

---

## 19. Output Styles & Undercover

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| Output style loader | `outputStyles/loadOutputStylesDir.ts` | — | `internal/outputstyles/outputstyles.go` | ✅ | |
| YAML frontmatter parsing | `utils/frontmatterParser.ts` | — | `internal/outputstyles/outputstyles.go` | ✅ | |
| Project + user dir merge | `outputStyles/loadOutputStylesDir.ts` | — | `internal/outputstyles/outputstyles.go` | ✅ | |
| Plugin output styles | `utils/plugins/loadPluginOutputStyles.ts` | — | ❌ | ❌ | |
| /output-style command | `commands/output-style/` | — | `internal/commands/outputstyle.go` | ✅ | |
| Undercover mode | `utils/undercover.ts` | — | `internal/undercover/undercover.go` | ✅ | |
| Undercover auto-detection | `utils/undercover.ts` | — | ❌ | ❌ | Always off unless env set |
| Undercover auto-notice | `utils/undercover.ts` | — | ❌ | ❌ | |

---

## 20. Analytics & Telemetry

| Feature | TS Source | Decoded Chunk(s) | Go (conduit) | Status | Notes |
|---------|-----------|-----------------|--------------|--------|-------|
| tengu_* event names | `services/analytics/` | — | ❌ | ⬛ | No-op intentional |
| Diagnostic tracking | `services/diagnosticTracking.ts` | — | ❌ | ⬛ | |
| Query profiler | `utils/queryProfiler.ts` | — | ❌ | ⬛ | |
| Heap dump service | `utils/heapDumpService.ts` | — | ❌ | ⬛ | |
| Startup profiler | `utils/startupProfiler.ts` | — | ❌ | ⬛ | |
| FPS tracker | `utils/fpsTracker.ts` | — | ❌ | ⬛ | |
| Stats collection | `utils/stats.ts` (1061 LOC) | — | ❌ | ⬛ | |

---

## 21. Utilities (Shared)

| Feature | TS Source | Go (conduit) | Status | Notes |
|---------|-----------|--------------|--------|-------|
| Git utilities | `utils/git.ts` (926 LOC) | ❌ | ❌ | Used by commit attribution |
| Git diff parsing | `utils/gitDiff.ts` | ❌ | ❌ | |
| Commit attribution | `utils/commitAttribution.ts` | ❌ | ❌ | Used by undercover auto-detect |
| Ripgrep integration | `utils/ripgrep.ts` (679 LOC) | `internal/tools/greptool/` | 🟡 | In greptool only |
| File I/O utilities | `utils/file.ts`, `fsOperations.ts` | ❌ | ❌ | Scattered in tools |
| File read cache | `utils/fileReadCache.ts` | ❌ | ❌ | |
| Shell command wrapper | `utils/ShellCommand.ts` | ❌ | ❌ | |
| Shell config | `utils/shellConfig.ts` | ❌ | ❌ | |
| HTTP proxy | `utils/proxy.ts` | `internal/api/retry.go` `NewClientWithProxy` | ✅ | HTTPS_PROXY / HTTP_PROXY env vars |
| Markdown utilities | `utils/markdown.ts` | ❌ | ❌ | |
| Memoization | `utils/memoize.ts` | ❌ | ❌ | sync.Once used ad-hoc |
| Cron scheduler | `utils/cronScheduler.ts` (565 LOC) | ❌ | ❌ | |
| Token counting | `utils/tokens.ts` | ❌ | ❌ | |
| Theme management | `utils/theme.ts` | ❌ | ❌ | |
| String utilities | `utils/stringUtils.ts` | ❌ | ❌ | |
| Word utilities | `utils/words.ts` | ❌ | ❌ | |
| Context analysis | `utils/analyzeContext.ts` | ❌ | ❌ | |
| CLAUDE.md loading | `utils/claudemd.ts` (1479 LOC) | `internal/claudemd/claudemd.go` | ✅ | Done in M-A |
| Auto-updater | `utils/autoUpdater.ts` | ❌ | ⬛ | |
| Platform detection | `utils/platform.ts` | `cmd/claude/util.go` | 🟡 | OS/arch only |
| Glob utilities | `utils/glob.ts` | `internal/tools/globtool/` | 🟡 | In tool only |

---

## 22. State Management

| Feature | TS Source | Go (conduit) | Status | Notes |
|---------|-----------|--------------|--------|-------|
| Redux-like app state | `state/` (6 files, 2380 LOC) | ❌ | 🔲 | Bubble Tea model replaces this |
| Bootstrap state | `bootstrap/state.ts` (1758 LOC) | ❌ | 🔲 | Handled in main.go |
| React contexts | `context/` (9 files) | ❌ | 🔲 | Bubble Tea model handles |

---

## Summary Scorecard

| Area | ✅ Complete | 🟡 Partial | ❌ Missing | ⬛ Descoped | Total |
|------|------------|-----------|-----------|------------|-------|
| Auth & OAuth | 9 | 0 | 6 | 3 | 18 |
| API Client & SSE | 9 | 1 | 2 | 0 | 12 |
| Agent Loop | 10 | 1 | 2 | 1 | 14 |
| Tools (framework) | 6 | 0 | 1 | 0 | 7 |
| Tools (individual, 40) | 32 | 0 | 3 | 5 | 40 |
| Permissions & Hooks | 16 | 0 | 2 | 1 | 19 |
| TUI & Rendering | 18 | 4 | 9 | 0 | 31 |
| Slash Commands | 41 | 1 | 6 | 12 | 60 |
| MCP Host | 9 | 0 | 4 | 0 | 13 |
| Plugins & Skills | 12 | 0 | 5 | 0 | 17 |
| Memory System | 8 | 0 | 5 | 3 | 16 |
| RTK | 13 | 0 | 2 | 0 | 15 |
| Session & History | 10 | 2 | 2 | 0 | 14 |
| Config & Settings | 8 | 0 | 7 | 3 | 18 |
| Bridge (M10) | 0 | 0 | 14 | 0 | 14 |
| Remote & ULTRAPLAN (M10) | 0 | 0 | 7 | 0 | 7 |
| Coordinator / Swarms | 0 | 1 | 8 | 0 | 9 |
| Attachments (M13) | 0 | 0 | 11 | 0 | 11 |
| Buddy / Voice / KAIROS | 5 | 1 | 5 | 2 | 13 |
| Output Styles & Undercover | 6 | 0 | 3 | 0 | 9 |
| Analytics & Telemetry | 0 | 0 | 0 | 7 | 7 |
| Utilities (shared) | 0 | 3 | 13 | 3 | 19 |
| State Management | 0 | 0 | 0 | 3 | 3 |
| **TOTAL** | **212** | **14** | **117** | **43** | **386** |

**Overall parity: 226/343 scoped features (66% complete, 4% partial)**
**Descoped: 43 features (intentionally excluded)**

---

## Milestone Map (Updated)

| Milestone | Features | Status |
|-----------|----------|--------|
| M1 — Auth + bare API call | Auth (9), API basics (5) | ✅ Done |
| M2 — Streaming + 5 core tools | SSE, BashTool, FileRead/Write/Edit, Grep, Glob | ✅ Done |
| M3 — TUI | Bubble Tea REPL, status bar, input, viewport | ✅ Done |
| M4 — All core tools | 24 tools implemented | ✅ Done |
| M5 — Permissions + hooks + commands | Permissions, all 4 hook types, 22 commands | ✅ Done |
| M6 — RTK in-process | 75 rules, SQLite tracking, /rtk gain | ✅ Done |
| M7 — MCP host | stdio/HTTP transports, tool proxy, config | ✅ Done |
| M8 — Plugins + Skills + memdir | Plugin ecosystem, skill tool, memory/dream | ✅ Done |
| M9 — Multi-agent | AgentTool, RunSubAgent, parallel tools | ✅ Done |
| M11 — Cosmetic parity | Buddy, output styles, undercover | ✅ Done |
| M-A — CLAUDE.md loading | Dir walk, @include, .claudeignore, session cache | ✅ Done |
| M-B — Agent/API gaps | Backoff, proxy, auto-compact, thinking budget, rate limits | ✅ Done |
| M-C — Missing tools | EnterPlanMode, ExitPlanMode, AskUser, Config, SyntheticOutput, worktree, MCP resources | ✅ Done |
| M-D — Missing slash commands | /status /tasks /agents /thinkback /color /copy /search /session /memory /context /effort /fast | ✅ Done |
| M-E — Hook completion | HTTP, prompt, agent hooks; async; desktop notifications | ✅ Done |
| M-F — Session completion | Cost persistence, file access, transcript search, title extraction | ✅ Done |
| M-G — Config completion | Env injection, XDG/Windows claudeDir(), ApplyEnv | ✅ Done |
| M-H — MCP completion | ListResources, ReadResource on all transports | ✅ Done |
| M-I — TUI polish | Full GFM: tables, headings, italic, strikethrough, task lists, blockquotes | ✅ Done |
| M-J — Worktree support | EnterWorktree, ExitWorktree, sanitizeSlug, IsInsideWorktree | ✅ Done |
| M-K — Rate limit display | anthropic-ratelimit-* parse, <20% warning, status bar badge | ✅ Done |
| M-L — Fast mode + effort | /fast ⚡, /effort low\|medium\|high\|max, ThinkingBudgets | ✅ Done |
| M-N — Memory completion | ScanMemories, RelevantMemories, /memory list\|show\|scan, age tracking | ✅ Done |
| **M10 — Bridge (IDE)** | 21 bridge features | ❌ Not started |
| **M12 — Hardening** | Conformance tests, benchmarks | ❌ Not started |
| **M13 — Attachments** | Image/PDF paste, drag-drop | ❌ Not started |

---

## Key Missing Features (Not in Any Milestone Yet)

These are implemented in Claude Code but not yet in conduit and not in M10/M13 (as of 2026-05-02):

1. **Vim mode** — vi keybindings in input box (`vim/`, 5 files). Medium value; large effort.
2. **Custom keybindings** — user-defined key mappings. Low value.
3. **Conversation recovery** — mid-turn error recovery (`utils/conversationRecovery.ts`). Medium value.
4. **API preconnect** — warm TCP connection to api.anthropic.com on startup. Low value.
5. **Token counting (accurate)** — cl100k token estimation before sending. Medium value.
6. **Onboarding flow** — first-run auth check + key command hints (`components/OnboardingComponent.tsx`).
7. **Plugin signature verification** — git commit sig check on install.
8. **Micro-compaction** — compact just the oldest turns, not the full context (`services/compact/microCompact.ts`).
9. **Session memory service** — inject recent session summaries on resume (`services/SessionMemory/`).
10. **Memory extraction** — auto-extract memorable facts after session end (`services/extractMemories/`).
11. **/passes, /extra-usage, /terminalSetup** — low-value CC-specific commands.

**Newly descoped (KAIROS/GrowthBook-gated — not in external builds):** BriefTool, ScheduleCronTool, RemoteTriggerTool (remote-only).

Previously listed as missing but now ✅ implemented (2026-05): CLAUDE.md loading, auto-compact, HTTP proxy, rate limit tracking, AskUserQuestion, EnterPlanMode/ExitPlanMode, MCP resources, effort/fast modes, /memory /context /status /tasks /session /agents /thinkback /color /copy /search /diff /doctor /files /review /usage /stats /theme /rename /pr-comments /tag, worktree tools, HTTP/prompt/agent hooks, XDG paths, cost persistence, transcript search, SyntheticOutputTool, Stats panel (asciigraph chart, per-model series, Overview heatmap), session activity tracking (idle reporting in /session), visual pickers for /theme /model /output-style.
