# Provider Account Compatibility

Last updated: 2026-05-11

This document tracks product-account provider wire contracts: GitHub Copilot
and ChatGPT / Codex. Claude subscription compatibility remains in
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
- Runtime headers include `Editor-Version`, `Copilot-Integration-Id`,
  `Editor-Plugin-Version`, `OpenAI-Intent`, and `x-initiator` on the routes
  that require them.
- Claude-family Copilot models route to `/v1/messages` and strip
  `cache_control.ephemeral.scope`, because Copilot rejects the Claude API
  extension field.
- GPT-5 Responses-capable Copilot models route to `/responses`, except the
  known `gpt-5-mini` exception which stays on chat completions.
- Other Copilot models route to `/chat/completions`.

Known drift risks:

- Copilot may change accepted editor header values.
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
- Responses streams must accept valid SSE bodies even when the server omits the
  `Content-Type` header.
- Tool schemas are normalized so empty object schemas include
  `properties: {}`.

Known drift risks:

- The ChatGPT / Codex account path is product-account auth, not the public
  OpenAI API.
- Auth parameters may be rejected if they drift from the reference client.
- Plan-gated accounts may authenticate but reject runtime calls.
- The model allowlist is conservative until model discovery is verified for
  real accounts.
