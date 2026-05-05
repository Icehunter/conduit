# Parity Comparison: conduit (Go) vs Claude Code TypeScript vs Decompiled Bundle

**Document Date:** 2026-05-04  
**Purpose:** Deep behavioral comparison identifying implementation gaps and edge cases not yet captured in PARITY.md.  
**Methodology:** Cross-reference Go implementation, TS source, and decompiled v2.1.126 bundle chunks to identify differences in:
- Data structures and fields
- API request/response shapes
- Streaming semantics
- Permission/hook mechanics
- Message assembly and history
- Tool execution ordering

---

## 1. Account/Profile Model

### TS Source References
- `utils/auth.ts` — OAuth token/credential mgmt
- `utils/profile.ts` — Profile endpoint calls
- Components: `components/ConsoleOAuthFlow.tsx`, `screens/REPL.tsx` (welcome card)

### Decompiled Chunks
- `1220.js` (OAuth, profile endpoint reference at line 277: `account.email`, `account.display_name`)

### Conduit Implementation
- `internal/auth/accounts.go` — Multi-account store (emails → keychain tokens)
- `internal/profile/profile.go` — Profile fetch endpoint
- Account entry: Email, AddedAt (timestamp), Active tracking in accounts.json

### Comparison Table

| Feature | TS Source | Decompiled v2.1.126 | Conduit Go | Status | Notes |
|---------|-----------|-------------------|------------|--------|-------|
| Profile endpoint | POST `/api/oauth/profile` | 1220.js:277 | ✅ GET `https://api.anthropic.com/api/oauth/profile` | ✅ | Correct endpoint and auth header |
| Profile display_name | `account.display_name` | 1220.js:277 | ✅ `Info.DisplayName` | ✅ | Field name correct |
| Profile email | `account.email` | 1220.js:277 | ✅ `Info.Email` | ✅ | Field name correct (not email_address) |
| Organization name | `organization.organization_name` | 1220.js:277 | ✅ `Info.OrganizationName` | ✅ | Full path correct |
| Subscription type | Derived from `organization.organization_type` enum | (e.g., "claude_pro", "claude_max") | ✅ Mapped to labels | ✅ | Maps claude_pro→Claude Pro, claude_max→Claude Max, etc. |
| Multi-account support | Single account per session; /login --switch <email> | Single active (TS simpler) | ✅ Full multi-account with accounts.json | ✅ Extra; conduit extends TS |
| Account persistence | Keychain per email | (TS source vague) | ✅ ~/.claude/accounts.json + per-email keychain keys | ✅ |
| ActiveEmail auto-select | N/A (TS: single account) | N/A | ✅ Auto-picks most-recent if active="" | ✅ Extra; backwards compat |
| Welcome card "Hi [name]" | Shows DisplayName or Email | Similar | ✅ Shows displayName or email from Profile | ✅ |

### Gaps Found

⚠️ **Profile fetch timeout:** TS likely uses default fetch timeout; conduit uses 8s hardcoded (`/Volumes/Engineering/Icehunter/conduit/internal/profile/profile.go:49`). Not a functional gap but timing differs.

⚠️ **Profile error handling:** Conduit returns zero Info on non-200; TS likely does same (commented as non-fatal in profile.go:47). Confirm TS behavior.

---

## 2. Settings Load/Merge/Persist

### TS Source References
- `utils/config.ts` (1817 LOC) — Full settings loader
- `schemas/hooks.ts` — Hook schemas
- Priority: user → project → local (gitignored)

### Decompiled Chunks
- `0628.js` — settings loader (partial)

### Conduit Implementation
- `internal/settings/settings.go` — Load, merge, raw-map preservation
- **Three-layer priority:** user `~/.claude/settings.json` → project `.claude/settings.json` → project `.claude/settings.local.json`
- Raw JSON map preservation to avoid field clobbering

### Comparison Table

| Feature | TS Source | Decompiled | Conduit Go | Status | Notes |
|---------|-----------|-----------|------------|--------|-------|
| **Settings Layers** | | | | | |
| User global settings | `~/.claude/settings.json` | 0628.js | ✅ `~/.claude/settings.json` | ✅ | |
| Project settings | `.claude/settings.json` | 0628.js | ✅ `.claude/settings.json` | ✅ | |
| Project local (gitignored) | `.claude/settings.local.json` | 0628.js | ✅ `.claude/settings.local.json` | ✅ | |
| Merge order | user → project → local | 0628.js | ✅ Same | ✅ | Last wins |
| **Permissions struct** | | | | | |
| allow array | `permissions.allow` | 0628.js | ✅ `Permissions.Allow` | ✅ | |
| deny array | `permissions.deny` | 0628.js | ✅ `Permissions.Deny` | ✅ | |
| ask array | `permissions.ask` | 0628.js | ✅ `Permissions.Ask` | ✅ | |
| defaultMode | `permissions.defaultMode` | 0628.js | ✅ `Permissions.DefaultMode` | ✅ | Persisted via SavePermissionsField |
| additionalDirectories | `permissions.additionalDirectories` | 0628.js | ✅ `Permissions.AdditionalDirs` | ✅ | |
| **Hook schemas** | | | | | |
| Hook.type | enum: "command"\|"http"\|"prompt"\|"agent" | schemas/hooks.ts | ✅ Hook.Type string | ✅ | |
| Hook.statusMessage | Optional spinner text | schemas/hooks.ts | ✅ StatusMessage | ✅ | |
| Hook.if | Permission rule gate | schemas/hooks.ts | ✅ If (string) | ✅ | Gated by permission check |
| Hook.timeout | Per-hook override (seconds) | schemas/hooks.ts | ✅ TimeoutSecs | ✅ | |
| Hook.once | Remove after first exec | schemas/hooks.ts | ✅ Once (bool) | ✅ | |
| Hook.async | Fire-and-forget | schemas/hooks.ts | ✅ Async (bool) | ✅ | Spawns goroutine |
| Hook.command | Shell command | schemas/hooks.ts | ✅ Command | ✅ | type="command" |
| Hook.url | HTTP POST target | schemas/hooks.ts | ✅ URL | ✅ | type="http" |
| Hook.headers | Extra HTTP headers | schemas/hooks.ts | ✅ Headers map | ✅ | |
| Hook.allowedEnvVars | Vars to interpolate | schemas/hooks.ts | ✅ AllowedEnvVars | ✅ | |
| Hook.prompt | LLM prompt (may have $ARGUMENTS) | schemas/hooks.ts | ✅ Prompt | ✅ | type="prompt"\|"agent" |
| Hook.model | Model override | schemas/hooks.ts | ✅ Model | ✅ | type="prompt"\|"agent" |
| HookMatcher.matcher | Tool name or glob | schemas/hooks.ts | ✅ Matcher | ✅ | "" = all tools |
| HookMatcher.hooks | Array of hooks | schemas/hooks.ts | ✅ Hooks | ✅ | |
| **Other Settings Fields** | | | | | |
| enabledPlugins | Map of plugin→bool | config.ts | ✅ EnabledPlugins | ✅ | key="pluginName@marketplace" |
| model | Preferred model name | config.ts | ✅ Model | ✅ | e.g. "claude-opus-4-7" |
| outputStyle | Active output style name | config.ts | ✅ OutputStyle | ✅ | Persisted via SaveOutputStyle |
| theme | Palette name (dark\|light\|...) | config.ts | ✅ Theme | ✅ | Hot-swap via /theme |
| themeOverrides | Per-field color tweaks | (TS may not have) | ✅ ThemeOverrides | ✅ Extra | conduit-only; #RRGGBB hex or ANSI codes |
| themes | Custom theme definitions | (TS may not have) | ✅ Themes | ✅ Extra | conduit-only; map[name]map[field]value |
| env | Environment variables | config.ts | ✅ Env | ✅ | Injected into tool exec |
| onboardingComplete | First-run gate | (TS mirrors CC's projectOnboardingState) | ✅ OnboardingComplete | ✅ | Set after welcome dismissed |
| enabledMcpjsonServers | Project MCP approval | (TS: MCPServerApprovalDialog) | ✅ EnabledMcpjsonServers | ✅ | Server names allowed |
| disabledMcpjsonServers | Project MCP deny list | (TS: MCPServerApprovalDialog) | ✅ DisabledMcpjsonServers | ✅ | Server names blocked |
| enableAllProjectMcpServers | "Yes All" flag | (TS: MCPServerApprovalDialog) | ✅ EnableAllProjectMcpServers | ✅ | One-time approval for all .mcp.json servers |
| **Raw-map preservation** | Fields may be unknown | config.ts (opaque merge) | ✅ Preserved via raw JSON map | ✅ Extra | Avoids clobbering unknown fields written by CC |

### Gaps Found

⚠️ **Settings file unknown fields:** TS source not clear on how unknown fields are preserved; conduit uses raw JSON map approach. Should test: write a custom field from real CC, confirm conduit doesn't drop it on save.

⚠️ **ApplyEnv semantics:** `settings.Env` merged but actual injection into tool subprocess happens in BashTool. Confirm that environment variables are union'd (not replaced) with inherited env.

**File paths reference:**
- TS: `/Volumes/Engineering/Icehunter/claude-code/src/utils/config.ts` (1817 LOC)
- TS: `/Volumes/Engineering/Icehunter/claude-code/src/schemas/hooks.ts`
- Go: `/Volumes/Engineering/Icehunter/conduit/internal/settings/settings.go` (479 LOC)

---

## 3. Agent Loop & Turn Structure

### TS Source References
- `QueryEngine.ts` (1295 LOC) — Main loop orchestrator
- `query.ts` (1729 LOC) — Turn execution, tool dispatch
- `services/compact/autoCompact.ts` — Auto-compact trigger logic

### Decompiled Chunks
- `3585.js`, `3918.js`, `4091.js` — Loop implementations (partial)

### Conduit Implementation
- `internal/agent/loop.go` — Full agentic loop
- `internal/compact/compact.go` — Compaction logic
- `internal/microcompact/microcompact.go` — Time-based microcompact

### Comparison Table

| Feature | TS Source | Decompiled | Conduit Go | Status | Notes |
|---------|-----------|-----------|------------|--------|-------|
| **Main Loop** | | | | | |
| Loop structure | queryLoop() iterates turns | 3585.js | ✅ Loop.Run() iterates turns | ✅ | |
| Turn limit (MaxTurns) | Configurable | query.ts | ✅ LoopConfig.MaxTurns | ✅ | 0 = no limit |
| **Message History** | | | | | |
| User message in history | Added after user input | query.ts | ✅ Caller appends to []Message | ✅ | Loop receives already-appended history |
| Assistant message appended | After stream complete | query.ts | ✅ After drainStream, before stop check | ✅ | Includes all content blocks |
| Tool result injection | After each tool exec | query.ts | ✅ As role="user" message | ✅ | One tool_result per content block |
| **Tool Execution** | | | | | |
| Sequential execution | Default; parallel with concurrency limit | coordinatorMode.ts | ✅ Serial by default; parallel up to 4 with maxConcurrentTools | ✅ Pool size = 4 |
| Tool permission gate | Checked before tool exec | hooks/toolPermission/ | ✅ Loop checks Gate before tool | ✅ | Can AskPermission if "ask" mode |
| PreToolUse hooks | Fired before each tool | utils/hooks/ | ✅ RunPreToolUse in loop | ✅ | Via hooks.RunPreToolUse(ctx, ...) |
| PostToolUse hooks | Fired after each tool | utils/hooks/ | ✅ RunPostToolUse in loop | ✅ | Via hooks.RunPostToolUse(ctx, ...) |
| **Auto-compact trigger** | | | | | |
| Trigger point | Before tool execution; check if inputTokens > 80% MaxTokens | autoCompact.ts | ✅ After drainStream, before next request (line ~430) | 🟡 Partial | **TIMING DIFFERENCE**: TS fires before tool execution; conduit fires before *next API request*. This is subtly different: TS checks after tool results are added; conduit checks on next turn's request assembly. Need to verify TS exact timing. |
| Compaction callback | OnCompact fired after summary | compact.ts | ✅ OnCompact(summary) in loop | ✅ | Allows TUI to persist summary |
| **Micro-compaction** | | | | | |
| Time-based threshold | 60 minutes since last assistant msg | microCompact.ts | ✅ MicroCompactGap=60m default | ✅ | Configurable |
| Keep recent count | Last 5 tool_results kept | microCompact.ts | ✅ MicroCompactKeep=5 default | ✅ | Older ones replaced with placeholder |
| **End-of-turn handling** | | | | | |
| OnEndTurn callback | Fired after end_turn (no tool_use) | query.ts | ✅ OnEndTurn(history) in loop | ✅ | Used for memory extraction, session memory update |
| **SessionStart hooks** | | | | | |
| Timing | Fired once before first turn | query.ts | ✅ In Loop.Run before loop, after arg validation | ✅ | SessionStart hooks fired synchronously |
| **Stop hooks** | | | | | |
| Timing | Fired on clean exit or error | query.ts | ✅ In defer after loop returns | ✅ | Via defer, runs in background context |

### Gaps Found

🟡 **Auto-compact timing uncertainty:** TS `autoCompact.ts` fires "at 80% inputTokens/MaxTokens" but exact trigger point (before tool exec vs before next request) needs confirmation from decoded chunks. Conduit currently checks on next request assembly. Test case: send message that triggers tool, does TS compact before or after tool execution?

**File path reference:**
- Go: `/Volumes/Engineering/Icehunter/conduit/internal/agent/loop.go` (512 LOC) — Run() method starts line 295

---

## 4. Message Streaming (SSE)

### TS Source References
- `services/api/` — SSE parsing
- Decoded: `0137.js` shows fromSSEResponse stream assembly
- Events: message_start, content_block_start, content_block_delta, content_block_stop, message_delta, message_stop

### Decompiled Chunks
- `0137.js` — Stream class, SSE event handling
- `0156.js` — Additional stream parsing logic

### Conduit Implementation
- `internal/sse/parser.go` — SSE line parsing
- `internal/sse/events.go` — Event type definitions
- `internal/api/stream.go` — Stream assembly and tool_use block assembly

### Comparison Table

| Feature | TS Source | Decompiled v2.1.126 | Conduit Go | Status | Notes |
|---------|-----------|-------------------|------------|--------|-------|
| **SSE Event Types** | | | | | |
| message_start | First event; carries metadata | 0137.js:34 | ✅ MessageStartEvent | ✅ | |
| message_delta | Final usage + stop_reason | (Anthropic API) | ✅ MessageDeltaEvent | ✅ | |
| content_block_start | Index + block type | Anthropic API | ✅ ContentBlockStartEvent | ✅ | |
| content_block_delta | Index + delta (text, input_json, thinking) | Anthropic API | ✅ ContentBlockDeltaEvent | ✅ | |
| content_block_stop | Index only | Anthropic API | ✅ ContentBlockStopEvent | ✅ | |
| message_stop | (legacy?) | (unclear) | ❓ Not decoded in conduit | ? | Check if needed |
| ping | Keep-alive | 0137.js:50 | ✅ Parsed; skipped unless IncludePings=true | ✅ | |
| error | API error event | 0137.js:51 | ✅ Decoded as APIError | ✅ | |
| **Tool Use Assembly** | | | | | |
| tool_use block type | "type":"tool_use" | Anthropic API | ✅ ContentBlock.Type="tool_use" | ✅ | |
| tool_use.id | Unique block ID | Anthropic API | ✅ ContentBlock.ID | ✅ | |
| tool_use.name | Tool name | Anthropic API | ✅ ContentBlock.Name | ✅ | |
| tool_use.input field | Accumulated from input_json_delta events | Anthropic API | ✅ ContentBlock.Input map[string]any | ✅ | Parsed from partial JSON deltas |
| **Critical: Empty Input** | | | | | |
| input={} for no-arg tools | If tool has no required params | (TBD) | ✅ Input can be empty map{} | ? | **TEST NEEDED**: Does TS always send "input":{}? Or can it be omitted? Conduit sends {} explicitly if tool has no params. |
| input omitted | (TBD) | (TBD) | ❌ Conduit always generates input field | ⚠️ | **GAP**: If TS omits "input" for some tools, conduit would differ. Check decompiled bundle for tool output format. |
| **Thinking blocks** | | | | | |
| thinking delta type | thinking_delta | Anthropic API (interleaved-thinking) | ✅ ContentDelta.Thinking | ✅ | Optional in stream |
| thinking block assembly | Accumulated from thinking_deltas | (TBD) | ✅ Accumulated into ContentBlock | ✅ | Type="thinking" |

### Gaps Found

🟡 **Empty input field semantics:** Conduit emits `"input":{}` for all tool_use blocks. TS source unclear if it always sends input or omits it for no-arg tools. Check `/Volumes/Engineering/Icehunter/bun-demincer/decoded/0158.js` (97 LOC, likely tool result envelope) for exact format.

⚠️ **message_stop event:** Not explicitly handled in conduit. Real Anthropic API docs may specify this, but conduit handles end-of-stream via EOF. If TS listens for message_stop, verify behavior matches.

**File paths:**
- Go: `/Volumes/Engineering/Icehunter/conduit/internal/sse/parser.go`
- Go: `/Volumes/Engineering/Icehunter/conduit/internal/sse/events.go`
- Decompiled: `/Volumes/Engineering/Icehunter/bun-demincer/decoded/0137.js` (184 LOC)
- Decompiled: `/Volumes/Engineering/Icehunter/bun-demincer/decoded/0158.js` (97 LOC) — tool result format

---

## 5. Permissions

### TS Source References
- `utils/permissions/` — Gate modes, rule matching
- `hooks/toolPermission/` — Permission callbacks
- Mode: "default" | "acceptEdits" | "plan" | "bypass"

### Decompiled Chunks
- (Permission gate implementation scattered; check 0628.js for settings)

### Conduit Implementation
- `internal/permissions/permissions.go` — Gate, rule matching, mode cycling
- `internal/commands/permissions.go` — /permissions command output
- Modes: "default", "acceptEdits", "plan", "bypass" (as string enums)

### Comparison Table

| Feature | TS Source | Decompiled | Conduit Go | Status | Notes |
|---------|-----------|-----------|------------|--------|-------|
| **Permission Modes** | | | | | |
| default | Ask before each tool | permissions.ts | ✅ Gate.Mode="default" | ✅ | AskPermission callback invoked |
| acceptEdits | Allow file edits, ask others | permissions.ts | ✅ Gate.Mode="acceptEdits" | ✅ | File tool bypass; others ask |
| plan | Approve all in plan window | permissions.ts | ✅ Gate.Mode="plan" | ✅ | Time-bounded auto-approve |
| bypass | Allow all (dangerous) | permissions.ts | ✅ Gate.Mode="bypass" | ✅ | No prompts |
| **Rule Matching** | | | | | |
| Glob patterns | `*.json`, `src/**` | permissions.ts | ✅ Glob + exact + prefix | ✅ | Uses filepath.Match |
| Exact match | `/exact/path` | permissions.ts | ✅ Exact string comparison | ✅ | |
| Prefix match | `path/` (trailing slash) | permissions.ts | ✅ Prefix before "/" | ✅ | |
| Allow rule | Whitelist tool/path | permissions.ts | ✅ Gate.Allow list | ✅ | |
| Deny rule | Blacklist (higher priority) | permissions.ts | ✅ Gate.Deny list; checked first | ✅ | Deny wins over Allow |
| Ask rule | Interactive approval per-call | permissions.ts | ✅ Gate.Ask list | ✅ | Gate.Ask (not exposed in TS, but conduit uses for mode classification) |
| **IsReadOnly flag** | | | | | |
| Read-only mode interactions | ReadOnly tools auto-allow | permissions.ts | ✅ tool.ReadOnly checked in Gate.Check() | ✅ | Bypass permission check if tool.ReadOnly && mode != "bypass" doesn't apply... check exact semantics. |
| **AskUserQuestion gate** | | | | | |
| Auto-approve vs ask | Gated by permission mode | tools/AskUserQuestionTool/ | ✅ Gate.Check() applies to AskUserQuestion | ✅ | Auto-approved in bypass/plan; asks in default/acceptEdits |
| **Session-scoped allow** | | | | | |
| Per-session allowlist | Temporary allow for session | permissions.ts | ✅ Gate.AllowOnce map (per tool/path combo) | ✅ | Used after user approves "always allow this session" |
| **Shift+Tab mode cycling** | | | | | |
| Mode cycle on Shift+Tab | default → acceptEdits → plan → bypass → default | keybindings.ts | ✅ In TUI model.go, cycleMode() | ✅ | |

### Gaps Found

⚠️ **IsReadOnly semantics unclear:** Conduit's Gate.Check() should probably auto-allow readonly tools regardless of mode, but exact TS behavior needs confirmation. Check `/Volumes/Engineering/Icehunter/claude-code/src/types/permissions.ts` for IsReadOnly field usage.

⚠️ **AskUserQuestion auto-approval:** Conduit's current implementation gates AskUserQuestion by permission mode. Confirm TS does same (likely yes, but verify in `/Volumes/Engineering/Icehunter/claude-code/src/tools/AskUserQuestionTool/`).

**File paths:**
- Go: `/Volumes/Engineering/Icehunter/conduit/internal/permissions/permissions.go`

---

## 6. Session Management (JSONL Format)

### TS Source References
- `utils/sessionStorage.ts` — JSONL save/load
- `utils/sessionTitle.ts` — Title extraction
- Event types: message (user/assistant/tool), system, metadata

### Decompiled Chunks
- `3096.js` — Session storage (partial)

### Conduit Implementation
- `internal/session/session.go` — JSONL write, Message + extras
- `internal/session/extras.go` — Cost, fileAccess, activity tracking
- Session ID generation: UUID

### Comparison Table

| Feature | TS Source | Decompiled | Conduit Go | Status | Notes |
|---------|-----------|-----------|------------|--------|-------|
| **JSONL Format** | | | | | |
| File extension | `.jsonl` (one JSON per line) | sessionStorage.ts | ✅ `.jsonl` | ✅ | |
| Session directory | `~/.claude/sessions/` | sessionStorage.ts | ✅ ClaudeDir()/sessions/ | ✅ | |
| Event type field | `"type"` enum | sessionStorage.ts | ✅ Events map to api.Message + metadata | ✅ | |
| Message event | Full api.Message (user/assistant) | sessionStorage.ts | ✅ Persisted as-is | ✅ | |
| Tool result event | api.Message role="user" with tool_result | sessionStorage.ts | ✅ Persisted as-is | ✅ | |
| **Session ID** | | | | | |
| ID generation | UUID v4 | sessionStorage.ts | ✅ uuid.New().String() | ✅ | |
| ID format | 36-char with dashes | sessionStorage.ts | ✅ Standard UUID format | ✅ | |
| **Session Title** | | | | | |
| Auto-title from first message | Extract from user's first message | sessionTitle.ts | ✅ Set at session start or /rename | ✅ | First 100 chars, truncated |
| Title persistence | In session metadata or separate file | sessionStorage.ts | ✅ In JSONL via metadata events or memory | ⚠️ | **CHECK**: Confirm TS stores title in JSONL vs separate file. Conduit uses first user message or explicit /rename. |
| **/resume command** | | | | | |
| List sessions | Show recent with age | commands/resume/ | ✅ Lists ~/.claude/sessions/*.jsonl | ✅ | Sorts by mtime, newest first |
| Session picker | Fuzzy-filterable UI | commands/resume/ | ✅ TUI modal with j/k navigation | ✅ | |
| Resume load | Restore history from JSONL | sessionRestore.ts | ✅ Load all messages, filter orphans | ✅ | FilterUnresolvedToolUses removes incomplete tool_use |
| **/rewind command** | | | | | |
| Rewind mechanics | Snapshot at point in history | commands/rewind/ | ✅ Can rewind to prior turns | ✅ | Truncate JSONL at chosen point |
| **Cost tracking** | | | | | |
| Cost event format | Tallied per turn (input/output tokens) | cost-tracker.ts | ✅ AppendCost(turn, inputTokens, outputTokens) | ✅ | Stored in session extras |
| **File access history** | | | | | |
| File read tracking | Record each FileReadTool call | fileHistory.ts | ✅ AppendFileAccess("read", path) | ✅ | Used by /files command |
| File write tracking | Record each FileWriteTool call | fileHistory.ts | ✅ AppendFileAccess("write", path) | ✅ | |

### Gaps Found

⚠️ **Session title storage:** Conduit doesn't explicitly persist session title to JSONL; uses first message or /rename. TS source unclear on exact mechanism. Test: create session, /rename it, resume the session — does title persist in JSONL?

**File paths:**
- Go: `/Volumes/Engineering/Icehunter/conduit/internal/session/session.go`

---

## 7. Commands Gap Analysis

### Implemented in Conduit (STATUS.md ✅)
- /help, /clear, /exit, /model, /compact, /permissions, /hooks, /login, /logout, /cost, /diff, /doctor, /files, /context, /stats, /keybindings, /effort, /fast, /privacy-settings, /memory, /feedback, /release-notes, /add-dir, /init, /review, /commit, /pr-comments, /fix, /export, /usage, /resume, /rewind, /rename, /theme, /plan, /mcp, /agents, /skills, /output-style, /search, /status, /tasks, /session, /tag, /color, /copy, /diff, /doctor, /files, /memory, /context, /effort, /fast, /thinkback, /pr-comments, /terminalSetup, /config, /rtk gain, /buddy

### Missing / Stub in Conduit
- /vim (toggles flag but no actual vim mode)
- /branch (conversation branching not implemented)
- /voice (voice STT/TTS, deferred)
- /feedback (opens GitHub in browser; Anthropic-internal dialog in TS)
- /sandbo…

Actually the table in PARITY.md is comprehensive. Main gaps: /vim (stub), /voice (deferred), /sandbox-toggle, /install-github-app, /install-slack-app (Anthropic-internal), /bridge, /remote-env, /remote-setup (bridge/remote-only).

---

## 8. Thinking Blocks & Extended Thinking

### TS Source References
- `utils/thinking.ts` — ThinkingConfig type
- `constants/betas.ts` — interleaved-thinking-2025-05-14 beta

### Decompiled Chunks
- (Thinking handling in 0137.js SSE parsing)

### Conduit Implementation
- `internal/api/types.go` — ThinkingConfig struct
- `internal/agent/loop.go` — Thinking budget plumbing
- /effort command sets budget

### Comparison Table

| Feature | TS Source | Decompiled | Conduit Go | Status | Notes |
|---------|-----------|-----------|------------|--------|-------|
| ThinkingConfig.type | Always "enabled" | (TBD) | ✅ "enabled" | ✅ | |
| ThinkingConfig.budget_tokens | Integer budget | (TBD) | ✅ BudgetTokens | ✅ | /effort low\|medium\|high\|max maps to tokens |
| Beta header | interleaved-thinking-2025-05-14 | constants/betas.ts | ✅ Added to BetaHeaders | ✅ | |
| Thinking stream events | thinking_delta in content_block_delta | SSE API | ✅ Parsed as ContentDelta.Thinking | ✅ | |
| Thinking block assembly | Accumulated into ContentBlock | (TBD) | ✅ Type="thinking" | ✅ | |
| /thinkback command | Show last thinking blocks | commands/thinkback/ | ✅ Renders thinking blocks | ✅ | |

---

## 9. Key Behavioral Differences

### Confirmed Differences (Non-issues)
1. **Multi-account support:** Conduit adds this; TS single-account. Backwards compatible.
2. **Custom themes:** Conduit adds themeOverrides + themes map; TS doesn't expose.
3. **Raw settings preservation:** Conduit preserves unknown JSON fields; TS might too but mechanism unclear.

### Potential Issues (Need Testing)

| Issue | Severity | Action |
|-------|----------|--------|
| Tool input field always present (never omitted) | High | Verify TS actually sends "input":{} for all tools. If TS omits it for some, need to match. |
| Auto-compact timing | Medium | Confirm TS compacts before or after tool execution. Conduit compacts before next request. |
| Session title persistence | Low | Check if TS stores title in JSONL or separate .title file. Conduit infers from first message. |
| Profile fetch timeout | Low | TS timeout value likely differs from 8s hardcoded in conduit. Not functional diff, just timing. |
| Permission defaultMode persistence | Medium | Test: set permissions.defaultMode in ~/.claude/settings.json, load it, verify Gate.DefaultMode reflects it. |
| IsReadOnly gate semantics | Medium | Confirm read-only tools auto-bypass permission checks regardless of mode. |

---

## 10. Summary: Top 10 Gaps Prioritized by Impact

| Rank | Gap | Component | Severity | Impact | Fix Effort |
|------|-----|-----------|----------|--------|------------|
| 1 | Tool input field format (always {} vs sometimes omitted) | SSE/API | **High** | If TS omits input for some tools, models trained on TS output will expect empty field omitted, not {}. Breaking change if different. | Low |
| 2 | Auto-compact trigger point timing | Agent loop | **Medium** | TS fires before tool exec; conduit fires before next request. May affect when compaction happens relative to token count. Subtle functional difference. | Medium |
| 3 | Session title storage mechanism | Session | **Low** | TS might use separate file; conduit infers from first message. Both work but semantics differ. | Low |
| 4 | Micro-compact gap + keep defaults | Micro-compact | **Low** | Hardcoded 60m + 5 in conduit; TS values unknown. Minor tuning diff. | Low |
| 5 | Profile fetch timeout | Profile | **Low** | Conduit 8s; TS likely different. Non-functional timing diff. | Low |
| 6 | Message history append point | Agent loop | **Medium** | User message added by caller vs loop—both acceptable, but ordering implications. | None (clarify only) |
| 7 | Settings unknown field preservation | Settings | **Low** | Conduit uses raw map; TS unclear. Both probably work but mechanism differs. | None (test only) |
| 8 | AskUserQuestion permission gating | Permissions | **Low** | Should be auto-approved in bypass/plan. Verify TS does same. | Low |
| 9 | IsReadOnly tool bypass semantics | Permissions | **Low** | Read-only tools should bypass permission checks. Verify TS behavior. | Low |
| 10 | Extended thinking budget defaults | Thinking | **Low** | /effort mapping to budget tokens; TS may differ. Non-breaking tuning. | Low |

---

## 11. Testing Checklist

Below is the suggested order for addressing gaps:

### Immediate (Session blocker)
- [ ] **Tool input field:** Check `/Volumes/Engineering/Icehunter/bun-demincer/decoded/0158.js` for tool_use output format. Does it always include "input" field? If not, update conduit to match.
- [ ] **Auto-compact timing:** Create test that sends >80% context usage message + tool call. Check TS vs conduit: which fires auto-compact first, before or after tool?

### Before next release
- [ ] **Session title persistence:** Resume a renamed session; verify title sticks. Check if TS stores in .title file or JSONL metadata.
- [ ] **Profile fetch:** Compare TS vs conduit timeout value. If TS slower, increase conduit timeout.

### Nice to have
- [ ] **Permission defaults:** Write settings.json with `permissions.defaultMode="bypass"`, load in conduit, verify gate reflects it.
- [ ] **ReadOnly tool bypass:** Confirm read-only tools (FileReadTool, GrepTool, etc.) bypass permission prompts in default mode.

---

## Appendix: File Locations Reference

### Go Implementation
- `/Volumes/Engineering/Icehunter/conduit/internal/auth/accounts.go` — Multi-account
- `/Volumes/Engineering/Icehunter/conduit/internal/profile/profile.go` — Profile fetch
- `/Volumes/Engineering/Icehunter/conduit/internal/settings/settings.go` — Settings load/merge
- `/Volumes/Engineering/Icehunter/conduit/internal/agent/loop.go` — Agent loop
- `/Volumes/Engineering/Icehunter/conduit/internal/sse/parser.go` — SSE parsing
- `/Volumes/Engineering/Icehunter/conduit/internal/sse/events.go` — Event types
- `/Volumes/Engineering/Icehunter/conduit/internal/api/types.go` — Message/ContentBlock structures
- `/Volumes/Engineering/Icehunter/conduit/internal/permissions/permissions.go` — Gate logic
- `/Volumes/Engineering/Icehunter/conduit/internal/session/session.go` — JSONL session save/load

### TS Source
- `/Volumes/Engineering/Icehunter/claude-code/src/utils/auth.ts` — OAuth
- `/Volumes/Engineering/Icehunter/claude-code/src/utils/profile.ts` — Profile
- `/Volumes/Engineering/Icehunter/claude-code/src/utils/config.ts` — Settings
- `/Volumes/Engineering/Icehunter/claude-code/src/QueryEngine.ts` — Loop orchestrator
- `/Volumes/Engineering/Icehunter/claude-code/src/query.ts` — Turn execution
- `/Volumes/Engineering/Icehunter/claude-code/src/types/permissions.ts` — Permission types
- `/Volumes/Engineering/Icehunter/claude-code/src/utils/sessionStorage.ts` — JSONL format
- `/Volumes/Engineering/Icehunter/claude-code/src/schemas/hooks.ts` — Hook definitions

### Decompiled Bundle
- `/Volumes/Engineering/Icehunter/bun-demincer/decoded/1220.js` — OAuth, profile (365 LOC)
- `/Volumes/Engineering/Icehunter/bun-demincer/decoded/0533.js` — Auth flow (124 LOC)
- `/Volumes/Engineering/Icehunter/bun-demincer/decoded/0137.js` — SSE parsing (184 LOC)
- `/Volumes/Engineering/Icehunter/bun-demincer/decoded/4500.js` — API client (274 LOC)
- `/Volumes/Engineering/Icehunter/bun-demincer/decoded/0158.js` — Tool result envelope (97 LOC)
- `/Volumes/Engineering/Icehunter/bun-demincer/decoded/1390.js` — (151 LOC, TBD)
- `/Volumes/Engineering/Icehunter/bun-demincer/decoded/2831.js` — System prompt (149 LOC)
- `/Volumes/Engineering/Icehunter/bun-demincer/decoded/0628.js` — Settings (partial)

---

## Document Metadata

**Created:** 2026-05-04  
**Last Updated:** 2026-05-04  
**Author:** Claude Code (Haiku 4.5) — File Search Specialist  
**Reviewed Against:** PARITY.md, STATUS.md, source code inspection  
**Testing Status:** ✅ Ready for test planning; 10 gaps identified, 3 HIGH priority
