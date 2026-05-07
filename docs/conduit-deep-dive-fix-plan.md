# Conduit Deep-Dive Audit — Remediation Plan

> **Destination:** This plan will be saved to `docs/conduit-deep-dive-fix-plan.md` (under the repo) and updated in-place — completed items checked off as work progresses. No items deferred; every finding ships.

## Progress checklist

### CRITICAL (user-felt + security)
- [x] **C1** — Auto-compact threshold uses output `max_tokens` (fix: model context window, TS-faithful threshold = `contextWindow − 20K reserve − 13K buffer`)
- [x] **C2** — Plugin MCP servers bypass trust gating
- [x] **C3** — Session title leaks `<summary>` tags
- [x] **C4** — Session list falls back to `session <id8>` + KB
- [x] **C5** — System prompt is a stubbed minimum (causes "fewer tool uses")
- [x] **C6** — Stream errors kill the loop instead of self-healing
- [x] **C7** — Silent auto-compact: emit a TUI event so user sees `⚡ Context compacted`

### HIGH
- [x] **H1** — MCP stdio scanner default 64 KB cap
- [x] **H2** — `Manager.CallTool` holds RLock for entire RPC
- [x] **H3** — MCP HTTP client has no timeout
- [x] **H4** — `/rewind` corrupts tool_use ↔ tool_result pairing

### MEDIUM
- [x] **M1** — AWS secret redaction is narrow
- [x] **M2** — Credential file mode mask wrong
- [x] **M3** — OAuth refresh client has no timeout
- [x] **M4** — `ExtraHeaders` can override identity headers
- [x] **M5** — ANSI strip incomplete
- [x] **M6** — Recorder writes 0644 + unredacted
- [x] **M7** — `parseRetryAfter` ignores HTTP-date form
- [x] **M8** — `copyDir` no zip-slip / symlink protection
- [x] **M9** — Bundled skills are nearly empty (only `simplify`, `remember`)
- [x] **M10** — Auto-compact runs twice per turn (de-dupe)

### LOW (no deferral — all in scope)
- [x] **L1** — SSE `data:` trailing whitespace strip diverges from spec
- [x] **L2** — `djb2Hash` uses Go runes not UTF-16 code units
- [x] **L3** — Premature SSE close treated as `end_turn`
- [x] **L4** — Hook HTTP client uses `http.DefaultClient` (redirects, no TLS hardening)
- [x] **L5** — `DefaultAsyncGroup` package-level mutable global
- [x] **L6** — Bash readonly classifier misses `find -fprint`, `sed s///e`
- [x] **L7** — Syntax highlighter is hand-rolled (docs claim Chroma)
- [x] **L8** — Notebook write 0644 → 0600
- [x] **L9** — `runPrint` `MaxTurns: 10` → 50 (parity with REPL)

### Verification gate
- [x] `make verify` clean (0 lint, race-clean, all tests pass) — verified 2026-05-07
- [x] STATUS.md and PARITY.md updated to reflect resolved divergences

---

## Context

User asked "deep dive this repo. Is there anything serious?" `make verify` is clean (0 lint, race-clean, 507+ passing tests). Three parallel Explore agents audited security, agent/API correctness, and TUI/plugin/RTK surfaces. The findings below are spot-checked against the code; STATUS.md stubs are excluded.

**Headline:** No exploitable critical security holes. Several **functional regressions vs TS Claude Code** that the user is feeling directly: aggressive auto-compact, weak system prompt → fewer tool uses, stream errors kill the loop instead of self-healing, session titles getting clobbered by compact-summary tags. Plus MCP transport bugs that wedge the agent and a handful of mediums around plugin-trust scope, secret redaction breadth, and recorder hygiene.

User-reported symptoms map to specific code paths:

- "Sessions sometimes save with `<summary>` titles, sometimes a number+KB with no messages" → C3, C4 (extras.go + compact.go).
- "Conduit uses fewer tools/skills than Claude Code" → C5 (system prompt is a deliberately-stubbed minimum), M9 (only 2 bundled skills), M10 (`MaxTurns: 10` in print mode).
- "Tool error killed the thinking loop instead of self-healing" → C6 (stream errors `return` from loop instead of feeding error back to model).

---

## CRITICAL — fix first

### C1. Auto-compact threshold uses output `max_tokens`, not the context window

- `internal/agent/loop.go:391-401, 430-440`
- `internal/model/model.go:50` defines `MaxTokens = 16000` (the **request `max_tokens`** output cap).
- `cmd/conduit/mainopts.go:55` and `mainrepl.go:455` wire `cfg.MaxTokens = internalmodel.MaxTokens`.
- The compact check is `if inputTokens > int(float64(l.cfg.MaxTokens) * 0.8)` → fires whenever `inputTokens > 12,800`.
- **TS reference (`/Volumes/Engineering/Icehunter/claude-code/src/services/compact/autoCompact.ts:32-91` + `src/utils/context.ts:9,51`):**
  ```
  contextWindow = MODEL_CONTEXT_WINDOW_DEFAULT = 200_000   // 1_000_000 for [1m] models / sonnet-4 / opus-4-6 with 1m beta
  reservedForSummary = min(getMaxOutputTokensForModel(model), 20_000)
  effectiveContextWindow = contextWindow − reservedForSummary
  AUTOCOMPACT_BUFFER_TOKENS = 13_000
  threshold = effectiveContextWindow − AUTOCOMPACT_BUFFER_TOKENS
  ```
  For 200K models: `200_000 − 20_000 − 13_000 = 167_000`. Conduit fires at 12,800 — 13× too aggressive.

**Fix:**
1. Add to `internal/model/model.go`:
   ```go
   const ContextWindowDefault = 200_000
   const ContextWindow1M     = 1_000_000
   const CompactReserveTokens = 20_000   // matches MAX_OUTPUT_TOKENS_FOR_SUMMARY
   const CompactBufferTokens  =  13_000  // matches AUTOCOMPACT_BUFFER_TOKENS
   func ContextWindowFor(model string) int { /* honors [1m] / sonnet-4 / opus-4-6 */ }
   func AutoCompactThresholdFor(model string) int { return ContextWindowFor(model) − CompactReserveTokens − CompactBufferTokens }
   ```
2. In `internal/agent/loop.go:391, 430`, replace `int(float64(l.cfg.MaxTokens) * 0.8)` with `model.AutoCompactThresholdFor(l.ActiveModel())`.
3. Honor `CLAUDE_CODE_AUTO_COMPACT_WINDOW` env override (TS line 40-46) and `DISABLE_AUTO_COMPACT` (TS line 152) for parity.
4. Add a circuit breaker mirroring `MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES = 3` (TS line 70) so a stuck `prompt_too_long` doesn't loop.
5. Keep `MaxTokens` purely for the request body — do NOT reuse it for thresholds anywhere else.

### C2. Plugin MCP servers register without trust gating

- `internal/mcp/config.go:99,135` — `loadPluginMCPServers(merged)` is called unconditionally from `LoadMergedServers`.
- Hooks are correctly trust-gated via `settings.FilterUntrustedHooks` (`internal/settings/settings.go:360`), but plugin MCP server registration is not. Cloning an untrusted repo with an enabled plugin manifest can spawn arbitrary stdio MCP processes (`command`/`args`) on first session.
- STATUS.md M12 implies parity here; reality is asymmetric.

**Fix:** add `trusted bool` + `cwd string` to `loadPluginMCPServers`, mirror the hooks filter (skip project-local plugins when untrusted). Plumb trust state through `LoadMergedServers` from the same call site that already computes it for hooks.

### C3. Session titles leak literal `<summary>...</summary>` from compact-injected history

- `internal/compact/compact.go:124-130` — after a compaction the new history is reinjected with `Text: "<summary>\n" + summary + "\n</summary>\n\nAbove is a summary..."`.
- `cmd/conduit/mainrepl.go:532-535` — `OnCompact → SetSummary` persists the summary to the JSONL, and the post-compact synthetic *user* message also gets persisted on the next turn end.
- On `/resume`, `internal/session/extras.go:ExtractTitle` walks for the first user `text` block; that block can be the synthetic one starting with `<summary>`. `titleFromText` (`extras.go:135-164`) does no XML strip → picker shows `<summary> contents…`.
- `extractSummary` (`compact.go:118-122`) silently falls back to the entire raw model response if `<summary>` tags are absent — so sometimes the title is just blob-of-summary text instead.

**Fix:** in `internal/session/extras.go:titleFromText`, detect a leading `<summary>` and trim through `</summary>` (and `<title>`, `<analysis>`, generic XML wrappers) before truncation. Also in `compact.go:118-122`, refuse to fall back to the whole response — return empty summary and let the caller skip persistence rather than dump the raw text.

### C4. Sessions show `session <id8> · N records · M KB` (the "number + KB, no messages" symptom)

- `internal/commands/session.go:730-732`:
  ```go
  title := session.ExtractTitle(s.FilePath)
  if title == "" { title = "session " + s.ID[:min(8, len(s.ID))] }
  ```
- `internal/tui/resumepanel.go:158-165` appends size/records.
- `ExtractTitle` (`extras.go:60-106`) returns `""` when:
  - First user message has no `text` block (image-only, attachment-only, slash-command).
  - JSONL contains only metadata entries (`session_settings`, `cost`, `summary`, `tag`, `file-access`) — happens when the user crashed/quit before the first user message was flushed. `Append` (`session.go:175-191`) does `OpenFile/Fprintf/Close` with no fsync, so a partial last line is silently dropped by `extras.go:79`.
  - `entryAPIMessage` (`sessionload.go:166-186`) skips entries whose role/type doesn't match its allowlist.

**Fix:** broaden `ExtractTitle` to also consider:
- `summary`-typed entries (`entry.Summary`) when present.
- The most recent assistant `text` block as a secondary fallback.
- For sessions with only metadata, return a date/cwd-based fallback instead of letting the caller print `session <id8>`.

Also — separately tracked but not blocking — add an `fsync` after append on session close to reduce mid-write loss.

### C5. System prompt is a deliberately-stubbed minimum (~1.5 KB vs TS ~10 KB)

- `internal/agent/systemprompt.go:31-72` — comment in code admits *"Trimmed copy… long enough to look like the real thing but short enough that we don't ship Anthropic's full IP. Real prompt is ~10 KB; ours is intentionally minimal."*
- The minimum is missing the entire **Tool usage policy**: parallel-tool-call encouragement, named-tool playbooks (Task for non-trivial searches, TodoWrite for multi-step plans, Skill matching), proactive-skill-use language.
- `MinimalOutputGuidance` (`systemprompt.go:67-72`) further pushes the model toward brevity ("simple question gets a direct answer, not headers and sections") — discouraging multi-step tool use.
- PARITY.md M-A and "system prompt assembly" both list ✅; STATUS.md does too. **Documentation drift** — the code self-narrates partial.

This is the **direct cause of the user's "conduit uses fewer tools/skills than Claude Code"** symptom.

**Fix:** expand `MinimalAgentSystemPrompt` with an explicit Tool-usage section that mirrors the TS prompt's structure without copying its prose verbatim. Keep it conduit-authored (no IP exposure), but cover: parallel tool calls when independent, when to use Task/Grep/Glob/SkillTool/TodoWrite, error-recovery guidance, and "prefer doing over asking." Update STATUS.md/PARITY.md to mark this as 🔶 with explicit notes.

### C6. Stream/HTTP errors mid-turn kill the loop instead of self-healing

- `internal/agent/loop.go:352-355` — if `client.StreamMessage` errors → `return msgs, fmt.Errorf("agent: stream: %w", err)`.
- `internal/agent/loop.go:366-385` — if `drainStream` errors → emit `EventPartial` then `return`.
- The tool-execution layer is fine: `internal/agent/looptools.go:180,204,234-239` correctly converts Go errors to `tool.Result{IsError: true}` and feeds them back to the model. The kill-path is one level up: a transient stream failure (overloaded_error, network blip, SSE truncation, retry-after > 2 min) ends the entire loop with no synthetic recovery message.
- **This is the user's "tool error killed the thinking loop instead of self-healing."** Even though the tool itself didn't fail, a streaming hiccup during the tool-use turn ended things.
- Foot-gun: `executeTools` returns `(_, error)` at `loop.go:429` even though `looptools.go:39 //nolint:unparam` confirms the error is always nil today; the kill-on-error branch at `loop.go:429-432` is dead but waits to be triggered by future code.

**Fix:**
1. In `loop.go` around lines 352-355 and 366-385, on non-cancellation stream errors: synthesize an in-history note (assistant + user with `tool_result{isError: true}` style or system-style message describing the failure), increment a per-loop retry counter, and `continue` instead of `return`. Cap consecutive failures at 3 before giving up. Surface a new `EventAPIRetry` event so the TUI shows "model stream failed, retrying" instead of silently exiting.
2. Drop the unused `error` return from `executeTools` (`looptools.go:39`) and the dead branch at `loop.go:429-432`.

---

## HIGH — wedges/leaks under realistic conditions

### H1. MCP stdio uses default `bufio.Scanner` (64 KB line cap)

- `internal/mcp/client.go:80` — `bufio.NewScanner(stdoutPipe)` with no `s.Buffer(...)`.
- Real MCP `tools/list` responses with rich JSON schemas (e.g., Playwright, Atlassian) and `tools/call` results routinely exceed 64 KB. On overflow `Scan()` returns false with `bufio.ErrTooLong`, `readLoop` exits, `done` closes, every in-flight call returns "server exited", and the connection is dead.
- Compare `internal/sse/parser.go:50` which sets 8 MiB explicitly.

**Fix:** `c.stdout.Buffer(make([]byte, 0, 64<<10), 8<<20)` (or higher) before starting `readLoop`. Same change in `internal/mcp/client.go:389` for the SSE/HTTP MCP transport.

### H2. `Manager.CallTool` holds the manager-wide `RLock` for the entire RPC

- `internal/mcp/manager.go:408-422` — `RLock()` taken with `defer`, then `srv.client.CallTool(ctx, ...)` invoked under the lock.
- Any in-flight tool call (potentially tens of seconds for an MCP that runs builds/queries) blocks `DisconnectServer`/`Reconnect`/`SyncPluginServers` (write lock). User can't disconnect a hanging MCP from `/mcp`.

**Fix:** snapshot the matched `*serverEntry` under the RLock, release the lock, then invoke `CallTool` outside the critical section.

### H3. MCP HTTP client has no timeout

- `internal/mcp/client.go:277` — `http: &http.Client{}` (no `Timeout`).
- A misbehaving HTTP MCP can hang JSON-RPC calls indefinitely. Per-request `context.WithTimeout` is not consistently applied at all call sites.

**Fix:** `http: &http.Client{Timeout: 60 * time.Second}` (matching `internal/mcp/oauth.go:95`). Keep per-call ctx for cancellation.

### H4. `/rewind` corrupts tool_use ↔ tool_result pairing

- `internal/tui/commandresultshandlers.go:662-682` — strips last `(user, assistant)` pair per rewind step.
- When the last turn included tool_use, in-memory history is `… user, assistant(tool_use), user(tool_result), assistant`. Stripping two messages leaves an orphaned `assistant(tool_use)` and the next API call rejects with "tool_use without tool_result." JSONL loader (`sessionload.go:30`) runs `FilterUnresolvedToolUses`; rewind path does not.
- Side bug: `run.go:230` does not truncate the JSONL → `/resume` after `/rewind` reverts everything.

**Fix:** route rewind through `FilterUnresolvedToolUses` after slicing; truncate the on-disk JSONL transcript to match in-memory length.

---

## MEDIUM — secret-handling breadth and config footguns

### M1. AWS secret redaction is narrow
`internal/rtk/filterssystems.go:168` matches only `AKIA[0-9A-Z]{16}` and `aws_secret_access_key=...`. Misses STS `ASIA...`, `aws_session_token`, JSON-shaped output of `aws sts get-session-token` (`"SecretAccessKey":"..."`, `"SessionToken":"..."`). STATUS.md "secret redaction included" is partial. **Fix:** add ASIA token regex, `aws_session_token=`, JSON keys (`SecretAccessKey`, `SessionToken`, `aws_secret_access_key`).

### M2. Credential file permission mask
`internal/secure/filestorage.go:69` — `mode&0o022` rejects only group/world *writable* files. Comment claims it also rejects world-readable. A 0o644 credentials file passes. **Fix:** tighten to `mode&0o077 != 0` so group/world readability fails the check.

### M3. OAuth refresh client has no timeout
`internal/auth/token.go:72` falls back to `http.DefaultClient`. `EnsureFresh` is called from the agent's 401 retry path → wedge if `platform.claude.com` TCP-connects but stalls. **Fix:** dedicated `*http.Client` with 30s timeout, mirror `internal/mcp/oauth.go:95`.

### M4. `ExtraHeaders` can override required identity headers
`internal/api/client.go:215-217` applies `ExtraHeaders` last → can clobber `User-Agent`, `x-app`, `anthropic-beta`, `Authorization`, Stainless headers. CLAUDE.md emphasizes byte-exact CLI fingerprint. **Fix:** deny-list locked keys when merging.

### M5. ANSI strip incomplete (RTK redaction bypass surface)
`internal/rtk/ansi.go:10` regex covers a CSI subset and OSC-BEL. Misses DCS (`\x1b P …`), APC, PM, OSC-ST (`\x1b\\`), C1 single-byte forms (0x9b, 0x9d), single-char ESC. Output downstream-rendered in a terminal can carry secrets/sequences past redaction. **Fix:** broaden the regex to cover all C1 controls and string-terminator forms.

### M6. Recorder permissions + scope
`internal/recorder/recorder.go:65,113` writes `~/.claude/recordings/<ts>.cast` at default 0644 *and* substitutes `os.Stdout` globally — every byte (including pre-redaction tool output, prompts) lands verbatim. **Fix:** create with 0600; document that recordings are unsanitized; consider redaction filter on the writer.

### M7. `parseRetryAfter` ignores HTTP-date form
`internal/api/retry.go:89-99` only `ParseFloat`. CDN / spec-compliant servers may send `Retry-After: <HTTP-date>` and we silently fall back to exponential backoff. **Fix:** also try `http.ParseTime`.

### M8. `copyDir` lacks zip-slip / symlink protection
`internal/plugins/install.go:361` is used during marketplace plugin install on attacker-controllable git contents. No `filepath.Rel` containment check, no `Lstat` before recurse → following symlinks can read outside source tree. Lower severity on Unix because `filepath.Join` cleans `..`, but symlinks still escape. **Fix:** `Lstat` and skip symlinks; verify `dst` containment with `filepath.Rel`.

### M9. Bundled skills are nearly empty
`internal/skills/bundled.go:9-22` ships only `simplify` and `remember`. Real Claude Code ships dozens. The `# Available skills` reminder block (`systemprompt.go:78-98`) is therefore tiny on a vanilla install, so the model rarely sees a skill that matches a request. Compounds C5. **Fix:** audit the TS bundled-skills set and port the missing entries (or document this as an intentional minimum and provide an installer).

### M10. Auto-compact runs twice per turn
`internal/agent/loop.go:391-401` (pre-tool-use stop check) and `loop.go:430-440` (post-tool-results) both invoke `compact.CompactWithModel`. The second call also triggers when the loop is iterating tool calls — interrupting a multi-step tool chain mid-flow with a Haiku summary. Combines with C1 to make the "fewer tool uses" symptom worse. **Fix:** drop the duplicate at lines 391-401; only compact at end_turn boundaries (or after tool_results when actually approaching the threshold).

### C7. Silent auto-compact (paired with C1/M10)
Today, when compaction fires, only `OnCompact(summary)` is called (`loop.go:396-398, 435-437`); the user has no visible signal that the conversation just got summarized. With C1 fixed (compaction will now legitimately fire near 167K tokens), users still need to know. **Fix:** add a TUI event (`EventCompacted{Tokens, ThresholdPct}`) emitted from the loop right before `compact.CompactWithModel` runs, render in the live row as `⚡ Context compacted (N% of window).` Also surface the post-compact recovery summary as a single-line system message in the chat. Update `internal/agent/loop.go` and `internal/tui/` event handlers.

---

## LOW — fixed in this pass (no deferral)

- **L1.** `internal/sse/parser.go:162` trims trailing whitespace from `data:` payload (spec only strips one leading space). Harmless on Anthropic JSON; flag for fidelity.
- **L2.** `internal/session/sessionlist.go:152-158` `djb2Hash` iterates Go runes, TS uses UTF-16 code units. Non-BMP cwd paths produce different sanitized dirs and break Claude Code → Conduit legacy import.
- **L3.** `internal/agent/loopstream.go:37-44` premature SSE close after `message_start` returns clean `io.EOF` and is treated as `end_turn` with empty content. Should distinguish.
- **L4.** `internal/hooks/http.go:42` uses `http.DefaultClient` — follows redirects, no TLS hardening; tool inputs (file content, bash commands) can leak via HTTP-hook redirect. Use a dedicated client with `CheckRedirect`.
- **L5.** `internal/hooks/async.go:56` `DefaultAsyncGroup` is a package-level mutable global with no mutex. Single-process today; future second concurrent session would race.
- **L6.** `internal/permissions/permissions.go:441-443` Bash readonly classifier treats `find -fprint`, GNU `sed s///e` as read-only — both can write/exec. Tighten flag set.
- **L7.** `internal/tui/syntaxhighlight.go:152` highlighting is hand-rolled; STATUS.md and PARITY.md claim "Chroma-based." Doc drift only; runtime is fine.
- **L8.** `internal/tools/notebookedittool/notebookedittool.go:246` writes notebook 0644; cell outputs can contain secrets — tighten to 0600.
- **L9.** `cmd/conduit/mainopts.go:58` print-mode caps `MaxTurns: 10` vs REPL's 50 (`mainrepl.go:457`). One-shot agentic tasks hit the cap fast. Bring to parity at 50.

---

## Execution order

User directive: ship every fix, no deferrals. Sequence chosen to maximize early signal for the user-felt symptoms while keeping changes reviewable per commit.

1. **Pass 1 — user-felt fidelity:** C1, C5, C6, C7, M10, C3, C4.
2. **Pass 2 — wedges:** C2, H1, H2, H3, H4.
3. **Pass 3 — secrets/security mediums:** M1, M2, M3, M4, M5, M6, M7, M8.
4. **Pass 4 — coverage gaps and lows:** M9 (skill bundling), L1, L2, L3, L4, L5, L6, L7, L8, L9.
5. **Pass 5 — verification + docs:** `make verify` clean, STATUS.md / PARITY.md updated, every checkbox above ticked.

After each pass, run `make verify` and tick the corresponding boxes in this file.

## Critical files

- `internal/agent/loop.go` (352-355, 366-385, 391-440, 429-432), `internal/agent/looptools.go` (39, 180, 204, 234-239), `internal/agent/systemprompt.go` (31-98), `internal/model/model.go` (50), `cmd/conduit/mainopts.go` (55, 58), `cmd/conduit/mainrepl.go` (455, 457, 532-535)
- `internal/compact/compact.go` (118-130)
- `internal/session/extras.go` (60-106, 135-164), `internal/session/sessionload.go` (89-91, 166-186), `internal/session/session.go` (175-191, 226, 240-242), `internal/commands/session.go` (725-740), `internal/tui/resumepanel.go` (158-165)
- `internal/skills/bundled.go` (9-22)
- `internal/mcp/client.go` (80, 277, 389), `internal/mcp/manager.go` (408-422), `internal/mcp/config.go` (99, 135)
- `internal/tui/commandresultshandlers.go` (662-682), `internal/session/run.go` (230)
- `internal/secure/filestorage.go` (69), `internal/auth/token.go` (72), `internal/recorder/recorder.go` (65, 113)
- `internal/rtk/filterssystems.go` (168), `internal/rtk/ansi.go` (10)
- `internal/api/client.go` (215-217), `internal/api/retry.go` (89-99)
- `internal/plugins/install.go` (361)

## Verification

For each fix:
1. Add a unit test capturing the bug:
   - **C1**: agent loop test asserting no compaction at 13K input tokens with a 200K context window; compaction fires at 161K.
   - **C3**: extras test asserting `titleFromText("<summary>foo</summary>")` returns `"foo"` (or skips the wrapper).
   - **C4**: session-list test for an empty/metadata-only JSONL → fallback title is non-empty and non-`session <id8>`.
   - **C5**: snapshot test of the assembled system prompt; assert it contains the new tool-usage section.
   - **C6**: agent-loop test that injects a transient stream error mid-tool-use and asserts the loop continues with a synthetic recovery message instead of returning.
   - **H1**: integration test against a stub stdio MCP server emitting a >64 KB `tools/list` response.
   - **H2**: race test that calls `Manager.CallTool` with a slow stub server and asserts `Disconnect`/`Reconnect` are not blocked.
2. `make verify` — must remain race-clean, 0 lint.
3. Manual: launch conduit, exercise `/resume` after compact, after image-only first turn, after a normal session — verify titles look right.
4. Manual: simulate a stream cut mid-tool-use (e.g., kill a forwarded TCP connection) and confirm the loop self-heals.
5. Update STATUS.md and PARITY.md to reflect resolved issues. Critically:
   - Mark `system prompt assembly` as 🔶 (currently ✅ but stubbed) before the C5 fix; flip to ✅ when the tool-usage section ships.
   - Add a "Known divergences" entry for the auto-compact bug being fixed (C1).

## What's NOT serious

- BashTool, FileRead/Write/Edit, REPL, Worktree, WebFetch (SSRF dialer), permissions gate call sites, hook timeouts, async hook lifecycle, sub-agent isolation, parallel tool pool, FilterUntrustedHooks call sites, TLS config, .goreleaser pipeline secret hygiene — all clean.
- No `panic()` in production paths. No `InsecureSkipVerify` anywhere. No process-wide goroutine leaks observed.
