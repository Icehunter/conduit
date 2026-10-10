# Conduit — Capability Matrix

Last updated: 2026-10-08

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

### Optional verified local coding (Conduit extension)

| Capability | Status | Notes |
|-----------|--------|-------|
| Repository definition of done and bounded local repair | ✅ | `internal/verification`, `internal/coding`, `internal/app/coding.go`; opt-in TUI/print completion gate and `conduit code`; frozen checks, explicit permissions, stale-source/workspace checks, independent optional review; single-endpoint Qwen3.8-27B MCP failure→repair smoke passed 2026-10-08 ([evidence](docs/verified-local-live-smoke.md)) |
| Endpoint pools and review role | ✅ | Stable endpoint IDs, SQLite session/role affinity and expiring request leases for bounded implement/review; streaming role pools and external-router load telemetry are unsupported |
| Privacy-controlled episodes and SFT/DPO export | ✅ | Separate append-only JSONL; explicit source/content opt-in; strict provenance and human labeling; incomplete/redacted/evaluation traces excluded. Opt-in local-router v1 traces capture actual expanded messages/parameters; strict input/output/model binding and upstream endpoint identity; legacy MCP context stays ineligible. Live Qwen trace→SFT export smoke passed 2026-10-08 ([details](docs/mcp-request-traces.md)) |
| Held-out evaluation | ✅ | Pinned local checkout, immutable fixtures, authorized checks, enforced wall time and validated external-runner usage; not an OS sandbox |
| Offline QLoRA and adapter merge | 🔶 | Pinned-artifact Python pipeline and stdlib validation tests; GPU training/export execution has not been run; no automatic training/promotion |

Configuration and limitations: [`docs/verified-local-coding.md`](docs/verified-local-coding.md), [`docs/training.md`](docs/training.md).

| Capability | Status | Notes |
|-----------|--------|-------|
| Claude Max/Pro subscription (OAuth) | ✅ | PKCE flow, token refresh, keychain storage |
| Anthropic API key | ✅ | Direct API key auth |
| Multi-account switching | ✅ | `/account` panel, `/login --switch <email>` |
| OpenAI-compatible providers (Gemini, etc.) | ✅ | API key stored in secure storage; role-assignable. Full system prompt (agent prompt + CLAUDE.md; only the billing and Claude identity blocks are Anthropic-only). Streams end only on `finish_reason`/`[DONE]` (else `ErrStreamTruncated`); mid-stream errors become typed error events; `content_filter` → `refusal`; empty tool results sent as `(no output)`; `extraBody` is a JSON merge patch (`internal/api/openaicompat.go`, `openaistream.go`) |
| In-text tool calling (`toolCallFormat`) | 🔶 | Provider setting `native`/`hermes`/`qwen3`/`auto`: schemas in the system prompt, `<tool_call>` JSON parsed from streamed or complete replies into `tool_use`, history sent back as text (`internal/api/texttools.go`); `auto` falls back on a "does not support tools" 400/422. Other oh-my-pi syntaxes and the XML qwen variant are not implemented; see `docs/local-provider-plan.md` |
| OpenAI-compatible reasoning | ✅ | `/effort` → `reasoning_effort` (Responses: `reasoning.effort`); `reasoning_content`/`reasoning` deltas and leaked `<think>` blocks become thinking blocks (`internal/api/thinksplit.go`, code-span aware); provider `reasoningReplay` auto/on/off replays reasoning for local servers' KV cache, plus Qwen `preserve_thinking`; see `docs/local-provider-plan.md` |
| Local models via MCP | ✅ | Route any role to an MCP-backed local provider |
| Role-based model routing | ✅ | Default, Main, Background, Planning, Implement roles via `/model` Ctrl+M |
| OpenAI API key (C-O3) | ✅ | `internal/providerauth/`; Accounts tab connect/rotate/disconnect |
| Gemini API key (C-O3) | ✅ | `internal/providerauth/`; Accounts tab connect/rotate/disconnect |
| OpenRouter API key (C-O3) | ✅ | `internal/providerauth/`; Accounts tab connect/rotate/disconnect |
| Provider auth interface (C-O3) | ✅ | `internal/providerauth/` — `Method`, `Config`, `Authorizer`, `APIKeyAuthorizer` |
| Provider entries reference accounts (C-O3) | ✅ | Provider entries store a credential alias that resolves through secure providerauth storage; known providers reuse `openai`, `gemini`, and `openrouter` credentials |
| GitHub Copilot auth | 🔶 | Experimental: device-code login, Copilot token exchange, model discovery, `/chat/completions`, `/responses`, and Claude `/v1/messages` routing; gracefully reports entitlement/discovery failures. Route per model from `/models` `supported_endpoints` (recorded in the credential, name heuristic as fallback); Business/Enterprise seats use the exchange's `endpoints.api` host; a model-policy 403 keeps the credential. Per-request `x-initiator` (`user` only for a real prompt, `agent` for tool follow-ups, sub-agents, compaction), `X-Interaction-Id` = session id, `Copilot-Vision-Request` (`copilot.RequestHeaders` via `api.Config.RequestHeaders`) |
| OpenAI ChatGPT Plus/Pro OAuth | 🔶 | Experimental: browser PKCE login, secure refresh-token storage, Codex Responses routing through `https://chatgpt.com/backend-api/codex/responses`; manual verification still required. `session_id` header and `prompt_cache_key` per request (conduit session id), `reasoning.encrypted_content` requested and replayed with `store=false`, reasoning summaries shown as thinking, `cached_tokens` reported as cache reads, input capped at 272K for auto-compact. Model list is live from the Codex backend `/models` (visible entries), then the OpenRouter-sourced catalog, then the static list; `gpt-5*-pro` and non-chat models are filtered (`codex.IsAgenticModel`) |
| Grok / xAI subscription OAuth | 🔶 | Experimental: PKCE browser login (loopback `/callback`, CORS for accounts.x.ai) with the Grok CLI public client, device-code flow in `internal/provider/xai` (not wired to the TUI), refresh serialized by `credlock`, Bearer access token on `https://api.x.ai/v1` chat completions. Model list live from `/v1/models` (text models only), then a static fallback. Add via Settings → Providers → Grok / xAI; manual verification against a real Grok account still required |
| Provider-account wire checks | ✅ | `PROVIDER_COMPATIBILITY.md` plus `make wire` cover Copilot, ChatGPT/Codex and Grok/xAI drift guards; Claude remains under `make wire-claude` |

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
| Auto-compact | ✅ | `internal/agent/loop_compact.go`. Fires at end_turn when the prompt (input + cache tokens) passes `AutoCompactThreshold(window, cap)`: window − 33K, capped at `compaction.maxTriggerTokens` (default 256K, 0 = no cap; read per end_turn through `LoopConfig.AutoCompactTriggerCap`), honoring `CLAUDE_CODE_AUTO_COMPACT_WINDOW`, and never below 75% of the window (a 32K local model would otherwise trigger at a negative count). The summarizer is the background target: the background role's client and model, else Haiku on an Anthropic Messages client that is first-party or another gateway serving dash-style `claude-*` IDs (never Copilot, decided by endpoint, since it also has dash-style IDs such as `claude-sonnet-4`), else the active client and model, and it follows provider failover. A non-transient failure of the target retries once on the active model with a notice; the summarizer's usage goes to `OnSubAgentUsage`. A compaction that cannot get the prompt under the trigger (system prompt and tools nearly fill a small window) is not repeated until the prompt grows 25% past it or falls under the trigger; the summary's user-message and anchor budgets also shrink with the window (window/8 and window/32 characters). A breaker opens after 3 non-transient failures in a row (transient ones do not count; success, `SetModel` and `SetClient` reset it). Sub-agents skip end_turn compaction but keep reactive. `DISABLE_AUTO_COMPACT` turns both off |
| Reactive compaction | ✅ | `recoverOverflow` (overflow errors and `model_context_window_exceeded`), each step once per Run: a max_tokens overflow lowers max_tokens to `max(3000, limit − input − 1000)` (at most 2 clamps; a prompt that alone overflows skips it; an output cap in the error clamps to the cap) and the thinking budget with it; then microcompaction if it shrinks the payload; then a compaction with a local-summary fallback, after which the next request carries `x-claude-code-context-compacted: reactive` on every attempt. A second overflow surfaces as an error |
| Compaction quality and persistence | ✅ | The most recent min(20K tokens, window/5) stays verbatim (split on an assistant message, so tool pairs are never cut); the summary message carries the user's messages verbatim, an anchor index and a recovery footer pointing at `session_search` / `CCRRetrieve` when registered. A summary with no usable text falls back to a deterministic local summary (notice "Summary model unavailable"); nothing to compact is not a failure ("Nothing to compact yet."). Auto, reactive and manual compaction all append (`Session.AppendCompaction`: summary, `compact_boundary`, history), written on the loop goroutine before the TUI hears of `EventCompacted`; the TUI sets `m.history` / `m.persistedCount` and shows "Context compacted (kind) — kept N recent messages; summary by <model>". `/resume` restores the summary and its tail; `/clear` writes a `session_reset` marker so a resume does not bring the cleared conversation back (`session_search` still indexes it); the indexer skips only the tail copies already in the file. Manual `/compact` summarizes through `Loop.BackgroundTarget()`. `compact.DefaultModel` is `claude-haiku-5-5` |
| Micro-compaction | ✅ | Clears old tool results after 60 min idle |
| Extended thinking (`/effort`) | ✅ | low/medium/high/max budgets |
| Collapsed thinking row | ✅ | `EventThinking` (`loopstream.go`) keeps reasoning out of the answer. The TUI shows a live dimmed "Thinking…" line, then a `RoleThinking` row, "Thinking… (N lines) [click to expand]", placed before the answer or tool rows; click toggles `ToolExpanded`. Covers Anthropic thinking and OpenAI-compatible reasoning; redacted/empty blocks show nothing; `/resume` rebuilds the rows from stored thinking blocks |
| Fast mode (`/fast`) | ✅ | Toggles `model.Fast` (Sonnet 5.5, CC's `latest_per_family.sonnet`); ⚡ badge |
| Retry policy | ✅ | One retry layer per phase. Before any response, `api.withRetry` retries network errors, 408, 409, 429 and every 5xx with `BackoffDelay` (1s base, 2x, 32s cap, ±25% jitter, 5 retries), honors `retry-after-ms` then `retry-after` and `x-should-retry`, returns a retryable status whose body classifies as non-transient (a 500 carrying an overflow message) at once, and gives up on a wait over 2 minutes. After a 200 (truncation, idle timeout, mid-stream error) only the loop retries: 3 resends of the byte-identical request with backoff (server hint capped at 60s), then provider rotation, then the completed text is kept as a partial message. The counter resets only after a clean drain. Retries, repairs and rotations do not use `MaxTurns`. `LoopConfig.Sleep` / `api.WithSleep` inject the wait. A 400, 401 or 403 is sent once. The idle-timeout error is no longer read as a user cancel (the caller's ctx decides) |
| Stream-end handling | ✅ | `internal/agent/loop_stop.go` `handleStop`. A stream with no `message_stop` is `ErrStreamTruncated`. An invalid or cut-off `tool_use` is kept with empty input and answered with an error `tool_result` telling the model to re-issue it (the TUI row closes). `max_tokens` with text continues with CC's nudge, up to 3 times; `refusal` returns `ErrModelRefusal` with a notice; `pause_turn` ends the turn with a notice; an empty (or thinking-only) response is retried once with a nudge merged into the trailing user message, then recorded as `(no content)` with a notice; `model_context_window_exceeded` goes to `recoverOverflow` (reactive compaction), and returns `ErrContextWindowExceeded` only when that cannot recover. Rows left open by a failed stream are closed with an error result. `EventNotice`, `EventProviderFailover` and `EventTTSR` render as system rows; the retry banner names the cause ("Rate limited", "API overloaded", "Server error (502)", "Connection error", "Stream stalled", "Response interrupted"); the plan-usage meter moves only on a rate limit; `EventPartial` is a TUI no-op (the partial was saved twice); auth, overflow and rate-limit errors carry a hint |
| Non-streaming request timeout | ✅ | `CreateMessage` wraps every call in a 5-minute `context.WithTimeout`; a trickling server cannot stall the loop indefinitely |
| Streaming idle-timeout hardening | ✅ | Fixed 2026-09-03. `internal/api/idlereader.go`'s `idleTimeoutReadCloser` wraps the response body in all three streaming transports (`stream.go`, `openaicompat.go`, `openairesponses.go`) with a 5-minute idle window (`streamIdleTimeout`, mirrors `nonStreamingTimeout`'s value) — any bytes at all (including SSE `ping` keepalives) reset the clock; only true silence trips it, cancelling a per-stream `context.WithCancel` derived from the caller's ctx, which is what the request was actually built with, so Go's transport tears the connection down mid-read. `Stream.Next()` surfaces a clear `"idle timeout"` error instead of a bare `context canceled`. The *total-duration* exclusion for streaming stays as-is (`nonStreamingTimeout` deliberately doesn't apply — long generations legitimately take minutes) |
| Response-header timeout | ✅ | `internal/api/headertimeout.go`. Every transport (Anthropic, chat, Responses) fails a request that gets no response headers within 5 minutes (`Config.ResponseHeaderTimeout`; none for local/LAN URLs or unset openai-compatible providers, `responseHeaderTimeout` provider setting in seconds) with `ErrResponseHeaderTimeout`, classified transient and resent once. Separate from the stream idle timeout, which only starts after headers |
| OpenAI transports: images and documents | ✅ | User `image`/`document` blocks go out as chat content parts (`image_url`, `file`) and Responses `input_image`/`input_file`; models the catalog lists without vision get a text note. Tool-result images are still text-only |
| Conversation recovery | ✅ | Partial blocks persisted; orphan tool_use filtered on resume; auto-retries after tool_use/tool_result chain validation errors by sanitizing history |
| Thinking prefix-lock heal | ✅ | Added 2026-09-09 (CC 2.1.266 parity). `internal/agent/loop_prefixlock.go`: on the strict prefix-lock 400 (`"not created in this conversation"` / `"bound to a different conversation"`), reads the `anthropic-thinking-prefix-mismatch` response header via the new typed `*api.APIStatusError`, strips thinking from the named block onward (2 partial attempts), then all thinking, then surfaces. Complements the 2026-09-06 fix that drops *unsigned* thinking blocks before the wire (`api.dropUnsignedThinkingBlocks`) and always emits the `thinking` key on signed-but-empty blocks (`ContentBlock.MarshalJSON`) |
| Billing context fields | ✅ | Added 2026-09-09 (CC 2.1.266 parity). `DynamicBillingBlock` now takes `BillingContext`: `cc_prompt_id` (fresh UUID per root `Run()`, inherited by child loops); `cc_prev_req` (previous response's `request-id` header); `cc_is_subagent=true` for child loops. `X-Stainless-Runtime-Version` bumped to `v26.3.0` (live capture). `cch` confirmed per-request native substitution (not reproducible; API accepts `00000`). `BillingVersion`/`BillingHeader` drift now caught by `make wire-claude-fast` without a live 400 (`verify.mjs` tracks both). Tool extraction rewritten for the code-split ESM bundle — structural AST parse + manifest-mapped identifier resolution; 62 real tool names, 0 protobuf noise. |
| Client-hint request headers | ✅ | Added 2026-10-06 (CC 2.1.292 parity). `api.WireHints` on `MessageRequest` (`internal/api/wirehints.go`): `x-claude-code-request-class`, `-agent-type`, `-prompt-id`, `-compaction`/`x-cc-compaction-request`, `-context-compacted`/`x-cc-context-compacted`, `-prev-tool-durations`, plus `-agent-id`/`-parent-agent-id` on sub-agents. Gated like CC's `DEn()` (first-party `api.anthropic.com`, or `CLAUDE_CODE_GATEWAY_HINT_HEADERS`). Loop identity set at every child spawn site; tool timings recorded in `executeTools`. |
| Mid-turn steering | ✅ | Message injected between tool batches; agent pivots without losing context |
| Sub-agents (Task tool) | ✅ | `internal/tools/agenttool/`; sub-agent token usage recorded in session JSONL via `OnSubAgentUsage`; child loops capped at 50 turns (`DefaultSubAgentMaxTurns`); cap returns `ErrMaxTurnsExceeded` — TUI shows a soft notice, subagent result carries an incomplete marker so the parent model knows the child was truncated rather than finished |
| Subagent permission-prompt bubbling | ✅ | Fixed and live-tested 2026-09-03. Sub-agents still never show a direct interactive prompt of their own (`AskPermission` stays nil — no change there), but a `DecisionAsk` no longer silently auto-allows: `LoopConfig.AskSubAgentPermission` (`internal/agent/loop.go`) bubbles the decision to whichever ancestor has an interactive session, inherited automatically through nested sub-agents via config copying. The TUI wires it to the same permission modal used for direct prompts (`internal/tui/run.go`/`permissionpanel.go`), showing `Subagent "<label>" wants to…` with Allow once / Switch subagent to auto mode / Deny. "Switch to auto" calls the gate's existing clamped `SetMode(ModeBypassPermissions)` — same mechanism `EnterAutoMode` already used, still bounded by the pre-existing escalation ceiling from `Gate.Clone()`. Headless/print mode (no relay wired) keeps the old silent-allow-except-plan-mode behavior unchanged. Confirmed live in the actual TUI: root `Task` dispatch still shows the unchanged 3-option root prompt; the sub-agent's own `Bash` call showed the new 3-option relay prompt; "Allow once" ran the tool for real (file created); the pipeline held up end to end. **A bug the live test caught that unit tests didn't**: `internal/agent/loopsubagent.go`'s `runSubAgentOnce` and `loopteammate.go`'s `SpawnTeammate` both build their child via the shared `buildChildLoop` helper, which never set the new `Loop.subAgentLabel` field — so the relay fired correctly but with an empty label, rendering visually identical to the old direct-prompt UI (same header, same options) and masking that the fix was actually active. Fixed by setting `child.subAgentLabel` right after `buildChildLoop` in both callers — which incidentally means Agent Teams gets this fix too (it was already inheriting `AskSubAgentPermission` via config copying; it just needed the label). Also found and left unfixed: the settings-panel "Auto Mode" picker option stores a `"auto"` string that no `permissions.Mode` case actually handles (unrelated pre-existing bug, `internal/tui/settingspaneldata.go`) |
| Coordinator mode | ✅ | Claude as orchestrator; task-notification XML |
| Council mode | ✅ | Parallel multi-model debate, synthesis, convergence detection, roles, voting |
| Agent Teams (experimental) | ✅ | `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` or `conduit.json` `agentTeams: true`; in-process goroutine teammates (divergence: no tmux/OS-process); lead+teammate multi-pane compositor layout; task store with Assignee/Dependencies + Claim/NextClaimable/auto-unblock; plan-approval handshake via PlanReply channel; shutdown-request/approve/reject protocol; lead inbox pump goroutines; live EventText streaming to TUI panes; `internal/team/`, `internal/agent/loopteammate.go` |
| Plan mode / ExitPlanMode approval | ✅ | Scrollable modal; auto/accept-edits/live-review/default/chat options; "chat about this" (Discuss:true) yields the turn — agent stops and waits for user input |
| Plan mode tool gating | ✅ | In plan mode non-read-only tools are auto-denied with no prompt; Task (sub-agents) is allowed (`internal/permissions/`) |
| System overlay (output style, plan/council directive) | ✅ | `Loop.SetSystemOverlay` blocks are appended after the per-Run rebuilt base; child loops clear `RebuildSystem` and inherit the parent overlay, so typed sub-agent prompts and the plan directive survive |
| Protected write paths | ✅ | Write/Edit/MultiEdit/NotebookEdit/HashEdit to `.git`, `.husky`, `.githooks`, shell rc files, `.vscode`/`.idea`, `.mcp.json`, `.claude.json`, `.config/git`, `.claude/{commands,agents,settings*}`, `.conduit/{conduit.json,mcp.json,commands,agents,settings*}` always Ask (Deny in plan/council), also through symlinks; hook approval and "always allow" do not bypass. acceptEdits auto-approves writes only inside cwd or trusted roots. Deny rules precede trust shortcuts. Config tool can no longer set permissions/env. AstGrep rewrite (any of its `paths`) and SkillManage create/update/promote targets are gated the same way. Gap: the prompt still offers "always allow" for protected paths (ignored) |
| Diff-first review gate | ✅ | Hunk-level Myers diff; per-hunk approve/reject/note; `acceptEditsLive` mid-turn pause |
| Decision journal | ✅ | Append-only JSONL; `RecordDecision` tool; council auto-records verdicts |
| Proactive health checks | ✅ | Git/deps pre-flight at session start; warnings in system context |
| Provider failover chains | ✅ | `internal/providerrotation/`; configure `providerChains.role: [key1, key2]` in conduit.json; 429/503/529 rotate to next provider in chain; cooldowns tracked in-process; `EventProviderFailover` emitted on swap |
| Token-Time Stopping Rules (TTSR) | ✅ | `internal/ttsr/`; regex rules in `.conduit/ttsr/*.md` frontmatter; 4KB sliding tail buffer; global 3-fires/turn circuit breaker; `EventTTSR` emitted on fire. Rules can match the visible text (default) or the decoded arguments of a finished tool call (`scope: text, tool`), be limited to tools (`tools:`), file paths (`paths:` globs, `**` crosses directories; the pattern may be omitted for a path-only rule) and agents (`agents:`, `main` or the agent type), run as `mode: interrupt` (default) or `warn` (the reminder rides with the next request after tool calls, no restart, no `EventTTSR`), and be gated with `repeat: once`, `repeat_gap: N` (user turns) and `max_fires` (per turn, now kept across the retries of a turn); fire history lives on the `Loop`. A repetition guard (`ttsr.WithRepetitionGuard`, on by default; `conduit.json` `ttsr.repetitionGuard: false` turns it off) interrupts text or thinking that ends in a unit of 8-600 bytes repeated 6+ times over 1200+ bytes (units of fewer than 3 distinct bytes ignored; units under 64 bytes need 20 repeats over 2400 bytes, 10 distinct bytes and no line break, so byte tables, zero fills and repeated log lines pass; nothing is judged inside an open ``` fence), at most twice per turn and counted apart from the 3-fire rule budget, with a correction that does not quote the loop. Warn rules count as fired when delivered, not when matched |
| Model-per-role sub-agent dispatch | ✅ | `SubAgentSpec.Role`; `LoopConfig.RoleResolver` resolves role→(model, client); `Task(role: "planning")` routes through configured provider; plugin agents declare `role:` in frontmatter; all roles configured via ctrl+m are now honoured by sub-agents |
| Workflows — JS-scripted multi-agent orchestration | ✅ | `internal/workflow/`: goja runtime (one goroutine owns the VM; host calls resolve promises back onto it) exposing CC's script API verbatim — `agent(prompt,{label,phase,schema,model,effort,isolation,agentType})`, `parallel`, `pipeline` (per-item chain, null/throw short-circuit), `phase`, `log`, `args`, `budget{total,spent(),remaining()}`, `workflow()` (one nesting level); `meta` extracted statically from the AST and rejected unless a pure literal; `Date.now`/`Math.random`/argless `new Date()` rejected statically and shimmed at runtime; caps: `min(16,CPUs-2)` concurrent, 1000 agents/run, 4096 items/call; all five graph-eng workflows run end-to-end in tests |
| Workflow journal + resume | ✅ | `journal.jsonl` per run under `<project>/workflows/<runId>/`; `Workflow({scriptPath, resumeFromRunId})` replays identical `agent()` calls. **Divergence**: keys are content-based (`sha256(prompt‖canonical(opts))` + occurrence), not CC's order-chained keys — replay itself reorders pipeline continuations, so chained keys miss everything after the first reorder; script text is deliberately not in the key so edit-and-resume works |
| Workflow worktree isolation | ✅ | `agent(…, {isolation:'worktree'})` → `git worktree add --detach` under `<repo>/.conduit/worktrees/`, child loop `Cwd` set; Bash (`cmd.Dir`), Read/Write/Edit/HashEdit/NotebookEdit/Glob/Grep/AstGrep/LSP resolve relative paths via `tool.CwdFromContext`; worktree auto-removed when clean, kept + logged when dirty |
| Task worktree isolation | ✅ | `Task(isolation: "worktree")` (CC's enum is `worktree|remote`; conduit supports `worktree` and accepts `none`/empty): `agenttool` creates a detached worktree via `workflow.NewWorktree` (unique per call), hands the path to `RunSubAgentTyped` through `tool.WithIsolatedCwd` so the child's `Cwd` is the worktree; afterwards `Worktree.Finish` removes it only when clean **and** HEAD unmoved (commits on the detached HEAD keep it), else the path is appended to the result. Non-git cwd returns a clear error and the child never runs. Permission gate is the parent's clone (the worktree lives under `<repo>/.conduit/worktrees`, inside the trusted root). Agent-teams spawn rejects `isolation` with an error. The worktree dir is added to `<git-common-dir>/info/exclude` (no gitlinks from `git add -A`); nested calls resolve to the main repo's `.conduit/worktrees`; first isolated call per process runs `workflow.PruneStale` (removal of admin entries for conduit's own vanished worktrees (no repo-wide `git worktree prune`) + removes clean, unreferenced dirs older than 24h) |
| Workflow tool (background + task-notification) | ✅ | `internal/tools/workflowtool/`; `{script\|scriptPath\|name, args, budgetTokens, resumeFromRunId}` starts a run and returns `runId` immediately; result injected as `<task-notification>` via `Loop.InjectMessage`; `op: status\|kill\|result` for polling; inline scripts persisted to `<run>/script.js`; `RunsDir` per project; killed on session exit |
| Workflow discovery + slash commands | ✅ | `~/.conduit/workflows` → `~/.claude/workflows` → `<cwd>/.claude/workflows` (`*.js`, symlink-aware, later wins); each saved workflow registers `/<name> <args>` which prompts the model to call `Workflow({name,args})` (CC behaviour — model stays in the loop to present the result); `/workflows` panel |
| Ultracode opt-in gate | ✅ | Tool description carries CC's opt-in rule; the `ultracode` keyword in a user message appends a system-reminder block for that turn; `/ultracode [on\|off]` flips a session flag that swaps the tool description to "author by default" and adds the reminder to every turn |
| `/workflows` panel | ✅ | `internal/tui/workflowpanel.go`; list view floated above input (status, agents settled/total, tokens, elapsed, current phase); detail view with phase tree, running nodes, narrator log tail, `x` kill, `s` save run script to `~/.conduit/workflows/<name>.js` and register the slash command live; working row shows `⟳ workflow <name> · <phase> n/m` while a run is live |

---

## Tools (38 built-in)

| Tool | Status | Notes |
|------|--------|-------|
| BashTool | ✅ | RTK filtering; Unix/macOS. Windows: Shell (PowerShell) instead. Output up to 1 MB stays in memory and is RTK-filtered whole; past that it streams to a spill file while the command runs. The model sees at most 30 KB / 2000 lines as head + tail around a `[N lines / M bytes omitted; full output: <path>]` marker, and the full output is kept in a private spill file under `~/.conduit/truncated/` (0600 file, 0700 dir, random name). The only kill is a 256 MB disk cap (`[killed: output exceeded 256 MB; partial output: <path>]`). Non-zero exits and timeouts get the same bounded output. Commands run in their own process group, so a timeout or cancel also kills their children (`internal/tools/bashtool/capture.go`, `procgroup_unix.go`) |
| Bash `run_in_background` | ✅ | `internal/bgjobs` (registry, `bash -c` in its own process group via `internal/procgroup`, detached from the tool call, combined output to a 0600 file under `truncate.Dir()`, 2 min default / 10 min max timeout that kills the group). `Bash` returns the job ID and output path at once; `BashOutput {bash_id, filter?}` returns output since its previous read (per-job offset) with status and exit code; `TaskStop` takes `task_id` and stops a job (todo-cancel otherwise), with `KillShell`/`KillBash` as legacy names. A finished job queues a `<task-notification>` (status, summary, last 4 KB, output path) through `Loop.InjectMessage`; an idle TUI session is woken by `tui.Wake` and starts a turn for it (same path now serves workflow completions, and a turn that ends with notifications queued starts another). `/bashes` (alias `/jobs`) picks a job, then View output or Kill, in the picker overlay; the footer shows `N shells` while any run. A stop the model or user asked for sends no notification; a sub-agent's or teammate's jobs are silent and stopped when it finishes; print mode injects only and tells the model nothing can follow its final response. Wakes that arrive during `/compact`, council, shell mode or local calls are held (`wakePending`) and delivered when idle; session exit kills all jobs |
| Bash read-only auto-approve | ✅ | `internal/shellsafe` `IsReadOnly`: strict AST classifier behind default/plan/council-mode auto-approval, `Bash(readonly:*)` rules and the agent's read-only fast path. Literal words only: no redirects (`2>/dev/null` and `2>&1` included), pipes, background jobs, assignments, declarations, `$VAR`/`$(...)`/`$((...))` expansions or backslash escapes; `&&`/`||`/`;` chains and subshells pass only when every part does, and `cd <dir>` only as the leading command. Per-command flag and operand policy from crush `945c518f`/`c848bb78`/`4e6d4bb6` (git branch/tag/remote/config/stash/reflog read forms only, `gitCodeExecFlags`, `find -exec/-delete`, `fd -x`, `rg --pre`, `sort -o`, `yq -i`, `tree -o`, `printf -v`, `test -v`, `uniq` one operand, `go` version/env/list/doc, `npm` ls/view/outdated, no awk/sed/pagers). Also fails closed on abbreviated long flags (`sort --outp=`), brace expansion (`--{ext-diff,stat}`), globs that could expand into flags (`rg foo *`), and wrapper flags that change the program (`env -S`, BSD `env -P`) |
| FileReadTool | ✅ | Line-range support; URL scheme dispatch: `pr://owner/repo/N` and `issue://owner/repo/N` (gh CLI, RTK-filtered), `http(s)://` (webfetchtool); `anchors: true` prefixes each line with a 7-char content-hash anchor via `hashline.Compute`; records what it showed in the session read tracker (`internal/readtracker/`). Gap: no "unchanged since last read" re-read stub, and the tracker is not reset on compaction (`Tracker.Reset` exists, unwired) |
| FileWriteTool | ✅ | Overwriting an existing file requires a prior Read this session and an unchanged file (read-before-write guard, `internal/readtracker/`); creating a new file needs none |
| FileEditTool | ✅ | Exact-string replacement; refuses files not Read this session or changed on disk since (read-before-write guard; one tracker per session, shared with sub-agents) |
| HashEditTool | ✅ | Content-hash-anchored editing; `internal/tools/hashedittool/`; survives line-number drift; multi-op bottom-to-top application; `end_anchor` range replace/delete and `insert: before\|after`; overlapping edits rejected; anchors for lines no anchored Read displayed are rejected; UTF-8 BOM ignored when hashing and preserved on write; hidden from the tool list for models that misread the grammar (Kimi, MiMo, MiniMax, DeepSeek, StepFun, codex-spark, GLM-5.3 flash) so they use Edit; read-before-write guard; stages through diff gate |
| GrepTool | ✅ | ripgrep backend |
| AstGrep | ✅ | `internal/tools/astgreptool/`; structural search/rewrite via installed ast-grep/sg; rewrites stage through diff gate; no auto-download |
| GlobTool | ✅ | |
| AgentTool (Task) | ✅ | Sub-agent spawning |
| Workflow | ✅ | JS-scripted multi-agent orchestration; background run + task-notification; see Agent Runtime rows |
| WebFetchTool | ✅ | HTML→markdown |
| WebSearchTool | ✅ | Multi-provider, first non-empty result wins, in order: Brave (API key via secure storage or `BRAVE_API_KEY`), Anthropic-native `web_search_20250305` (only when the active client speaks the Anthropic Messages API), ChatGPT/Codex subscription (hosted Responses `web_search`, default `chatgpt-codex` credential only), then the keyless tier: Parallel public MCP, DuckDuckGo HTML, Mojeek HTML. The keyless tier sends the query to third parties, so `webSearch.keyless` in conduit.json gates it: `auto` (default) uses it only when nothing else is configured or usable (a configured provider that fails returns its error instead), `always` also falls back to it after a failure, `never` disables it. Codex search has a 60s budget, keyless 20s, and never follows redirects. Mojeek's ALTCHA wall is reported as a failure, not solved. `internal/websearch/{,brave,codex,parallel,duckduckgo,mojeek}/` |
| NotebookEditTool | ✅ | Jupyter cell edit; read-before-write guard |
| REPLTool | ✅ | persistent kernel per (session, agent, lang); each sub-agent and teammate gets its own interpreter (`tool.WithScope` carries the agent id; `Registry.ReleaseScope` closes its kernels when it ends); idle reaper; python + node; bash stays subprocess-per-call; `internal/kernel/` |
| SleepTool | ✅ | |
| TodoWriteTool | ✅ | |
| TaskCreate/Get/List/Update/Stop | ✅ | In-process task store; Assignee + Dependencies fields; Claim/NextClaimable (self-claim); Complete auto-unblocks dependents; OnCreated/OnCompleted hook callbacks |
| SendMessage | ✅ | `internal/tools/sendmessagetool/`; registered for lead when agent teams active; per-teammate sender identity baked via `NewFor`; kinds: message, plan-approve/reject, shutdown-request/approve/reject |
| EnterPlanMode / ExitPlanMode | ✅ | |
| EnterWorktree / ExitWorktree | ✅ | git worktree add/remove |
| EnterAutoMode / ExitAutoMode | ✅ | Conduit-original; bypassPermissions with user consent |
| AskUserQuestion | ✅ | Dismiss (Esc/ctrl+c) yields the turn (agent stops, waits for user); digit keys focus option only, Enter confirms; all async-opened overlays (permission prompt, plan approval, diff review, AskUserQuestion) swallow the first non-Esc keystroke to prevent buffered keystrokes from auto-accepting dialogs |
| ConfigTool | ✅ | get/set/allow/deny/env |
| MCPTool | ✅ | MCP tool proxy; registered and removed live as servers connect, reconnect, finish OAuth, or are disabled/added/removed (`app.SyncMCPTools`) |
| ListMcpResources / ReadMcpResource | ✅ | |
| SkillTool | ✅ | |
| LSPTool | ✅ | hover, definition, references, diagnostics, documentSymbol, workspaceSymbol, implementation, callHierarchy; `internal/tools/lsp/lsp_test.go` |
| ToolSearchTool | ✅ | Live registry search |
| SyntheticOutputTool | ✅ | Coordinator signalling |
| LocalImplement | ✅ | Conduit-original; MCP-backed bounded implementation. Apply mode: required `targets` (1-20 files), local-router writes them (needs `writeRoots`) and returns the diff; each target is gated as `Edit`; no draft fallback (`internal/tools/localimplementtool/`) |
| Implement-role guard | ✅ | While the MCP implement provider is reachable (a connected server exposes its implement tool, re-checked on every call; any pool member counts), the main model may Write/Edit only files recorded by LocalImplement (APPLIED) or CodingTask, and raw `*implement*` MCP tools are refused. Bash and other tools go through the normal permission gate. When the implementer is unreachable the guard is inactive and the main model edits directly, with a one-time notice in the first edit result. Enforced in every permission mode and in sub-agents (`internal/implguard/`, `internal/agent/loopimplguard.go`, `cmd/conduit/implguard.go`) |
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
| Project-scope approval gate | ✅ | Startup picker; answers persisted in `~/.conduit/conduit.json` (per project); approval read only from user-level settings and that state, never from settings files inside the repo; no `.mcp.json` server starts while the workspace is untrusted; a denied server is not asked about again; approving connects in the background |
| Resource listing and reading | ✅ | |
| Bounded per-server connect timeout | ✅ | `internal/mcp/manager.go` `ConnectTimeout` (15s); a wedged server (spawns but never completes the initialize handshake) used to block `ConnectAll`'s `WaitGroup` forever, hanging conduit's whole startup before the TUI ever appeared. Found via sibling-repo triage (crush fixed the same bug class) 2026-09-03 |
| Background connect at startup | ✅ | `Manager.StartConnectAll`: the TUI starts at once with each server listed as pending; a prompt waits for servers still connecting (including one just approved at the startup prompt) at most `mcp.InitWaitBudget` (10s) measured from startup (`LoopConfig.WaitForTools`, crush 1e5be92e); later prompts never wait. Connect, reconnect and disconnect of one server are serialized by a per-server lock |
| Live MCP tool sync | ✅ | The manager's change hook re-syncs the (lock-guarded) tool registry after every server state change: startup connect, `/mcp` reconnect/enable/disable, OAuth (`/mcp auth`, McpAuthTool), plugin install/uninstall/enable/disable, `/mcp-approve`, `/mcp add`/`add-json`/`remove`. Covers MCP tools, `authenticate` pseudo-tools and LocalImplement/LocalHelper. Sub-agent and teammate registries are live views (`tool.Registry.WithOverrides`/`Subset`), so late servers reach them and their ToolSearch offers only what they can call. Server instructions and coordinator server names are rebuilt per Run |

---

## Session & Memory

| Capability | Status | Notes |
|-----------|--------|-------|
| JSONL session transcripts | ✅ | `~/.conduit/projects/` |
| Session resume (`--continue`, `/resume`) | ✅ | Claude history fallback/import |
| Session rewind | ✅ | JSONL snapshots |
| Session search | ✅ | Fuzzy over JSONL transcripts; `after` (inclusive) / `before` (exclusive) date bounds (ISO date, RFC3339 or relative `7d`/`24h`/`2w`) and `exclude_session_ids` (cap 20) / `exclude_current_session`, applied in the SQL WHERE (`sessionsearch.Filter`). `exclude_current_session` uses the registry's live session id and errors when it is unknown |
| Session export (markdown) | ✅ | |
| Session rename / tag | ✅ | |
| Cost persistence | ✅ | AppendCost per turn; LoadCost on resume |
| File access history | ✅ | |
| MEMORY.md auto-memory | ✅ | user/feedback/project/reference types; compact prompt mode is default to reduce token overhead (`CONDUIT_MEMORY_PROMPT_FULL=1` restores full taxonomy block) |
| Memory extraction (auto) | ✅ | Sub-agent fires on each end_turn |
| Session memory summaries | ✅ | `summary.md` per session; injected on resume only when transcript history is unavailable (and size-capped) |
| Auto-dream consolidation | ✅ | 24h + 5 session gate |
| Curator (weekly memory+skill hygiene) | ✅ | fires after 7d or 10 sessions; exclusive cross-process lock (`flock` on `.curator.lock`, released by the kernel if the holder dies, so a leftover file never blocks); snapshot + deterministic transitions (active→stale 14d→archived 30d, `CuratorConfig.StaleDays/ArchiveDays`) before LLM consolidation; archived skills are purged 90d after `archived_at` (`CuratorConfig.PurgeDays`); only archives/purges skills recorded as agent-created in global-conduit scope (usage telemetry for any other skill never makes it eligible); skipped for the run when the pre-run snapshot fails; archives are versioned (`name.<timestamp>`), and purge removes only directories the ledger recorded as curator archives; promotes general project skills to global-conduit; `internal/memdir/curator.go`. Gap: thresholds are code defaults, no settings key |
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
| Personal agents (~/.claude/agents/*.md, not plugin-bundled) | ✅ | `plugins.DiscoverFSAgents` + `AgentRegistry.AddFS`; mirrors `internal/skills/fsloader.go` discovery order (`~/.conduit/agents` → `~/.claude/agents` → `<cwd>/.claude/agents`, symlink-aware); registered bare-name (no plugin prefix) |
| Plugin MCP server sync | ✅ | |
| Plugin output styles | ✅ | |
| Bundled skills (`/simplify`, `/remember`) | ✅ | |
| FS-based skill discovery (agentskills.io) | ✅ | `~/.conduit/skills/`, `~/.claude/skills/`, `<cwd>/.claude/skills/`; YAML frontmatter + `references/` support; `internal/skills/fsloader.go` |
| Custom user slash commands | ✅ | `~/.claude/commands/*.md` + `<cwd>/.claude/commands/*.md`; YAML frontmatter `description:`; `$ARGUMENTS` substitution; `internal/commands/custom.go` |
| Background memory/skill nudge prompts | ✅ | `bgreview` fires every 5/7 end_turns, hands the review a digest of the last 24 messages (24K chars, `transcript.Render`) in `<recent-conversation>` marked as data, runs it on the session context with a 5-minute timeout (`Reviewer.Wait()` at shutdown) and limits the memory review's Write to the memory directory (`bgreview.RestrictWrites`, symlink- and `..`-safe), routes the review agents through the background role (`backgroundTarget`, so a local provider never gets a Claude model); skill review is gap-driven (detects missing capability, creates skill if needed), scope-classified (project vs global-conduit), and hardened with "capture the fix not the failure" guardrails |
| SkillManage tool | ✅ | `internal/tools/skillmanagetool/`; create/view/update/patch/write_file/remove_file/list/promote/history/rollback; `patch` replaces one `old_string` (unique unless `replace_all`) or one markdown `section` and re-validates the result; `write_file`/`remove_file` manage `references/` only; hard validation rejects (`internal/skills/skilllint`: frontmatter with name+description, name ≤64 `[a-z0-9_-]`, description ≤1024, body present, SKILL.md ≤100k chars, support files ≤1 MiB) and advisory lint warnings (description length/marketing, oversized body, missing When to Use, incident-log shape, dangling `references/` links, forbidden files, references sprawl) are returned for findings the write introduced; project + global-conduit + global scopes (writes to an existing skill search project then global-conduit, error if both have it, and need `scope="global"` for `~/.claude/skills`); `WithAgentProvenance()` marks agent-created skills; telemetry hooks into `internal/skillusage/` |
| Skill usage telemetry | ✅ | `internal/skillusage/`; per-skill use/view/patch counts, provenance (agent vs user), lifecycle state, pinning; JSON store at `~/.conduit/skill-usage.json` |
| Skill lifecycle transitions | ✅ | deterministic active→stale (14d) →archived (30d) idle transitions, then archived→purged 90d after `archived_at`; pinned and user-authored skills exempt; runs in curator before LLM pass |
| Skill edit ledger | ✅ | `internal/skillusage/ledger.go`; JSONL at `~/.conduit/skill-ledger/ledger.jsonl` with content-addressed before/after blobs; every SkillManage write (create/update/patch/write_file/remove_file) records actor (`agent`/`curator`/`user`, curator via ctx), time, file and hashes; promote/archive/purge are logged as non-reversible entries; single-edit rollback (`SkillManage action=rollback`, `/skills revert <id>`) fails closed on missing/corrupt blob, unsafe path or a file changed since the edit, and is itself recorded; ledger bounded to 5 MB and blobs to 64 MB (oldest trimmed, orphan blobs GC'd after 1h); rollback check+write run under the ledger lock |
| Skill backups + rollback | ✅ | `internal/skillusage/backup.go`; tar.gz snapshot of `~/.conduit/skills/` + `~/.claude/skills/` (symlinks stored as links, never followed) before every curator mutation; prune to 5 most recent; rollback with pre-rollback snapshot |
| `/skills` subcommands | ✅ | `status`, `pin`, `unpin`, `backups`, `rollback` (whole-snapshot restore), `history [name]`, `revert <entry-id>` (single-edit undo) backed by `internal/skillusage/`; `internal/commands/skills.go` |

---

## Hooks

| Capability | Status | Notes |
|-----------|--------|-------|
| PreToolUse / PostToolUse / SessionStart / Stop | ✅ | |
| Shell, HTTP, prompt, agent hook types | ✅ | |
| Async hooks | ✅ | Non-blocking goroutine |
| Hook approve / block directives | ✅ | |
| PreToolUse `hookSpecificOutput` | ✅ | CC form: `permissionDecision` allow / deny / ask (+ reason) and `updatedInput`, which replaces the model's tool input before the permission check and execution (history keeps what the model sent). `ask` prompts even where rules allow; `allow` still honours deny rules, protected paths and explicit ask rules; a rewritten input missing required schema fields is denied like CC's schema failure. Also parsed from http and prompt/agent hook output. Divergence: schema validation is the required-field check only, and hooks run one after another (last `updatedInput` wins) rather than in parallel |
| Tool argument coercion | ✅ | `internal/tool/coerce.go`, applied before hooks and permissions: `"42"`/`"3.0"` to integer, numeric strings to number, `"true"`/`"false"` to boolean, JSON-encoded arrays/objects in strings parsed (also nested), a bare value wrapped for an array field, `"null"` for nullable fields. Schema-driven and only when the value fails its type; never mutates the input |
| Secret redaction in tool output | ✅ | `internal/rtk/redact.go` `RedactSecrets`, applied to every tool result before it reaches history and the UI (PostToolUse hooks see the raw text): AWS/GitHub/Anthropic/OpenAI/Slack/Stripe/Google keys, JWTs, private key blocks, URL credentials, `password=`/`api_key:` style assignments (identifier-like values skipped), as `[REDACTED:kind]`. Generic `password=`-style values need 12+ characters of mixed classes (or 24+ with two); `PWD`, path values under `secret*` keys and `sk-learn-…` style names are left alone; an unterminated private-key header masks only its base64 body lines. `Read` results are never redacted (edits must match disk), and Write/Edit/HashEdit/NotebookEdit/AstGrep reject input carrying a `[REDACTED:…]` placeholder the target file does not already contain. Files on disk are never touched. `conduit.json` `redactSecrets: false` turns it off. Gap: the CCR store keeps raw bash output, but `CCRRetrieve` results are redacted on the way out |
| `/btw` side question | ✅ | `/btw <question>` asks the main model with a copy of the conversation (mid-turn tail trimmed), the parent's tool definitions (so tool-using history is valid and the cache prefix matches) but an empty registry so nothing can run, 1024 max tokens, no thinking, no hooks, sent as a user-typed main request; the answer is a system row and nothing enters history. Usage is billed to the session ledger. `Loop.SideQuestion`, `internal/tui/btw.go` |
| Desktop notifications | ✅ | macOS osascript / Linux notify-send |
| TeammateIdle / TaskCreated / TaskCompleted | ✅ | Agent-teams hooks; `internal/hooks/hooks.go`; wired from GlobalStore callbacks and SpawnTeammate idle path |
| UserPromptSubmit | ✅ | CC semantics, `hooks.RunUserPromptSubmit`. Runs only for a prompt the caller names (`Loop.RunPrompt(ctx, UserPrompt{Raw, Text}, …)`: the TUI and `-p` pass the raw typed text, or the original `/cmd args`), after SessionStart and before the first request; plain `Run`, tool-result continuations, queued notifications, todo-continue nudges and child/side-question loops never fire it. Steering typed mid-turn and council / local-MCP prompts are screened in a command before they are sent (`Loop.ScreenPrompt`). Matcher and `async` ignored. Exit 0 plain stdout or `additionalContext` is appended as a `<system-reminder>` block (hidden when a session is resumed); exit 2, `{"decision":"block"}` or `{"continue":false}` drops the prompt, makes no request and returns `*agent.PromptBlockedError` (TUI restores the typed text and attachments, keeping any new draft below it; `-p` prints the reason and exits 1); `systemMessage` is a notice; `sessionTitle` renames the session. Other failures and timeouts fail open with a warning notice. `continue:false` from other events blocks them. Conduit extension: `hookSpecificOutput.updatedPrompt` (see COMPATIBILITY.md). Council and local-MCP routes apply `updatedPrompt` and append `additionalContext` (to the proposal, critique and synthesis prompts, never the recorded question, and to the text sent to the provider); a block leaves the typed text and attachments in the input |

---

## TUI & Clients

| Capability | Status | Notes |
|-----------|--------|-------|
| Bubble Tea v2 terminal TUI | ✅ | Primary client |
| Ultraviolet compositor (floating panels) | ✅ | No viewport shrink |
| Full GFM markdown rendering | ✅ | Tables, headings, task lists, strikethrough, blockquotes, italic |
| Syntax highlighting | ✅ | Hand-rolled; no Chroma dependency |
| Animated work indicator | ✅ | Gradient scramble row; its clock ticks only while a run is in flight, so an idle session does no per-frame work |
| Streaming cost display | ✅ | Live `$0.0042` as tokens stream; `workinganim.SetStatus()` |
| Collapsible tool outputs | ✅ | ▼/▶ indicators; space to toggle; persists per-message. Output of up to 11 lines is shown in full (a hint hiding one line saves nothing); first-line indentation is kept |
| Transcript render cache | ✅ | `rendercache.go`: rendered rows are memoized per message (keyed by content, width, verbose, expanded, theme generation, cwd), so a streamed token re-renders only the streaming tail. Not done: incremental markdown for one very long streaming message |
| Footer location off the render path | ✅ | cwd and git branch are read in a Cmd every 3s and cached; View never touches the disk |
| Clipboard | ✅ | `copyCmd`: terminal copy via `tea.SetClipboard`, native tool (pbcopy/wl-copy/xclip/clip) in a Cmd; the flash reports the real outcome. Selection copy joins soft-wrapped rows with a space instead of a newline (`softwrap.go`; falls back to newlines when a wrap cannot be inferred) |
| Bash command display | ✅ | Tool rows and the permission prompt show the full command, wrapped and highlighted, without a leading `cd <cwd> &&`; short commands stay inline in the row header |
| `!` shell mode | ✅ | `!cmd` runs through the Bash tool and the permission gate (once-only prompt), shows a Bash row, and appends `<bash-input>`/`<bash-stdout>` to the conversation. Divergence from Claude Code: the model is not queried automatically afterwards; settings `env` is not applied (plain process env) |
| Edit prompt in `$EDITOR` | ✅ | `ctrl+x` (`ctrl+g` is the agent strip) opens `$VISUAL`/`$EDITOR` on a temp file; a failed or missing editor leaves the draft alone |
| Persistent prompt history | ✅ | `~/.conduit/history/<project>.jsonl`, 200 entries, written off-loop; seeds up-arrow history at startup |
| Exit resume hint | ✅ | Prints `conduit --resume <id>` after the TUI exits when the session has content |
| Session model restore | 🔶 | The model is recorded as a `session_settings` entry and restored on in-session `/resume` (not on startup `--resume`, so `--model` keeps precedence); provider-routed sessions are not restored |
| Mouse capture setting | ✅ | `disableMouse: true` in `conduit.json` leaves the mouse to the terminal (native selection over tmux/ssh); wheel scroll and click-to-expand are unavailable then. `ctrl+home` / `ctrl+end` jump to top / bottom |
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
| Agent Teams strip + agent log panel | ✅ | `internal/tui/teampanes.go` (flat status strip, Ctrl+G toggle) + `internal/tui/subagentpanel.go` (Enter drills into per-agent log); `x` kills the selected running teammate via `team.Team.Cancel` without shutting down the rest of the team |
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
| JSON schema for config files | ✅ | `internal/settings/schema` (reflection walker, draft 2020-12) generates `schemas/{settings,conduit,mcp}.schema.json`; `conduit config schema [settings\|conduit\|mcp]` prints one, `make schema` regenerates, a drift test fails when the committed files are stale. Roots stay open (`additionalProperties: true`) so Claude Code keys pass; `$schema` is accepted and survives saves |

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
| Usage observability | ✅ | stderr logs on `message_delta` decode failure (turn token counts lost); OpenAI-compat streams that end without usage data are reported with `CLAUDE_GO_DEBUG_HTTP=1` (stderr output would corrupt the TUI) |
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
| C-O4.5: Runtime polish | Config JSON schema ✅ (`internal/settings/schema`, `conduit config schema`); background job tracking (✅ Bash `run_in_background` jobs: `internal/bgjobs`, `BashOutput`, `/bashes`); broader project instruction files (AGENTS.md, copilot-instructions, .cursor/rules ✅); prompt-submit hook ✅ (UserPromptSubmit) | ✅ | Plan §4.5; instruction files done in `internal/instructions`; files over 40000 bytes are cut on a UTF-8 boundary and the marker lists the dropped markdown headings (fence-aware, max 15) |
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
