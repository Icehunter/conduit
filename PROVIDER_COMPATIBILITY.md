# Provider Account Compatibility

Last updated: 2026-10-07

This document tracks product-account provider wire contracts: GitHub Copilot,
ChatGPT / Codex and Grok / xAI. Claude subscription compatibility remains in
`COMPATIBILITY.md`.

## Checks

Run the provider-account guardrails with:

```bash
make wire
```

The default mode checks Conduit's implementation and uses local reference
checkouts when present. Strict mode requires the active OpenCode and Crush
reference checkouts:

```bash
make wire-strict
```

Reference paths default to sibling checkouts and can be overridden:

```bash
OPENCODE_REPO=../opencode CRUSH_REPO=../crush make wire-strict
```

Claude keeps its deeper decoded-bundle fingerprint check:

```bash
make wire-claude
```

To run all provider-account checks plus the Claude check:

```bash
make wire-all
```

## GitHub Copilot

Reference sources:

- `../opencode/packages/opencode/src/provider/sdk/copilot`
- `../opencode/packages/opencode/src/plugin/github-copilot/models.ts`
- `../opencode/packages/llm/src/providers/github-copilot.ts`
- `../crush` Copilot provider material, when available

Conduit owner files:

- `internal/provider/copilot/copilot.go`
- `internal/app/auth.go`
- `internal/provider/copilot/copilot_test.go`

Wire contract:

- GitHub device-code auth starts at
  `https://github.com/login/device/code`.
- GitHub OAuth token exchange uses
  `https://github.com/login/oauth/access_token`.
- Copilot API token exchange uses
  `https://api.github.com/copilot_internal/v2/token`.
- Model discovery uses `https://api.githubcopilot.com/models`.
- Business and Enterprise seats: the token exchange names the seat's API origin
  in `endpoints.api`. It is stored in the credential metadata
  (`api_endpoint`) only when it is an `https` host under `githubcopilot.com`
  or `ghe.com`, because the bearer token is sent there. Model discovery and
  the chat, Responses and Messages routes use it in place of
  `https://api.githubcopilot.com`; a base URL the user configured is kept.
- A 403 from the token exchange whose body is about a model's policy or
  enablement (`model_not_supported`, "policy") returns `copilot.ErrModelPolicy`
  and keeps the credential. Only a plain "no Copilot on this account" 403
  deletes it.
- Static runtime headers on every route: `Editor-Version`,
  `Copilot-Integration-Id`, `Editor-Plugin-Version`, `OpenAI-Intent`, and
  `X-GitHub-Api-Version: 2023-07-07`. OpenCode moved to `2026-06-01` with
  token-based billing; conduit stays on `2023-07-07` until that auth flow can
  be verified.
- Per-request headers (`copilot.RequestHeaders`, applied last through
  `api.Config.RequestHeaders` on all three routes, evaluated once per call so
  retries resend the same values), following OpenCode's `copilot.ts`:
  - `x-initiator: user` only for a main-loop request the loop marks as a user
    turn (`WireHints.UserTurn`: the prompt, and the request after a steering
    message the user typed). Tool-result follow-ups, text conduit injects as a
    user message (team mailbox deliveries, task notifications), sub-agents,
    workflows, compaction, background calls and requests without hints send
    `agent`, so they don't bill as premium requests. Retries keep the value
    of the request they repeat, so a retry after a mid-stream failure may
    bill again; a connection failure never reached the server.
  - `X-Interaction-Id: <session id>` from `WireHints.SessionID`, which the
    agent loop fills and sub-agents inherit. Compaction requests carry no
    session id yet, so they omit it.
  - `Copilot-Vision-Request: true` when any message, or a tool result inside
    one, holds an image.
- The route comes from each model's `supported_endpoints` in `/models`
  (`copilot.RouteForModel`; the table is recorded in the credential metadata
  as `model_endpoints` each time models are fetched): `/responses` first, then
  `/v1/messages`, otherwise `/chat/completions`. `gpt-5-mini` stays on chat
  completions when it offers it. Without a recorded list for the model (models
  never fetched, or a model that reports none) the name heuristic applies:
  `gpt-N` with N >= 5 (not `gpt-5-mini`) to `/responses`, `claude-*` to
  `/v1/messages`, the rest to `/chat/completions`.
- `/v1/messages` strips `cache_control.ephemeral.scope`, because Copilot
  rejects the Claude API extension field.
- Not ported from oh-my-pi: the retry with the Copilot CLI integration id
  after a Business `model_not_supported`.

Known drift risks:

- Copilot may change accepted editor header values.
- Copilot may tie premium-request billing to headers beyond `x-initiator`
  (OpenCode's `2026-06-01` API version adds token-based billing).
- Model metadata can change per account entitlement.
- Supported endpoint metadata can change without a public version marker.

## ChatGPT / Codex

Reference sources:

- `../opencode/packages/opencode/src/plugin/codex.ts`
- `../opencode/packages/llm/src/providers/openai-options.ts`
- Optional local Codex CLI checkout through `CODEX_REPO`

Conduit owner files:

- `internal/provider/codex/codex.go`
- `internal/app/auth.go`
- `internal/api/openairesponses.go`
- `internal/api/openaicompat.go`
- `internal/api/stream_test.go`

Wire contract:

- OAuth issuer is `https://auth.openai.com`.
- OAuth client id is `app_EMoamEEZ73f0CkXaXp7hrann`.
- Browser OAuth uses the fixed callback
  `http://localhost:1455/auth/callback`.
- Auth requests include `codex_cli_simplified_flow=true`,
  `id_token_add_organizations=true`, and `originator=opencode`.
- Runtime base URL is `https://chatgpt.com/backend-api/codex`.
- Runtime calls use the Responses path and OAuth bearer auth.
- Runtime calls send `ChatGPT-Account-Id` when the token exposes an account id.
- Runtime request shape differs from normal OpenAI API keys:
  system text is sent as top-level `instructions`, `store` is set to `false`,
  and `max_output_tokens` is omitted unless this contract changes.
- Every request carries a non-empty `session_id` header and the same value as
  `prompt_cache_key`: the conduit session id (`WireHints.SessionID`, which
  sub-agents inherit), else a stable id generated per client.
- Because `store` is false, requests send
  `include: ["reasoning.encrypted_content"]`. The stream's reasoning items
  come back as thinking blocks whose signature is
  `oai-resp:<item id>:<encrypted_content>`, and later turns replay them as
  `reasoning` input items (`summary: []`, `encrypted_content`, no `id`, since
  nothing is stored with `store=false`), following opencode and codex-rs. A
  foreign signature (Anthropic's) is never replayed on this wire, and
  `oai-resp:` blocks are stripped from Anthropic-wire requests
  (`dropNonAnthropicThinkingBlocks`), so a session that moves from Codex to
  Claude does not 400.
- Input is capped at 272K (`codex.InputLimit`), whatever context window a model
  advertises, so auto-compact fires before the backend rejects the request.
- `input_tokens_details.cached_tokens` is reported as
  `cache_read_input_tokens`, and `input_tokens` is the uncached remainder (the
  Anthropic convention), so the prompt size is still their sum.
- The model list comes from `GET <base>/models?client_version=...` (entries
  with `visibility: list`), falling back to the shared catalog and then to
  `codex.FallbackModelIDs`. `codex.IsAgenticModel` drops `gpt-5*-pro` and the
  non-chat models from every source.
- Responses streams must accept valid SSE bodies even when the server omits the
  `Content-Type` header.
- Tool schemas are normalized so empty object schemas include
  `properties: {}`.
- `/effort` maps onto `reasoning.effort` (8000+ budget tokens `high`, 2000+
  `medium`, else `low`); no `reasoning` object when thinking is off.

Known drift risks:

- The ChatGPT / Codex account path is product-account auth, not the public
  OpenAI API.
- Auth parameters may be rejected if they drift from the reference client.
- Plan-gated accounts may authenticate but reject runtime calls.
- The model allowlist is conservative until model discovery is verified for
  real accounts.

## Grok / xAI

Reference sources:

- `../crush/internal/oauth/grok/` (crush `30145cbc`): `oauth.go`, `flow.go`,
  `device.go`, `models.go`
- Checked by `scripts/provider-wire-check/check-xai.sh`

Conduit files:

- `internal/provider/xai/` (`xai.go` OAuth and refresh, `flow.go` browser
  flow, `device.go` device flow, `models.go` model listing)
- `internal/app/auth.go` (`isXAIOAuth`, `xaiConfig`)

Contract:

- Public OAuth client `b1a00492-073a-47ea-816f-4c329264a828`, the one xAI's own
  Grok CLI registers. xAI has no self-service client registration, so agent
  CLIs share it. Issuer `https://auth.x.ai`, endpoints `/oauth2/authorize`,
  `/oauth2/token` and `/oauth2/device/code`.
- Scope `openid profile email offline_access grok-cli:access api:access
  conversations:read conversations:write workspaces:read workspaces:write`;
  the shared client's whole registered set is expected.
- Browser flow is PKCE S256 with `state` and `nonce` and a `referrer`
  parameter (`conduit`). The redirect is `http://127.0.0.1:<port>/callback`
  on an OS-assigned port; xAI registers loopback redirects by path only, so
  the path must stay `/callback`. Callback responses carry CORS headers for
  `https://accounts.x.ai`, whose consent page may deliver the code with a
  cross-origin fetch (with `Access-Control-Allow-Private-Network`).
- Device flow (RFC 8628) is implemented in the package
  (`Authorizer.AuthorizeDevice`); the TUI uses the browser flow.
- Refresh posts `grant_type=refresh_token`; the previous refresh token is kept
  when the answer omits one. Refresh is serialized across processes with
  `credlock` key `xai:<credential>`.
- Runtime is OpenAI-compatible chat completions at `https://api.x.ai/v1` with
  the access token as a Bearer credential.
- Credentials are stored structured (`ProviderCredential`: access token,
  refresh token, expiry, `email` and `account_id` from the ID token) under the
  alias `grok-xai` (`grok-xai-2`, ...). The alias avoids an `xai-` prefix,
  which the provider form rejects as an API-key shape. Any alias starting with
  `grok-xai` is a Grok account; an `api.x.ai` provider whose credential is
  structured is also treated as OAuth, while a plain API key keeps the
  generic OpenAI-compatible path.
- Models: live from `GET /v1/models`; only entries with
  `completion_text_token_price` are language models (image and video models
  are dropped). Context length, image input (`prompt_image_token_price`) and
  reasoning (`capabilities.reasoning_effort`) come from the listing. The static
  fallback is `xai.FallbackModels`.

Known drift risks:

- The client id is shared with xAI's Grok CLI and could be restricted by xAI.
- Registered redirect URIs and scopes can change without notice.
- Model entitlements differ per plan.

## OpenAI-dialect transports (shared)

All product accounts above, and every `openai-compatible` provider, go
through `internal/api/openaicompat.go` (`/chat/completions`),
`internal/api/openairesponses.go` (`/responses`) and
`internal/api/openaistream.go`, which convert the streams into Anthropic
Messages events for the agent loop.

Stream contract:

- A chat stream ends only after a `finish_reason` or `[DONE]`; a Responses
  stream only after `response.completed`, `response.incomplete` or
  `response.done`. A body that stops earlier fails with
  `api.ErrStreamTruncated` and no `message_stop`, so the loop retries it
  instead of treating a cut-off reply (or half a tool call) as finished.
- A mid-stream error (`{"error": …}` chunks with string or numeric codes, a
  Responses `error` event with top-level or nested fields, `response.failed`,
  `finish_reason` `error` / `network_error`) becomes an Anthropic `error`
  event typed `rate_limit_error`, `overloaded_error`, `authentication_error`,
  `invalid_request_error` or `api_error`, with the provider's type and code
  kept in the message so `api.Classify` can spot context overflows.
- `finish_reason: content_filter` and Responses `incomplete` with
  `content_filter` map to stop reason `refusal`; a natural end that carried
  tool calls maps to `tool_use`.
- Non-2xx responses decode into `*api.APIStatusError` with the status and
  `retry-after` / `retry-after-ms` / `x-should-retry` values.

Request contract:

- The system message keeps every conduit system block (agent prompt, output
  guidance, CLAUDE.md / AGENTS.md instructions, memory, skills) except the
  `x-anthropic-billing-header` block and the exact Claude Code identity
  blocks, which only make sense to Anthropic.
- Reasoning summaries: a Responses request with thinking on sends
  `reasoning.summary: "auto"`; `response.reasoning_summary_text.delta` (and
  `response.reasoning_text.delta`) become thinking deltas, with a blank line
  between summary parts.
- User `image` and `document` blocks are sent, not dropped. Chat: content
  parts (`image_url` with a base64 data URL, `file` with `file_data` for
  PDFs). Responses: `input_image` and `input_file`. A model the catalog lists
  without vision gets a text note instead of the image
  (`api.Config.ModelVision`); models the catalog does not list get the image.
  Images inside tool results are still text-only.
- Requests wait at most 5 minutes for response headers on hosted endpoints
  (Anthropic, ChatGPT/Codex, Copilot, OpenAI), then fail with
  `api.ErrResponseHeaderTimeout`, which `api.Classify` treats as transient and
  `withRetry` resends once. Local and LAN base URLs have no limit by default,
  since a server may prefill for minutes; a generic openai-compatible provider
  sets one with the `responseHeaderTimeout` provider setting (seconds, unset
  means none). It is separate from the stream idle timeout, which starts once
  the stream is open.
- Every tool call gets a result: an empty one is sent as `(no output)`. A
  user turn's tool results go before its own text.
- In-text tool calling (provider `toolCallFormat`, default `native`): `hermes`
  or `qwen3` sends no `tools` field; the schemas go into the system prompt in
  a `<tools>` block, and the model writes calls as
  `<tool_call>{"name":…,"arguments":{…}}</tool_call>` (syntax from oh-my-pi's
  hermes and qwen3 dialects). Streamed and complete replies are parsed into
  `tool_use` blocks with generated ids. A call opens at the start of a
  line (blanks aside) or right after another call. A call that starts
  mid-line counts only if nothing but whitespace and more calls follow it to
  the end of the reply (a real call ends the turn; a quoted excerpt is
  followed by prose), so it is held until the next non-blank text or the end
  of the stream. Nothing inside a fenced code block is a call; inline code spans are not tracked, and
  an unmatched backtick has no effect. The closing tag counts only outside
  JSON strings (an argument may contain `</tool_call>`), with the first plain
  close tag as the fallback at the end of the reply. Earlier calls go back as that same text in the assistant
  message and their results as `<tool_response>` blocks (an error result is
  prefixed `Error: `) in one user message ahead of the user's own text, for
  both syntaxes, so the server's chat template needs no tool-role support. A
  call whose arguments are not a JSON object (or that was cut off) becomes a
  `tool_use` the agent loop answers with its invalid-arguments error result;
  a block with no usable name stays text. `auto` is native until a 400/422
  says the model or server does not support tools (Ollama, llama.cpp without
  `--jinja`), then hermes for that model for the rest of the session. The
  retry follows what the rejected body carried, so requests built before the
  rejection was learned are retried too, and a retry after a 5xx rebuilds its
  body.
  Limits and weaker isolation: text tools have weaker isolation than native
  tools. Results are plain text in a user turn, and a result that contains
  `</tool_response>` is escaped as `<\/tool_response>` on replay, but the
  model can still be steered by text it reads; only the line-start and
  trailing-call rules keep a quoted `<tool_call>` from running. Images in tool results are text-only
  (as on the native path). Reasoning replay in text mode follows the usual
  policy, but an assistant turn's tool calls are in its text rather than in
  `tool_calls`, so the Kimi/OpenRouter "tool-call turns only" replay rule
  never fires for it. A complete (non-streamed) reply whose call has unusable
  arguments yields a `tool_use` with empty input, since that path has no
  invalid-arguments signal.
- Reasoning replay, `reasoning_effort` and `<think>` splitting are described
  in `docs/local-provider-plan.md` ("Reasoning on OpenAI-compatible servers").
