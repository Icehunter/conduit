# Upstream sync — 2026-10-06

Reference repos reviewed for changes since the last pull, checked against conduit's code.
Next sync: diff from the "Synced to" column.

| Repo | Last synced | Synced to | Commits |
|------|-------------|-----------|---------|
| `../opencode` (dev) | `38e10eb140` (2026-08-08) | `ecc4916b5a` (2026-10-06) | 501 |
| `../crush` (upstream/main) | `75791b8883` (2026-08-08) | `140e8cb970` (2026-10-06) | 282 |
| `../hermes-agent` (main) | `3e6a081d60` (2026-08-08) | `72a346e8ea` (2026-10-07) | ~27k non-merge |
| `../oh-my-pi` (main) | `7732c9b158` (2026-06-13) | `d700137b96` (2026-10-07) | ~19k |

Claude Code wire sync to 2.1.292 is logged in `COMPATIBILITY.md`.

Note for oh-my-pi: upstream commit `1949c798a7` (2026-08-23) deleted most of the
`CHANGELOG.md` history, so a CHANGELOG diff alone misses most of a range that spans it;
read the changelogs at `1949c798a7^` too.

Legend: **[V]** = reproduced or confirmed in conduit's code by a second check, not only by
the agent that found it. Sources: OC = opencode, CR = crush, HM = hermes-agent,
OMP = oh-my-pi. Effort is a rough estimate in hours.

## Status (2026-10-08)

Implemented (uncommitted, reviewed): B1–B5, E1 (OpenAI reasoning), E2 (retry classification
and backoff, reactive compaction), E3 (background review sees the conversation), E4 (MCP
background connect and tool sync), E5 (compaction quality: verbatim tail, anchors, local
fallback, 256K trigger cap). Also fixed, found during the work: the system-prompt filter dropping
the agent prompt for OpenAI-compatible models; system blocks (plan mode, sub-agent prompts)
wiped every Run; repo self-approval of `.mcp.json` servers; deny rules skipped for trusted roots;
acceptEdits writes outside cwd; protected paths (`.git`, shell rc, conduit config). Collapsed
thinking row added.

Batch 2 (also implemented, reviewed): Codex session/cache key, encrypted reasoning replay, cached
tokens, 272K cap, live model list; Copilot Business/Enterprise endpoints and supported_endpoints
routing; images/documents on OpenAI transports; response-header timeout; Responses reasoning
summaries; read-before-write guard; HashEdit ranges/inserts/BOM/CRLF and weak-model Edit fallback;
tool-argument coercion; hook updatedInput/permissionDecision; `/btw`; TTSR tool-arg/path/agent
rules, warn mode, repetition guard; per-sub-agent REPL kernels; secret redaction in tool output;
TUI render cache, idle spinner, clipboard, resume hint, ctrl+home/end, mouse setting, full bash
display, `$EDITOR` (ctrl+x), persistent input history, model restore on resume, `!` shell mode;
curator 14/30-day lifecycle with ledger-tracked purge, skill edit ledger with rollback,
SkillManage patch/references/lint, session_search date and session filters, UTF-8-safe
instruction truncation. Batch 3 (also implemented, reviewed): keyless web search (Parallel, DuckDuckGo, Mojeek) and
Codex-subscription search, gated by `webSearch.keyless: auto|always|never` (auto = only when no
other provider is configured); opt-in in-text tool calling (`toolCallFormat: hermes|qwen3|auto`);
Grok/xAI subscription OAuth (experimental until tested against a real account); Task
`isolation: "worktree"` with locked conduit-owned worktrees and a conservative stale sweep.
With batch 3, every item listed in this document is implemented.

## Bugs found in conduit (fix first)

| # | Issue | Where | Source | Effort |
|---|-------|-------|--------|--------|
| B1 | **Read-only Bash auto-approve is bypassable [V].** `shellsafe.IsReadOnly` returns true for `env rm -rf …`, `git branch -D main`, `sort -o /etc/hosts`, `fd -x rm`, `git stash drop` (also reported: `awk 'BEGIN{system()}'`, `git config --global`, `rg --pre`, `yq -i`, `npm audit fix`), so default mode runs them without a prompt. Model: crush `945c518f` flag policies on the same `sh/syntax` AST (crush later reverted it in `d73a6e42` without a stated reason). | `internal/shellsafe/shellsafe.go`, `internal/permissions/permissions.go:196` | CR | 3-4 |
| B2 | **Copilot bills every tool-loop request as a user turn [V].** `x-initiator: user` is hard-coded; upstream sends `agent` for tool follow-ups and sub-agents, adds `X-Interaction-Id`, and moved to API version `2026-06-01`. | `internal/provider/copilot/copilot.go:92-113` | OC | 2-3 |
| B3 | **Auto-compact is hard-wired to Haiku on the main client [V].** `compact.DefaultModel` (`claude-haiku-4-5`) is sent through `l.client`, so with an OpenAI-compatible/Gemini/local main provider it fails 3 times and auto-compact switches off. STATUS.md says it uses the active role's client/model; manual `/compact` does. | `internal/agent/loop.go` (auto-compact block) | HM | 1-2 |
| B4 | **Bash kills commands at 120 KB of output and keeps only the head [V].** `AtomicLimitWriter` at 4×30 KB cancels the process, so a long `make test` is SIGKILLed; the 30 KB head-only cut also runs before the `internal/truncate` spill. Keep head + tail and spill the full output instead. | `internal/tools/bashtool/bashtool.go:166-176, 229-246` | CR (`b8e93d11`, `10ec02a5`) | 1-2 |
| B5 | **Stream endings aren't checked [V: loop never branches on `stop_reason`].** `max_tokens` cut-offs are treated as finished, a tool call cut mid-JSON runs with broken input, empty replies end the turn; OpenAI transports emit `message_stop` even without `[DONE]`/`finish_reason`, and mid-stream `{"error":…}` chunks and `content_filter` become `end_turn`. | `internal/agent/loop.go`, `internal/api/openaicompat.go`, `internal/api/openairesponses.go` | HM, OMP, OC | 6-8 |

## Enhance what conduit already ported

| # | Item | Where | Source | Effort |
|---|------|-------|--------|--------|
| E1 | Reasoning on OpenAI-compatible providers: send `reasoning_effort`, read and replay `reasoning_content` (keeps llama.cpp's KV cache warm; Qwen `preserve_thinking`), split `<think>` tags into thinking blocks. Fits the in-progress `ExtraBody` / local-provider work. | `internal/api/openaicompat.go` | OC, CR, OMP | 4-6 |
| E2 | Retry classification: don't resend permanent 400/401/403 three times; backoff for 5xx; honor `retry-after-ms`; retry connection errors; full compaction on context-overflow 400/413; failover on 403/5xx/model-not-found using reset headers instead of a fixed 5-minute cooldown. | `internal/agent/loop.go`, `internal/api/retry.go` | OC, HM, OMP | 6-8 |
| E3 | Background memory/skill review agents never see the conversation (`OnEndTurn` passes no messages), so "review this session" reviews nothing. Upstream passes a cache-sharing copy plus a digest of recent messages. | `internal/bgreview`, `cmd/conduit/mainrepl.go` | HM | 4-6 |
| E4 | MCP: `ConnectAll` blocks startup; tools are registered once, so a server that connects after OAuth or `/mcp reconnect` never exposes its tools (the `RefreshMcpTools` reason string in `scripts/wire-check/verify.mjs` claims otherwise). | `internal/mcp/manager.go`, `internal/tool/registry.go` | CR | 4-6 |
| E5 | Compaction quality: keep a verbatim tail (10-25K tokens, ≤20% of window), quote user messages, index exact identifiers (paths, SHAs, errors), keep tool-call arguments, local fallback summary on failure; cap the trigger at ~256K on 1M-token models. | `internal/compact/compact.go`, `internal/model/model.go` | HM, OC | 8-12 |

More, lower priority:

- Codex: empty session id, no `prompt_cache_key`, effort hard-coded to `medium` (ignores `/effort`), `gpt-5*-pro` allowed, 272K input limit vs advertised 400K, cached tokens not reported, static model list (OC, OMP, CR).
- Copilot Business/Enterprise `endpoints.api` ignored; routing by model name instead of `supported_endpoints` (OMP, CR).
- OpenAI transports drop image/document blocks and empty tool results (400 from OpenAI) (OC, CR).
- HashEdit: plain-replace fallback for models with weak edit grammar; richer ops (ranges, insert, block); strip BOM before hashing (OMP).
- Per-sub-agent REPL kernels (subagents appear to share the parent's interpreter) (OMP).
- TTSR rules on tool args/paths/agents, warn mode (OMP).
- TUI: spinner ticks at 20 Hz while idle; every streamed token re-renders the transcript; clipboard ignores errors and writes raw OSC 52 in `Update` (CR).
- Curator/skills: 14/30-day thresholds, archive purge, exclusive lock, edit ledger with rollback, SkillManage patch + lint (HM).
- Response-header timeout (5 min) before the idle timer starts (OC).
- Truncated-output files written 0644 and follow symlinks (`truncate.go:208`, `ccr.go:88`) (HM).

## New features worth adding

| # | Feature | Source | Effort |
|---|---------|--------|--------|
| N1 | Read-before-write guard for file tools (CC forbids overwriting an unread or changed file) | HM | 5-7 |
| N2 | Coerce tool arguments to the schema (`"42"`, `"true"` from Qwen/Gemini/OpenRouter) | HM | 2-3 |
| N3 | `/btw` side questions | HM, OMP | 3-6 |
| N4 | Print `conduit --resume <id>` on exit; ctrl+home/end; setting to turn off mouse capture | CR | 2 total |
| N5 | Hook `updatedInput` (hooks rewriting tool arguments; also CC parity) | OMP | 2-3 |

Also seen: full bash command display without the `cd … &&` prefix (CR), `$EDITOR` prompt editing (CR), restore model/provider on resume (CR), persistent input history (CR), `!` shell mode (CR), mid-stream repetition detection (OMP, HM), secret redaction in tool output (OMP, HM), keyless web search for non-Anthropic users (OMP), worktree isolation for Task sub-agents (HM), tool calling for models without native tool support (OMP), Grok/xAI OAuth (CR).

## Dependencies

Crush's charm stack (bubbletea v2.0.9, lipgloss v2.0.6, bubbles v2.2.1, x/ansi v0.11.8, sh/v3 v3.14.1) matches conduit; conduit's ultraviolet is newer. No bumps needed.

## Doc drift noticed

- `docs/tree-rendering.md` refers to `crushTreeEnumerator()`, which no longer exists.
- `CLAUDE.md` says Go 1.26; `go.mod` says 1.27.1.
