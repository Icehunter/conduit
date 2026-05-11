# Provider Account OAuth Plan

This document is for Conduit maintainers adding browser or device-code
provider accounts. After reading it, a maintainer should be able to implement
GitHub Copilot and ChatGPT Plus/Pro account providers without mixing them into
generic OpenAI-compatible routing.

This plan is intentionally separate from the OpenCode enhancements roadmap. The
roadmap explains why these providers matter; this document explains how to
build them safely in Conduit's provider architecture.

## Decision

Treat Copilot and ChatGPT Plus/Pro like Claude subscription support: they are
product-account wire providers, not ordinary API-key providers.

Conduit already has three distinct provider classes:

- Generic API-key providers, where a static credential is enough.
- OpenAI-compatible providers, where a base URL and bearer key unlock a catalog
  of models.
- Product-account providers, where the user logs into a product account and
  Conduit must preserve that product's auth, headers, endpoint shape, quota
  behavior, and stream quirks.

Claude Max/Pro subscription support is the existing product-account provider.
GitHub Copilot and ChatGPT Plus/Pro should follow that pattern: isolated auth,
isolated request adapter, explicit model discovery, and compatibility notes
that are not shared with generic OpenAI-compatible providers.

## Reference Behavior

OpenCode and Crush agree on the broad shape but differ in packaging.

OpenCode has a generic provider-auth plugin interface with `api` and `oauth`
methods. OAuth providers expose prompts, an `authorize` step, and a `callback`
step. Successful callbacks persist either an API key credential or an OAuth
credential with access, refresh, expiry, and provider-specific metadata.

OpenCode's GitHub Copilot provider uses GitHub device authorization. It requests
a device code, shows the verification URL and user code, polls GitHub for an
access token, and uses that token when calling Copilot endpoints. Its Copilot
SDK is deliberately provider-specific: it handles Copilot chat, responses,
reasoning fields, tool-call streaming, and Copilot request headers.

Crush implements GitHub Copilot in a smaller Go-native way. It requests a
GitHub device code, polls for a GitHub token, exchanges that token for a
Copilot API token, stores the Copilot token as the active provider key, and
keeps the GitHub token as refresh material. It also imports existing GitHub
Copilot app tokens from disk when available.

OpenCode's ChatGPT Plus/Pro support is not the normal OpenAI API provider. It
uses OpenAI account OAuth, obtains ChatGPT/Codex access and refresh tokens,
extracts account identity from token claims, rewrites OpenAI-style requests to
the ChatGPT Codex responses endpoint, and attaches account-specific headers.
That is the same category as Conduit's Claude subscription path.

## Architecture

Add a product-account auth layer on top of the existing provider-auth package.
It should support API keys, device-code OAuth, and localhost PKCE OAuth through
one internal interface, but each provider should own its wire details.

Core concepts:

- `ProviderAccount` is non-secret metadata: provider id, account id, display
  name, method, added time, and active state.
- `ProviderCredential` is secret material stored in secure storage: API keys,
  OAuth access tokens, refresh tokens, expiry, and provider-specific private
  metadata.
- `ProviderKind` remains explicit in role bindings. A selected Copilot model
  should bind to a Copilot provider kind, not to generic `openai-compatible`.
- Runtime adapters are typed. No dynamic provider SDK should hide streaming,
  tool-call, or reasoning differences from the agent loop.

Conduit should keep the current OpenAI-compatible path for Gemini, OpenRouter,
OpenAI API keys, and private OpenAI-compatible endpoints. Copilot and
ChatGPT/Codex should get their own provider kinds because their auth and wire
behavior are materially different.

## GitHub Copilot Plan

Goal: users can connect GitHub Copilot once, then see the models their account
can access in the model picker after connect, restart, and catalog refresh.

Current state: Copilot is experimental. Conduit has a GitHub device-code login,
GitHub-token to Copilot-token exchange, secure structured credential storage,
model discovery, and model picker exposure through explicit provider entries.
Runtime routing now follows OpenCode's provider layout: Claude-flavored Copilot
models use the Anthropic `/v1/messages` shim, GPT-5-class models use
`/responses`, and the remaining chat models use `/chat/completions`.
Entitlement, authorization timeout, and model-discovery failures are surfaced as
recoverable UI errors rather than corrupting provider config.

Implementation slices:

1. Add a Copilot OAuth package.
   - Request GitHub device codes.
   - Poll for GitHub access tokens.
   - Handle `authorization_pending`, `slow_down`, timeout, cancellation, and
     unavailable Copilot entitlement.
   - Exchange or refresh the GitHub token into a Copilot API token.

2. Store Copilot account credentials securely.
   - Store the GitHub token as refresh material.
   - Store the current Copilot API token as access material.
   - Persist account metadata outside secure storage.
   - Refresh on startup when the Copilot token is expired or near expiry.

3. Add Copilot model discovery.
   - Fetch Copilot model metadata with account auth.
   - Filter to picker-enabled and non-disabled models.
   - Preserve context window, output limit, tool-call, vision, reasoning, and
     endpoint capability flags.
   - Cache discovered models, but refresh on login, restart, and explicit
     `/models --refresh`.

4. Add a dedicated Copilot runtime adapter.
   - Start with chat-completions streaming because Conduit already has an
     OpenAI-compatible stream converter.
   - Add Copilot headers, including initiator and intent headers.
   - Parse Copilot reasoning fields and preserve reasoning continuation
     metadata where the API emits it.
   - Assemble streaming tool-call arguments robustly, including chunks that
     contain full JSON in one event.
   - Add responses-mode support only for models that require it.

5. Wire the TUI.
   - Accounts tab: connect, disconnect, refresh, rotate/re-auth.
   - Model picker: show "GitHub Copilot" as its own provider section.
   - Role assignment: bind selected Copilot models by role.
   - Status/error text: distinguish GitHub auth failures, Copilot entitlement
     failures, token refresh failures, and model endpoint failures.

Verification:

- Unit tests for device-code polling state transitions.
- Unit tests for secure storage round trips and refresh decisions.
- HTTP fixture tests for Copilot model discovery.
- Streaming adapter tests for text, reasoning, tool calls, usage, and error
  responses.
- End-to-end manual verification with a real Copilot account before marking
  the capability done.

## ChatGPT Plus/Pro Plan

Goal: users can connect a ChatGPT Plus/Pro account and route eligible
ChatGPT/Codex models through Conduit's role picker, with the feature clearly
labeled until the wire path is verified.

Implementation slices:

1. Add a ChatGPT/Codex OAuth package.
   - Implement PKCE browser login through the OpenAI account issuer.
   - Implement headless device flow if the verified endpoint remains usable.
   - Extract account identity from token claims.
   - Refresh access tokens from refresh tokens.
   - Store access, refresh, expiry, and account id securely.

2. Add a ChatGPT/Codex runtime adapter.
   - Route OpenAI Responses-shaped requests to the ChatGPT Codex backend
     endpoint.
   - Attach OAuth bearer auth rather than API-key auth.
   - Attach account identity headers when needed for organization or plan
     subscriptions.
   - Match Codex CLI request behavior where it differs from normal OpenAI API
     behavior.

3. Gate model discovery and picker rows.
   - Expose only models known to work with the ChatGPT/Codex account path.
   - Prefer server-confirmed model lists when available.
   - Fall back to a conservative allowlist only while the provider is marked
     experimental.
   - Show plan-gating errors clearly when the user lacks Plus/Pro/Codex access.

4. Keep API-key OpenAI separate.
   - OpenAI API keys continue using the normal OpenAI/OpenAI-compatible path.
   - ChatGPT Plus/Pro OAuth never reuses `OPENAI_API_KEY`.
   - A user may have both providers connected; model picker labels must make
     the difference obvious.

Verification:

- Unit tests for PKCE URL construction, state validation, token refresh, and
  account-id extraction.
- HTTP fixture tests for Codex request rewriting and auth headers.
- Streaming adapter tests for Responses events, tool calls, reasoning, usage,
  and error responses.
- Manual verification with a Plus/Pro account before removing the experimental
  label.

## Storage Shape

Provider OAuth storage should be account-aware from the beginning.

Non-secret config should answer:

- Which provider accounts exist?
- Which account is active for each provider?
- Which role bindings refer to which provider account and model?
- Which model cache belongs to which provider account?

Secret storage should answer:

- What credential material is needed to call the provider?
- When does it expire?
- What refresh material is available?
- What provider-private metadata must travel with requests?

Do not store OAuth tokens in plain JSON config. Do not embed access tokens in
role bindings. Role bindings should identify a provider account and model; the
runtime should resolve secret material at call time.

## Compatibility Documents

Claude wire compatibility stays in `COMPATIBILITY.md`.

When Copilot or ChatGPT/Codex support lands, add a separate compatibility
section or document for provider-account wire paths. It should track:

- OAuth endpoints and client identifiers.
- Required request headers.
- Endpoint paths.
- Token refresh behavior.
- Model discovery behavior.
- Known intentional divergences from reference clients.

Do not put Copilot or ChatGPT/Codex constants into the Claude compatibility
contract. They are the same architectural category, but not the same wire
contract.

## Risks

Copilot risk is moderate. The GitHub device flow is stable, and both reference
implementations demonstrate the token and model-discovery shape. The remaining
risk is Copilot API/header drift.

ChatGPT/Codex risk is higher. The path is product-account based and may change
independently of the public OpenAI API. Ship it behind an experimental label
until token refresh, account selection, model gating, and stream conversion are
verified with real accounts.

The largest Conduit risk is architectural leakage. Avoid "just make it
OpenAI-compatible" shortcuts. Product-account providers must be explicit,
auditable, and independently testable.

## Suggested Order

1. Land the provider-account storage and auth interface changes.
2. Implement GitHub Copilot device auth.
3. Implement Copilot model discovery.
4. Implement Copilot chat streaming.
5. Add Copilot responses support for models that require it.
6. Implement ChatGPT/Codex OAuth as experimental.
7. Implement ChatGPT/Codex Responses routing and model gating.
8. Promote ChatGPT/Codex from experimental only after real-account verification.
