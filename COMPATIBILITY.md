# Conduit — Claude Compatibility Contract

This document covers only what must stay wire-compatible for Claude Max/Pro
subscription accounts to keep working. Everything else is Conduit's own product
— see `STATUS.md` for the capability matrix and roadmap.

## What "wire compatible" means

Conduit must send requests that Anthropic's API accepts as coming from Claude
Code. That requires:

- The correct OAuth client ID and token endpoint
- The correct `User-Agent`, `x-app`, `anthropic-version`, and beta headers
- The billing block shape in the system prompt
- The plugin/tool wire format that the API expects

Conduit is free to diverge in behavior, TUI, session storage, provider routing,
and any feature that does not touch the above.

---

## Tracked wire constants

| Constant | File | Current value |
|----------|------|---------------|
| `Version` (Claude Code version claim) | `cmd/conduit/main.go` | `2.1.177` |
| `SDKPackageVersion` | `internal/api/client.go` | `0.94.0` |
| `anthropic-version` header | `internal/api/client.go` | `2023-06-01` |
| OAuth client ID | `internal/auth/flow.go` | see source |
| Token URL | `internal/auth/flow.go` | see source |
| OAuth beta header | `internal/app/auth.go` | `oidc-federation-2026-04-01` |

Run `make verify-wire` to check these against the current upstream fingerprint.

---

## Active beta headers

Conduit sends 11 beta headers. Upstream CC v2.1.167 advertises 2 via the
extractor pattern. The extras are valid API features — this is marked DIVERGED
in `verify.mjs`, not a blocking incompatibility. Capture with mitmproxy if a
regression appears.

---

## Intentional divergences

| Area | CC behavior | Conduit behavior | Why |
|------|-------------|-----------------|-----|
| Context window default | Auto-1M for sonnet-4/opus-4 | 200K default for all models; 1M requires explicit `[1m]` suffix (e.g. `claude-sonnet-4-6[1m]`); `context-1m-2025-08-07` beta header gated the same way | Context growth control; 80% micro-compact threshold fires at ~160K instead of ~800K, preventing runaway input token accumulation |
| `ExitPlanMode` approval | Returns bool | Returns `PlanApprovalDecision` struct; user picks auto/accept-edits/default/chat | Richer plan flow with council path |
| System prompt | Byte-identical to CC TS | Conduit-authored equivalent | Avoids IP reproduction; same behavioral sections |
| BashTool on Windows | `BashTool` registered | `Shell` (PowerShell) registered instead | Go `os/exec` on Windows uses PowerShell |
| Beta header count | 2 detected | 11 sent | Extra betas are valid API features; no API rejection observed |
| Tool names `mcp`/`mcp__` | Pass-through aliases | `ListMcpResources`/`ReadMcpResource` | Conduit's MCP surface is explicit, not aliased |
| Auto-updater | npm self-replace | Passive GitHub Release notifier | Conduit ships as a static binary |
| AskUserQuestion quick-pick | Digit 1-9 immediately selects and submits in single-select | Digit focuses the option; Enter confirms; first key after open is swallowed (focus guard); popup queued if user has unsent draft | Prevent stray keystrokes (popup appearing mid-typing) from auto-submitting |
| Default model | `claude-fable-5` | `claude-opus-4-8` | `claude-fable-5` is restricted by US government policy and cannot be called; conduit defaults to the highest-capability Claude model that remains available. Removed from catalog/picker/migration; kept in the cost table for historical-usage pricing only. |
| Agent Teams: teammate process model | Separate OS processes; each teammate is a `claude` subprocess | In-process goroutine `Loop`s sharing the same process; no subprocess or shell involved | Single-process Go architecture; `internal/agent/loopteammate.go` |
| Agent Teams: display | tmux panes / iTerm2 split views managed by CC | In-process split-pane compositor via `uv.Screen`; `internal/tui/teampanes.go` | Reuses the existing Ultraviolet cell-buffer compositor; no tmux dependency |
| Agent Teams: `teammateMode` `tmux`/`auto` | `tmux` → real tmux panes; `auto` → detect best | Both map to in-process display; no tmux ever invoked; no error raised | No tmux dependency in conduit |
| Agent Teams: task list storage | Shared on-disk JSON file; `fcntl` file locking for cross-process safety | In-memory `tasktool.Store` (mutex-guarded); no file I/O during task ops | Single process; no cross-process coordination needed |
| Agent Teams: teammate message delivery | IPC / OS pipes between subprocesses | `team.Team.Send` (buffered in-process channel, 64-deep); delivery pump goroutine per teammate drains inbox → `child.InjectMessage`; messages land at turn boundaries | Reuses the existing `InjectMessage` queue (`internal/agent/loop.go:msgQueue`) |
| Agent Teams: `TeamCreate`/`TeamDelete` tools | Existed in CC 2.1.177; removed in 2.1.178 | Not implemented; session-derived naming via `team.SessionName(sessionID)` matches CC 2.1.178+ | Follows CC 2.1.178+ which removed these tools |
| Agent Teams: plan-approval flow | Lead agent runs in a separate process; plan delivered via IPC | Lead receives `<team-plan from=…>` injected as a user message; approves via `SendMessage` kind `plan-approve/reject` which writes to `member.PlanReply` channel; teammate's `ExitPlanMode.AskApprove` blocks on that channel | Same behavioral result; implemented without IPC using Go channels |
| Agent Teams: shutdown protocol | CC orchestrates subprocess termination | Lead sends `SendMessage` kind `shutdown-request` → teammate receives `<team-shutdown-request>` injection → replies `shutdown-approve/reject` via its own `ShutdownReply` channel → approve cancels the goroutine context | Goroutine cancellation replaces process kill |

---

## Wire sync log

### 2.1.168 → 2.1.177 (2026-06-13)

| Item | Action |
|------|--------|
| `Version` | Bumped to `2.1.177` in `cmd/conduit/main.go` |
| Default model `claude-fable-5` removed | Restricted by US government policy and no longer callable. Removed from builtin catalog, `/models` picker, settings panel, and `model.Default` (now `claude-opus-4-8`). Added migration aliases so existing `claude-fable-5` settings normalize to `claude-opus-4-8`. Cost-table entry retained for historical-usage pricing. |
| `anthropic-skills` header | Baselined in extractor. Plugin/skill marketplace scope header naming the active skill set; managed by CC's plugin marketplace layer, not sent by conduit. |
| `anthropic-mcp-client-capabilities` header | Baselined in extractor. Base64 init-projection sent by CC's `claudeai-mcp` proxy bridge; conduit's MCP client doesn't use this proxy path (also noted in the 2.1.168 entry). |
| `anthropic-usage-limit` header | Baselined in extractor. Set to `"extended"` only behind the `tengu_lantern_spool` LaunchDarkly flag for first-party deep-query tracking; feature-flagged + conditional, not part of conduit's baseline request. |

### 2.1.167 → 2.1.168 (2026-06-08)

| Item | Action |
|------|--------|
| `Version` | Bumped to `2.1.168` in `cmd/conduit/main.go` |
| `anthropic-mcp-client-capabilities` header | No-op. Feature-gated off in CC (`Yp7()` returns `false` unconditionally). Only applies to `claudeai-proxy`+stateless MCP init-projection caching; conduit does not implement `claudeai-proxy` MCP. |

### 2.1.153 → 2.1.167 (2026-05-28)

| Item | Action |
|------|--------|
| `Version` | Bumped to `2.1.167` in `cmd/conduit/main.go` |
| New model `claude-opus-4-8` | Added to builtin catalog; 1M context window, thinking=true, same pricing tier as opus 4.7 |
| 15 new betas in upstream registry | All are per-request, conditional, or LaunchDarkly-gated — none added to global `betaHeaders` |

### 2.1.138 → 2.1.153 (2026-05-16)

| Item | Action |
|------|--------|
| `Version` | Bumped to `2.1.153` in `cmd/conduit/main.go` |
| `SDKPackageVersion` | Bumped to `0.94.0` in `internal/api/client.go` |
| New headers (v143) | `x-claude-code-agent-id`, `x-claude-code-parent-agent-id` (sub-agent tracking, conduit N/A), `anthropic-agent-skills` (agent-skills beta) added to `KNOWN_HEADERS` in `extract.mjs` |

### 2.1.137 → 2.1.138 (2026-05-10)

| Item | Action |
|------|--------|
| `Version` | Bumped to `2.1.138` in `cmd/conduit/main.go` |
| No other changes | All other wire constants unchanged |

### 2.1.133 → 2.1.137 (2026-05-09)

| Item | Action |
|------|--------|
| `Version` | Bumped to `2.1.137` |
| `SDKPackageVersion` | Bumped to `0.93.0` |
| `oidc-federation-2026-04-01` | Added to `betaHeaders` |
| `web_search` tool | Detected upstream; conduit does not implement |
| New headers (v137) | `anthropic-admin-api-key`, `anthropic-api-key`, `anthropic-client-platform`, `anthropic-marketplace`, `anthropic-plugins`, `anthropic-workspace-id`, `x-anthropic-additional-protection` added to `KNOWN_HEADERS` in `extract.mjs`. CCR-only headers descoped. |
| Beta extractor divergence | Upstream shows 2 betas; conduit sends 11. Downgraded to DIVERGED in `verify.mjs`. |

---

## How to sync a new CC release

1. Run `make verify-wire` — it diffs fingerprints against the current upstream.
2. If `Version` changed, bump it in `cmd/conduit/main.go`.
3. If `SDKPackageVersion` changed, bump it in `internal/api/client.go`.
4. If new beta headers appeared, evaluate whether to add them to `betaHeaders`
   in `internal/app/auth.go`.
5. If new wire headers appeared, add them to `KNOWN_HEADERS` in `extract.mjs`.
6. Record any intentional divergences in the table above.
7. Run `make verify` — must pass.

---

## Descoped CC features (not a compatibility concern)

Bridge/IDE integration, remote sessions, Agent Teams tmux/OS-process display (conduit uses in-process compositor), AWS auth,
mTLS, GrowthBook feature flags, Anthropic-internal analytics, voice STT,
KAIROS assistant mode, and ULTRAPLAN are intentionally excluded. They do not
affect the wire format for normal Claude Max/Pro subscription usage.
