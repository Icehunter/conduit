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
| `Version` (Claude Code version claim) | `cmd/conduit/main.go` | `2.1.294` |
| `SDKPackageVersion` | `internal/api/client.go` | `0.128.0` |
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
| MCP config env expansion | `${VAR}` only; `${VAR:-a:-b}` defaults to `a` (JS two-arg `split` truncates); missing vars are a config-load warning | `${VAR}`, `${VAR:-default}` and bare `$VAR`; `${VAR:-a:-b}` defaults to `a:-b` (POSIX); missing vars are reported per-server in `ConnectedServer.Error` | Unresolved references stay verbatim in both, so a typo is visible rather than silently empty; conduit reports at connect time because that's where the value is used (`internal/mcp/config.go:expandEnvVars`) |
| Tool names `mcp`/`mcp__` | Pass-through aliases | `ListMcpResources`/`ReadMcpResource` | Conduit's MCP surface is explicit, not aliased |
| MCP project-server approval | `enabledMcpjsonServers` / `disabledMcpjsonServers` / `enableAllProjectMcpServers` come from every settings layer, including the repo's `.claude/settings.json` and the `.claude/settings.local.json` its approval dialog writes (`getProjectMcpServerStatus`) | Read only from user-level settings and conduit's per-project state in `~/.conduit/conduit.json`, where the approval prompt records answers; settings files inside the checkout are ignored, and no `.mcp.json` server starts while the workspace is untrusted | A cloned repo could otherwise approve its own `.mcp.json` servers and have their commands run on first launch. Approvals CC recorded in `.claude/settings.local.json` don't carry over, so conduit asks once (`internal/mcp/config.go:mcpjsonApprovalState`) |
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
| Retry headers (adopted) | The SDK honors `retry-after-ms`, then `retry-after`, then `x-should-retry` | Same order in `api.withRetry` (`retryAfterFromHeaders`, `shouldRetryStatus`); a retry-after past 2 minutes is surfaced instead of waited out | Matches CC; the cap mirrors CC giving up on long subscription limits |
| Retry scope | The SDK retries 408, 409, 429, 5xx and connection errors; stream failures after a 200 are not resent | Same status set before a response. After a 200 the loop resends up to 3 times with backoff, then rotates provider, then keeps the completed text | Local and OpenAI-compatible servers drop streams far more often than the first-party API |
| Empty response | No equivalent adopted | Retries once with a `<system-reminder>` nudge merged into the trailing user message (no turn used), then records `(no content)` and shows a notice | A silent empty turn looks like a hang; the nudge is conduit's, not CC's |
| Refusal | No equivalent adopted | Returns `ErrModelRefusal` with a notice and drops the refused prompt from history | Replaying a refusal primes the next request |
| Reactive compaction (adopted) | On a context-overflow error CC lowers max_tokens (`max(3000, limit − input − 1000)`), then compacts and retries once | Same order, each step once per Run (microcompaction between the two); a second overflow surfaces. The max_tokens clamp also covers OpenAI/vLLM wordings and is limited to 2 per request; `model_context_window_exceeded` takes the same path | Matches CC; local servers word the overflow differently |
| Local fallback summary | A failed summarizer fails the compaction | Reactive compaction falls back to a deterministic local summary (`compact.CompactLocal`) when the summarizer errors, and any compaction whose summary has no usable text uses it too (notice "Summary model unavailable — used a local summary") | The request cannot continue without a smaller history, and an empty summary used to wipe it |
| Auto-compact trigger cap | Triggers at window − 33K (about 967K on a 1M window) | `min(max(window − 33K, 75% of window), compaction.maxTriggerTokens)`, default 256K, 0 = no cap (the 75% floor keeps a 32K local model from compacting every turn); `CLAUDE_CODE_AUTO_COMPACT_WINDOW` still applies | A 1M conversation is slow, costly and degraded long before 967K |
| Compaction tail and persistence | Summary replaces the history | The latest min(20K tokens, window/5) of messages stays verbatim after the summary; every compaction is appended to the session file behind a `compact_boundary` (older records stay for `session_search`) | Keeps the working context, and `/resume` restores the compacted conversation |
| Compaction summarizer | Small fast model | The background role's model, else Haiku only on an Anthropic client that can serve it (first-party, or a non-Copilot gateway with dash-style Claude IDs), else the active model; never a Claude ID for a non-Anthropic client | A local or Copilot client has no Haiku to call |
| Cut-off tool call | No equivalent adopted | Kept with empty input and answered with an error `tool_result` telling the model to re-issue it | The model learns the call failed; the UI row closes |

---

## Wire sync log

### 2.1.293 → 2.1.294 (2026-10-08)

| Item | Action |
|------|--------|
| `Version` / `BillingVersion` / `BillingHeader` | Bumped to `2.1.294`. The fingerprint differs only in the version strings; SDK, client ID, OAuth, betas, headers, tools and model IDs are unchanged (`make wire-claude`: 0 NEW). |

### 2.1.292 → 2.1.293 (2026-10-08)

| Item | Action |
|------|--------|
| `Version` / `BillingVersion` / `BillingHeader` | Bumped to `2.1.293`. SDK, client ID, OAuth, betas, headers and tools unchanged (`make wire-claude`: 0 NEW). |
| `claude-haiku-5-5` added | Haiku 5.5: 1M context (native), 128K output, pricing tier `haiku_55` ($0.10/$0.50, cache read $0.01) with a `long_prompt` tier above 100K prompt tokens ($0.50/$2.50, cache read $0.05). `cost.go` rows can now carry a long-prompt tier. Bundle aliases: `haiku` → `claude-haiku-5-5`, `latest_per_family.haiku` likewise; `resolveModelName`, the `/model` shortlist and a `haiku5.5` shorthand follow. `compact.DefaultModel` moved to `claude-haiku-5-5` with the auto-compact model work (CC 2.1.293 `latest_per_family.haiku`); the web-search model stays on Haiku 4.5. |

### 2.1.266 → 2.1.292 (2026-10-06)

The 2.1.280 sync (commit `e10ba98`) only updated the fingerprint and model catalog; the version constants stayed at 2.1.266. This sync covers both releases.

| Item | Action |
|------|--------|
| `Version` / `BillingVersion` / `BillingHeader` | Bumped `2.1.266` → `2.1.292` in `cmd/conduit/main.go` and `internal/agent/systemprompt.go`. |
| `SDKPackageVersion` | Bumped `0.112.1` → `0.128.0` in `internal/api/client.go`. |
| `cc_version` suffix algorithm | Unchanged: same salt, same `[4,7,20]` indices, same SHA-256 slice (`decoded-2.1.292/0618_e5o.js` `vk()`, identical to 2.1.266 apart from minified names). `TestComputeBillingSuffix_MatchesLiveCapture` now pins the algorithm against the 2.1.266 live capture with the version passed explicitly (`billingSuffixFor`), so a version bump no longer breaks it. A fresh live capture for 2.1.292 has not been taken. |
| `claude-sonnet-5-5` added | New model: Sonnet 5.5, 1M context (native), 128K max output, pricing tier `tier_2_10` ($2/$10, cache read $0.20). Added to `internal/catalog/builtin.go`. |
| Family aliases | The bundle's `aliases` table now maps `opus` → `claude-opus-5-5` and `sonnet` → `claude-sonnet-5-5` (`latest_per_family` agrees). `resolveModelName` follows; `opus5.5`/`sonnet5.5` shorthands added; the `/model` account shortlist and `model.Fast` (`/fast`) move to the 5.5 models. `opus5`/`sonnet5` still name the 5.0 models. |
| Pricing table (`internal/api/cost.go`) | Rebuilt from the bundle's `pricing_tiers` table. Fixes: `claude-opus-5-5` was billed at Opus 5's $5/$25 (prefix match) instead of $4/$20; `claude-mythos-*` had no row ($0); `claude-opus-4-0`/`4-1` were billed at $5/$25 instead of $15/$75; cache reads were a flat 10% of input, but Fable 5.1 and Mythos 5.1 read at $0.25 and Opus 5.5 / Sonnet 5.x at $0.20. Rows now carry an optional cache-read rate. Pinned by `TestCostUSDForModel`. |
| Client-hint request headers (adopted) | CC sends these on `/v1/messages` when `DEn()` is true, which for a first-party OAuth or API-key session against `api.anthropic.com` needs no flag (`CLAUDE_CODE_GATEWAY_HINT_HEADERS` overrides). Conduit was missing all of them since at least 2.1.280. Now sent via `api.WireHints` (`internal/api/wirehints.go`): `x-claude-code-request-class` (`main`/`subagent`/`workflow`/`compaction`/`auxiliary`), `x-claude-code-agent-type` (`general-purpose`, `custom`, `teammate`), `x-claude-code-prompt-id` (new in 2.1.292; same UUID as `cc_prompt_id`), `x-claude-code-compaction` + `x-cc-compaction-request` on the summarization request, `x-claude-code-context-compacted` + `x-cc-context-compacted` on the first main request after a compaction, and `x-claude-code-prev-tool-durations` (`Name=ms;…`, max 32 entries / 4096 bytes, CC's escaping). `x-claude-code-agent-id` / `x-claude-code-parent-agent-id` are sent on sub-agent requests to any Anthropic endpoint, as in CC. Gates match CC: the `x-claude-code-*` set follows `DEn()` (first-party base URL, or `CLAUDE_CODE_GATEWAY_HINT_HEADERS` parsed as CC's triBool: `1/true/yes/on`, `0/false/no/off`); the legacy `x-cc-*` pair follows `yl()` (first-party base URL only, no env override). The context-compacted flag rides on every attempt of the first request after a compaction (CC re-adds it per retry), clears once a response drains, and is forgotten on `/clear`, `/rewind` and a session switch. Request classes follow CC's `rs()`: agent hooks and forked skills are `subagent`; prompt hooks, memory extraction, session memory and dream are `auxiliary`. The `reactive` context-compacted value is adopted: after a compaction forced by a context-overflow error the next request carries `x-claude-code-context-compacted: reactive` on every attempt, while the summarization request still sends `compaction: auto`. |
| Other new headers (not adopted) | Baselined in `KNOWN_HEADERS` with reasons: `anthropic-cache-keepalive`, `x-claude-code-tunnel-credential-epoch`, `x-anthropic-attachment-prefetch-seen` (response headers); `x-anthropic-attachment-prefetch`, `x-claude-remote-session-origin`, `anthropic-client-feature`, `anthropic-client-version` (other endpoints/modes); `anthropic-example`, `anthropic-experimental`, `anthropic-plugin-directory`, `anthropic-sans`, `anthropic-serif`, `x-claude-code-keep-branch` (scan false positives). |
| New tools (not ported) | `AppifactRepl`, `FetchInboxMessage`, `GetTask`, `PublishPlugin`, `SubagentHandback` added to `TOOLS_DELIBERATELY_NOT_PORTED` with reasons. `Artifact` left the tool list upstream. |
| `client_id`, OAuth URLs, beta registry, `billing_salt`, `anthropic-version` | Unchanged. `make wire-claude-fast`: 0 CHANGED, 0 NEW. |

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

### 2.1.259 → 2.1.266 (2026-09-09)

| Item | Action |
|------|--------|
| `Version` | Bumped to `2.1.266` in `cmd/conduit/main.go` |
| `BillingVersion` / `BillingHeader` | Bumped to `2.1.266` in `internal/agent/systemprompt.go`. `verify.mjs` now checks `BillingVersion` and the `BillingHeader` literal against the fingerprint version (the follow-up from the 2.1.259 entry below), so this class of drift is caught by `make wire-claude-fast` instead of a live 400. |
| `SDKPackageVersion` | Unchanged at `0.112.1` |
| `X-Stainless-Runtime-Version` | `v24.3.0` → `v26.3.0` (live capture). Now a named constant `StainlessRuntimeVersion` in `internal/api/client.go`. |
| `billing_salt`, client ID, OAuth URLs, beta registry, model IDs | Unchanged — no new model literals in the decoded bundle. `computeBillingSuffix` re-verified against the live capture (`"say the word hello and nothing else"` → `0f4`), pinned by `TestComputeBillingSuffix_MatchesLiveCapture`. |
| **`cch` is per-request, not per-build** | Live mitmproxy capture (`scripts/wire-check/history/2.1.266/live-capture.json` + repeated probes): `cch` differed on *every* request — `2642f`, `67f7c`, `6afbe`, `1c474`, `396d2`, `92a39` — including four identical prompts on the same model. The decoded JS only ever contains the `cch=00000` literal (`1647_b8t.js` `b8t()`), so the substitution is native and its input isn't visible; no hash (md5/sha1/sha256/sha512/blake2b/sha3) over any permutation of prompt-id / session-id / client-request-id / device-id / account-uuid / salt / version / header prefix reproduces it. **Not reproducible.** The same source sends `cch=00000` verbatim for Vertex accounts, and the API has accepted `00000` from conduit since 2.1.200, so conduit keeps sending it. Every prior note that called it "static per-build" (`capture.py`, `systemprompt.go`, the 2.1.179/2.1.200 entries) was wrong and is corrected. |
| `cc_prompt_id`, `cc_prev_req`, `cc_is_subagent` (billing header) | New fields from `b8t()`, adopted. `DynamicBillingBlock` now takes a `BillingContext`: `cc_prompt_id` is a fresh UUID per root `Run()` (one user prompt, tool turns included) that child loops inherit via `parentPromptID`; `cc_prev_req` is the previous response's `request-id` header (validated `^req_[A-Za-z0-9_-]{1,36}$`); `cc_is_subagent=true` on child loops. Field order matches CC: `cch; cc_is_subagent; cc_prev_req; cc_prompt_id`. `cc_workload` not adopted (CC-internal workload tagging). |
| `anthropic-thinking-prefix-mismatch` (new header) | **Response** header, not a request header. Baselined in `KNOWN_HEADERS`. CC 2.1.266 introduces a *strict prefix lock*: thinking-block signatures are bound to the conversation they were produced in, and replaying one elsewhere (after compaction, `--resume`, a model swap) returns `400` with `"not created in this conversation"` / `"bound to a different conversation"` plus this header naming the first offending block (`block=messages.N.content.M;kind=<word>`). Adopted CC's heal: `internal/api` now returns a typed `*APIStatusError` (same `Error()` text as before) carrying the parsed header; `internal/agent/loop_prefixlock.go` strips thinking from the named block onward for the first two attempts, then all thinking, then surfaces the error — mirroring CC's `Afs=2` budget, `pAt` per-message strip, and `[Thinking removed]` placeholder. |
| Live-only deltas not adopted | `effort-2025-11-24` absent from the capture because Haiku 4.5 doesn't support effort (`uy(model)` gate) — conduit's per-model beta denylist already covers this. `thinking.display: "omitted"` is set only for non-interactive sessions. `diagnostics.previous_message_id` is behind three feature flags. |
| Tool extraction rewritten for the ESM bundle | The old file-level `userFacingName`+`input_schema` gate matched everything in the one giant ESM module (433 "tools": protobuf enums, X.509 names…). `extractToolsESM` now parses prettier-formatted object literals structurally (own keys at the inferred indent; requires `name` + `inputSchema` + `call`/`userFacingName`) and resolves identifier names through same-file `var` declarator lists and manifest-mapped cross-chunk imports. Yields the real 62-tool list. `verify.mjs` now reads conduit's tool names from `Name()` literals/consts instead of directory names, maps aliases (`Agent`→`Task`, `Skill`→`SkillTool`, `PowerShell`→`Shell`, MCP resource tools), and carries `TOOLS_DELIBERATELY_NOT_PORTED` with reasons (claude.ai Design/Projects/Artifacts, Cron, self-hosted runners, cloud memory, onboarding/feedback UI). `ccr-byoc-2025-07-29` added to `BETAS_DELIBERATELY_NOT_SENT`. `make wire-claude-fast` exits 0 again. |

### Thinking-block round-trip fix (2026-09-06)

| Item | Action |
|------|--------|
| **Empty thinking blocks dropped their required `thinking` field — 400 on every follow-up turn** | `ContentBlock.MarshalJSON` in `internal/api/types.go` emitted `thinking` under `if cb.Thinking != ""`. Fable 5.1 returns *signed* thinking blocks with no visible reasoning text, so the block serialized as `{"type":"thinking","signature":"…"}` and the API rejected the whole conversation with `400 invalid_request_error: messages.1.content.0.thinking.thinking: Field required` on the turn after the first tool call. The same omission also stripped the field from persisted session JSONL, so `--resume` reproduced it. Fixed by always emitting `thinking` when `Type == "thinking"`. |
| Unsigned thinking blocks stripped before the wire | The API also requires `signature` on replayed thinking blocks. A stream cut before its `signature_delta` left an unsigned block in history that would 400 the next turn. `dropUnsignedThinkingBlocks` (called from `sanitizeAnthropicRequest`, so both the streaming and non-streaming paths get it) removes them, dropping any message left with no content. `buildContentBlocks` additionally stops persisting thinking blocks that have neither text nor signature. |

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

## Intentional behavior divergences

- **WebSearch provider chain.** Claude Code's WebSearch uses only Anthropic's server-side `web_search`. Conduit tries Brave (key), Anthropic-native (Anthropic-capable clients only), the ChatGPT/Codex subscription, then a keyless tier (Parallel public MCP, DuckDuckGo, Mojeek). None of this touches the Anthropic wire format. Because the keyless tier sends the query to third parties, `webSearch.keyless` in conduit.json gates it: `auto` (default) uses it only when no other provider is configured or usable, and returns a configured provider's error rather than falling back; `always` falls back after a failure; `never` disables it.
- **UserPromptSubmit.** Claude Code's hook can block the prompt or add context, but cannot rewrite it. Conduit also accepts `hookSpecificOutput.updatedPrompt` (a string): it replaces only the typed-text block of the message, so @-mention file blocks, images and reminders stay. The rewrite is what the API receives and what history keeps (so `/resume` shows it), while the live transcript keeps the text as typed. Other deliberate differences: the hook input `prompt` is the raw typed text (the original `/cmd args` for a slash command) before @-mention expansion; `{"continue":false}` and `{"decision":"block"}` both remove the prompt from the conversation and put the typed text back in the input box, where Claude Code keeps the prompt in the transcript for `continue:false`; hooks run for steering typed mid-turn and for council and local-MCP prompts, which Claude Code has no equivalent of; conduit-only permission modes are reported to the hook as their nearest Claude Code mode (`acceptEditsLive` as `acceptEdits`, `council` as `plan`); a hook that is unreachable, times out or exits with a status other than 2 fails open and is reported to the user as a warning; project-local UserPromptSubmit hooks run only in a trusted workspace. `{"continue":false}` from any other event (PreToolUse and so on) blocks that event, never lets it through.

- **Background shells: `BashOutput` stays a live tool; `TaskOutput` differs.** Claude Code 2.1.293 reads a background task's output through `TaskOutput` and no longer exposes `BashOutput`. Conduit keeps `BashOutput {bash_id, filter?}` (output since its previous read, plus status and exit code) because its `TaskOutput` is the todo-notes appender from the task list, not an output reader. `TaskStop` accepts CC's `task_id` (and the older `taskId`/`shell_id`) and stops a background job when the ID is one, else cancels the todo task; `KillShell` and `KillBash` are registered as deferrable wrappers of it. Job IDs are `bash_N`, so they cannot collide with todo IDs (`task_N`).

---

## Descoped CC features (not a compatibility concern)

Bridge/IDE integration, remote sessions, Agent Teams tmux/OS-process display (conduit uses in-process compositor), AWS auth,
mTLS, GrowthBook feature flags, Anthropic-internal analytics, voice STT,
KAIROS assistant mode, and ULTRAPLAN are intentionally excluded. They do not
affect the wire format for normal Claude Max/Pro subscription usage.
