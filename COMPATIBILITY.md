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
| `Version` (Claude Code version claim) | `cmd/conduit/main.go` | `2.1.259` |
| `SDKPackageVersion` | `internal/api/client.go` | `0.112.1` |
| `anthropic-version` header | `internal/api/client.go` | `2023-06-01` |
| OAuth client ID | `internal/auth/flow.go` | see source |
| Token URL | `internal/auth/flow.go` | see source |
| OAuth beta header | `internal/app/auth.go` | `oauth-2025-04-20` |

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
| Agent Teams: teammate process model | Separate OS processes; each teammate is a `claude` subprocess | In-process goroutine `Loop`s sharing the same process; no subprocess or shell involved | Single-process Go architecture; `internal/agent/loopteammate.go` |
| Agent Teams: display | tmux panes / iTerm2 split views managed by CC | In-process split-pane compositor via `uv.Screen`; `internal/tui/teampanes.go` | Reuses the existing Ultraviolet cell-buffer compositor; no tmux dependency |
| Agent Teams: `teammateMode` `tmux`/`auto` | `tmux` → real tmux panes; `auto` → detect best | Both map to in-process display; no tmux ever invoked; no error raised | No tmux dependency in conduit |
| Agent Teams: task list storage | Shared on-disk JSON file; `fcntl` file locking for cross-process safety | In-memory `tasktool.Store` (mutex-guarded); no file I/O during task ops | Single process; no cross-process coordination needed |
| Agent Teams: teammate message delivery | IPC / OS pipes between subprocesses | `team.Team.Send` (buffered in-process channel, 64-deep); delivery pump goroutine per teammate drains inbox → `child.InjectMessage`; messages land at turn boundaries | Reuses the existing `InjectMessage` queue (`internal/agent/loop.go:msgQueue`) |
| Agent Teams: `TeamCreate`/`TeamDelete` tools | Existed in CC 2.1.177; removed in 2.1.178 | Not implemented; session-derived naming via `team.SessionName(sessionID)` matches CC 2.1.178+ | Follows CC 2.1.178+ which removed these tools |
| Agent Teams: plan-approval flow | Lead agent runs in a separate process; plan delivered via IPC | Lead receives `<team-plan from=…>` injected as a user message; approves via `SendMessage` kind `plan-approve/reject` which writes to `member.PlanReply` channel; teammate's `ExitPlanMode.AskApprove` blocks on that channel | Same behavioral result; implemented without IPC using Go channels |
| Agent Teams: shutdown protocol | CC orchestrates subprocess termination | Lead sends `SendMessage` kind `shutdown-request` → teammate receives `<team-shutdown-request>` injection → replies `shutdown-approve/reject` via its own `ShutdownReply` channel → approve cancels the goroutine context | Goroutine cancellation replaces process kill |
| Beta `ccr-byoc-2025-07-29` | Sent (bring-your-own-cloud gate for Bedrock/Vertex customer-hosted deployments) | Not sent | Gates enterprise BYOC deployments; conduit only authenticates via Max/Pro OAuth and never exercises that path — same reasoning as `oidc-federation-2026-04-01` above |

---

## Wire sync log

### Tooling: Claude Code's Bun bundle format changed (2026-09-03)

`make wire-all` stopped working because Anthropic changed how the `claude` binary is built.
Through 2.1.226 it was one monolithic file with thousands of inlined CommonJS module wrappers;
starting at 2.1.259 it's code-split into ~1600 separate real ES modules. `bun-demincer`'s
`resplit.mjs` only understood the old shape, so decode silently produced almost nothing and
every wire anchor came back empty.

Fixed: `scripts/wire-check/decode.mjs` now resolves the bundler entry point from
`extracted/manifest.json` instead of a hardcoded path, and detects the bundle shape to pick
between `resplit.mjs` (monolithic) and the new `bun-demincer/src/resplit-esm.mjs` (code-split
ESM). See `scripts/wire-check/README.md` ("Bun bundle format") for detail.

With extraction working again, `make wire-all` surfaced real drift against upstream 2.1.259,
applied below.

### 2.1.226 → 2.1.259 (2026-09-03)

| Item | Action |
|------|--------|
| `Version` | Bumped to `2.1.259` in `cmd/conduit/main.go` |
| `SDKPackageVersion` | Bumped to `0.112.1` in `internal/api/client.go` |
| Beta `ccr-byoc-2025-07-29` | Not added — bring-your-own-cloud gate for Bedrock/Vertex customer-hosted deployments; conduit only authenticates via Max/Pro OAuth. Recorded as an intentional divergence above (same reasoning as `oidc-federation-2026-04-01`). |
| New headers (v2.1.259) | `anthropic-mcp-discover-protocol-version`, `anthropic-mcp-registry`, `anthropic-oauth-token`, `anthropic-organization-id`, `anthropic-user-profile-id`, `anthropic-telemetry`, `x-claude-code-signature`, `x-claude-gateway-user-email`, `x-claude-gateway-user-id` added to `KNOWN_HEADERS` in `extract.mjs` — all account/gateway/registry metadata or bridge-only signing, not part of conduit's request shape. |
| `claude-opus-5` / `claude-fable-5-1` added | New model generation. Added to builtin catalog ($5/$25 and $10/$50 per 1M respectively, both 1M context, thinking=true — pricing confirmed against `platform.claude.com/docs/en/about-claude/pricing`, not the decoded bundle, which no longer carries static pricing/context data — that's fetched from a runtime config at call time as of this version). `model.Default` moved to `claude-fable-5-1`. No migration aliases: `claude-fable-5`, `claude-opus-4-8`, and `claude-opus-4-5` are still current, non-retired, selectable models per Anthropic's own pricing table, not superseded IDs — `migrations.go`'s alias map is only for IDs actually removed from the picker (its own existing precedent: `claude-opus-4-7` → `claude-opus-4-8`, because 4-7 isn't in the catalog at all). |
| `claude-sonnet-5` pricing corrected | Was `$3.00/$15.00` (a pre-launch estimate carried over from the 2.1.200 sync); Anthropic's pricing page confirms the `$2/$10` introductory rate became the permanent price (the scheduled Sept 2026 increase to $3/$15 was cancelled). Corrected in `builtin.go` and `cost.go`. |
| `claude-mythos-5` / `claude-mythos-5-1` — not added | Real, priced models (same tier as Fable: $10/$50/MTok) but explicitly limited-availability/early-access per Anthropic's docs, not generally available. |
| MCP startup hang fix | `internal/mcp/manager.go` `connectWithCwd` had no per-server connect timeout — one wedged stdio MCP server could hang conduit's entire startup indefinitely before the TUI ever appeared. Added a bounded timeout per server (unrelated to wire compat directly, but found and fixed alongside this sync; see `STATUS.md`). |
| **`BillingVersion` missed in the `Version` bump — broke every request, caught by live testing** | `internal/agent/systemprompt.go`'s `BillingVersion` const (used in the `cc_version=` billing header sent on every request) was a *separate* hardcoded `"2.1.200"` that this sync's `Version` bump didn't touch, despite an existing `// must match cmd/conduit/main.go var Version` comment on it. Anthropic's API validates the two are consistent and returned `400 Bad Request: ... version 2.1.251 or newer is required` for every single request — not caught by `make verify` (no test asserts wire-format consistency at this level) or by `scripts/wire-check` (no anchor tracks `cc_version` specifically inside the billing header — see follow-up below). Found only by actually running the built binary. Fixed: bumped to `2.1.259`, added a matching cross-reference comment on `main.go`'s `Version` so the next sync can't miss it as easily, and noted `BillingCch`'s doc comment now explicitly says it's a carried-forward assumption (not re-verified this sync) rather than implying it was checked. **Follow-up worth doing**: add a `scripts/wire-check` anchor for the billing header's `cc_version=` field so this class of drift is caught by the tool next time, not by a live 400. |

`cch` (billing header) still requires a live mitmproxy capture — the extractor has never been
able to read it statically (`<<bun-macro>>`, unchanged since 2.1.226); not a regression from
the tooling fix above. `NEW tools`/`NEW headers` rows from the raw diff are large and include
known noise from the tool-name heuristic (see `scripts/wire-check/README.md` — "Treat the tool
diff as informational, not authoritative"); not reproduced here beyond the headers actually
triaged.

### 2.1.177 → 2.1.200 (2026-07-05)

| Item | Action |
|------|--------|
| `Version` | Bumped to `2.1.200` in `cmd/conduit/main.go` |
| `BillingCch` | Updated to `00000` (v2.1.200 firstParty value; bedrock/vertex remain `00000`; Bun macro is no longer used — inline check in `decoded-2.1.200/1470.js`). |
| `claude-fable-5` re-enabled | Previously restricted by US government policy; re-enabled as of CC 2.1.200. Added to builtin catalog (`$10/$50 per 1M`), restored as `model.Default` and `model.Fast` fallback; removed fable→opus migration aliases; restored to settings panel and model picker. |
| `claude-sonnet-5` added | New model (`latest_per_family.sonnet = claude-sonnet-5`). Added to builtin catalog ($3/$15 per 1M, 1M context, thinking=true), cost table, and model picker. `model.Fast` updated to `claude-sonnet-5`. |
| Pricing corrections | Opus 4.x: $5/$25 (was $15/$75); Haiku 4.x: $1/$5 (was $0.80/$4.00); Fable/Opus 4 pricing per CC 2.1.200 skill reference. |

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
