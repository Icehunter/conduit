# Conduit Enhancements From OpenCode

## Reader And Goal

This plan is for Conduit maintainers deciding which OpenCode ideas are worth
bringing into the Go codebase. After reading it, a maintainer should be able to
choose the next implementation slice, understand what Conduit already has, and
avoid accidentally treating Claude Code compatibility as Conduit's product
boundary.

Conduit started as a Claude Code parity port, but it is no longer only that.
The current center of gravity is a multi-model, role-based Go agent runtime.
Claude Code compatibility remains valuable because Conduit can track and verify
the headers, OAuth shape, and wire behavior needed for compatible
account/provider integrations. That should be treated as one supported
account/provider path, not as the limiting architecture for the rest of
Conduit.

## Context Snapshot

OpenCode advertises these user-facing capabilities:

- LSP enabled automatically
- multi-session operation on the same project
- share links for sessions
- GitHub Copilot login
- ChatGPT Plus/Pro login through OpenAI
- 75+ providers through Models.dev, including local models
- terminal, desktop, and editor-extension clients

Conduit already covers more of this surface than the marketing list implies:

- LSP exists as a tool and supports hover, definition, references, and
  diagnostics, but it has limited server coverage and no tool-level tests.
- Multi-agent orchestration exists through subagents, task tools, and council
  mode, but multiple simultaneous user sessions in one runtime are not a first
  class server concept.
- Provider routing exists for Claude subscription, Anthropic API,
  OpenAI-compatible providers, and MCP-backed local providers, with role
  assignments for default, main, background, planning, and implement.
- `/accounts` already proves the shape for account-aware auth and should grow
  into the common account surface for Claude, Anthropic API, OpenAI, Copilot,
  OpenRouter, Gemini, and other providers with custom auth flows.
- Session persistence, resume, export, search, rewind, worktrees, plugins, MCP,
  hooks, and Conduit-owned config are already strong.
- Bridge, IDE, remote trigger, and team messaging are intentionally descoped in
  the current status docs.

The biggest OpenCode difference is architectural: OpenCode runs an agent server
and treats TUI, desktop, web, and editor surfaces as clients over HTTP and event
streams. Conduit is currently a local Go TUI/runtime with direct ownership of
the loop. We should adopt OpenCode features incrementally without making the
real `conduit` binary less reliable.

Crush adds useful Go-native reference points rather than a separate product
direction:

- Catwalk-backed provider catalogs and `update-providers` are worth comparing
  with Models.dev before hardcoding one catalog source.
- GitHub Copilot login is a concrete example of an account flow that belongs
  under the expanded `/accounts` surface if its current API path is acceptable.
- Its local API uses Unix sockets or Windows named pipes as first-class
  transports, which may be a better default than raw loopback HTTP for local
  attach.
- Per-session prompt queues, cancellation, and agent state are useful server
  behaviors to model before multi-session UI work.
- Background shell jobs, explicit file-read tracking, generated config schema,
  and LSP diagnostics/restart/reference tools are high-value details that fit
  into the OpenCode-inspired slices below.

## Recommended Order

### 0. Re-evaluate STATUS/PARITY As Product Documents

Status: do before large feature work.

`STATUS.md` and `PARITY.md` were useful while Conduit was primarily a Claude
Code port. They may now be mixing three different jobs:

1. Tracking Claude-compatible wire/header behavior.
2. Tracking Conduit's real product capabilities.
3. Capturing historical porting decisions and descoped Claude Code features.

That split should be made explicit before new OpenCode-inspired work lands.

Recommended outcome:

1. Keep automated wire/header verification, because that is what allows the
   Claude Max/Pro account path to keep working.
2. Rename or replace `PARITY.md` with a narrower compatibility document if its
   main remaining purpose is Claude Code wire/account compatibility.
3. Replace `STATUS.md` with a Conduit capability matrix or roadmap if its
   milestone structure still implies Claude Code parity is the product target.
4. Move historical porting notes and descoped Claude Code features into an
   archive document so they remain searchable without steering future work.
5. Make the new docs answer product questions directly: which account/provider
   paths work, which roles can use which models, which clients exist, and which
   compatibility modes are intentionally supported.

Conduit-specific constraints:

- Do not delete useful compatibility knowledge until the wire-check scripts and
  account/provider docs cover the same operational facts.
- Keep `STATUS.md` updates required only if `STATUS.md` survives as the active
  capability tracker. If it becomes archival, update the replacement instead.
- Keep any Claude Code divergences that affect subscription access or protocol
  behavior in an active compatibility document.

### 1. Harden LSP To Match The Advertised Promise

Status: high leverage, lowest architectural risk.

OpenCode has a broad LSP server registry, config-driven overrides, lazy startup
when files are touched, status reporting, diagnostics seeding, symbols,
implementations, and call hierarchy. Conduit has the essential JSON-RPC client
and a smaller feature set.

Add in this order:

1. Add tool-level tests for existing LSP hover, definition, references, and
   diagnostics behavior.
2. Add an LSP status command or panel row that shows enabled, connecting,
   connected, broken, and disabled servers.
3. Expand server discovery conservatively: TypeScript, ESLint, Pyright, Go,
   Rust, Vue, Svelte, Astro, YAML, Lua, C#, Java, Bash, Dockerfile, Terraform,
   and Nix are good candidates.
4. Add config overrides for server command, args, env, extensions, root markers,
   and disabled state.
5. Add `documentSymbol`, `workspaceSymbol`, `implementation`, and call hierarchy
   methods after the existing operations have tests.
6. Consider Crush-style focused tool actions for diagnostics, references, and
   restart if they make model behavior more predictable than one broad LSP
   surface.

Conduit-specific constraints:

- Do not auto-download language servers at first. Prefer installed binaries,
  tool-managed binaries, or explicit config. Auto-install can come later.
- Keep LSP failures non-fatal. A missing server should degrade to no LSP data,
  not break file reads or the TUI.
- Update `STATUS.md` when LSP test coverage and server coverage change.

### 2. Add A Models.dev Catalog Without Replacing Provider Routing

Status: high leverage, medium risk.

OpenCode uses Models.dev as a cached model/provider database, then layers custom
auth and provider logic on top. Crush uses Charm's Catwalk provider catalog with
embedded fallback data and an explicit `update-providers` command. Conduit
already has a typed provider registry, role routing, and OpenAI-compatible setup
UI, but users still manually enter provider URLs and model ids.

Add a Go `modelcatalog` package that:

1. Supports at least one catalog source, with Models.dev and Catwalk evaluated
   before the source is locked in.
2. Supports an override URL and an override local JSON path.
3. Stores cache in Conduit-owned cache storage with a short TTL and
   cross-process locking.
4. Parses provider ids, display names, env var names, model context limits,
   output limits, cost, modality flags, reasoning support, and tool-call flags.
5. Exposes a read-only API to `/models`, `/providers`, and the model picker.

Then improve the provider UI:

1. Let users choose a known provider from the catalog.
2. Pre-fill the base URL, env var hints, context window, and model list when
   the catalog has that data.
3. Keep a manual OpenAI-compatible path for local or private endpoints.
4. Show model capabilities in the picker: tool use, vision, reasoning, context,
   and cost where available.
5. Add an explicit catalog refresh command similar to Crush's
   `update-providers`.

Conduit-specific constraints:

- Preserve the existing role model. Models.dev should populate options, not
  become the source of truth for active routing.
- The `~/.conduit/conduit.json` provider schema should continue to validate
  without network access.
- The agent loop should continue using explicit provider adapters. Do not add a
  generic dynamic SDK layer that hides streaming and tool-call differences.

### 3. Add Provider Auth Plugins Carefully

Status: useful, but only after the catalog exists.

OpenCode allows provider plugins to define auth methods with text/select
prompts and OAuth or API-key callbacks. This enables Copilot, OpenAI OAuth, and
other provider-specific flows without hardcoding everything into the core.

Conduit can adopt the idea in a Go-native way by expanding the existing
`/accounts` and settings accounts panel into a provider account manager:

1. Define a small provider-auth interface for built-in auth flows:
   `Methods`, `Authorize`, and `Callback`.
2. Store provider auth in secure storage keyed by provider id and account id.
3. Treat the current Claude subscription OAuth and Anthropic API support as
   reference implementations for additional account types.
4. Add API-key flows first for catalog providers.
5. Add OAuth flows only for providers with stable public flows and clear terms.
6. Add connect, switch, disconnect, refresh, rotate key, and validate actions to
   `/accounts`.
7. Let provider entries reference accounts rather than embedding auth details.
8. Keep the provider-auth panel in settings as the visual counterpart to
   `/accounts`.

Candidate built-ins:

- Claude Max/Pro subscription account
- Anthropic API account
- OpenAI API key
- Gemini API key
- OpenRouter API key
- GitHub Copilot OAuth, if the flow can be implemented without depending on
  private or brittle endpoints
- OpenAI ChatGPT Plus/Pro OAuth only if a supported flow is available

Conduit-specific constraints:

- Do not promise ChatGPT Plus/Pro or Copilot support until the auth path is
  verified against current provider behavior.
- Keep OAuth flows explicit and inspectable. No provider should get silent
  access to Conduit's Claude tokens or MCP tokens.
- Keep Claude-compatible identity and header behavior isolated to the
  Claude-compatible provider path. Other providers should use their own
  identity, headers, limits, and usage reporting.
- Add tests for auth persistence, refresh, missing-token handling, and provider
  validation.

### 4. Introduce A Local Server Spine For Multi-Session Clients

Status: strategic, larger architectural slice.

OpenCode's multi-session, desktop app, editor extension, remote attach, and web
UI all depend on a server that exposes sessions, messages, permissions,
questions, file status, provider config, and events. Conduit should not jump
straight to desktop or extension work before this spine exists.

Build a minimal Go server behind an explicit command such as `conduit serve`:

1. Start with a local-only transport. Prefer Unix socket on POSIX and named
   pipe on Windows if the client ergonomics are good; otherwise use loopback
   HTTP by default.
2. Serve a health endpoint and version endpoint.
3. Expose session list, session get, message list, message post, abort,
   compact, diff, todo, file status, and permission response endpoints.
4. Stream events using Server-Sent Events first. WebSockets can wait.
5. Keep all storage Conduit-owned and compatible with current JSONL sessions.
6. Gate non-loopback bind behind explicit flags and basic auth.
7. Add an internal client so the TUI can eventually talk to either in-process
   runtime or a server.
8. Add per-session prompt queue semantics before allowing multiple attached
   clients to post work into a busy session.

The first user-facing result should be simple:

- `conduit serve`
- `conduit attach http://127.0.0.1:<port>`
- two attached TUIs can see session status and one active run per session

Conduit-specific constraints:

- Preserve the single-binary workflow. The default `conduit` command should
  remain a working TUI without requiring a daemon.
- Keep permission prompts deterministic when multiple clients are attached.
  Start with one active permission responder per session.
- Add race tests around session run state, abort, and event fanout.
- Add file-read tracking alongside file status so attached clients can show
  what context the agent has actually consumed.

### 4.5. Add Crush-Inspired Runtime Polish Where It Fits

Status: opportunistic; fold into nearby slices.

These are not separate roadmap pillars, but they jumped out as practical
Conduit improvements:

1. Generate and publish a JSON schema for Conduit's config files so provider,
   MCP, LSP, hook, and account settings are easier to edit by hand.
2. Promote background shell jobs into a tracked tool surface with list, output,
   and kill actions if Conduit's current bash implementation still lacks the
   full background job lifecycle.
3. Track files read by each session explicitly, not only files mentioned in
   transcripts, so context summaries and attach clients can reason about stale
   or missing context.
4. Add hook ideas that avoid token waste: hook-returned `context_files` and a
   prompt-submit hook that can gate, rewrite, or attach context before a turn.
5. Support a broader set of project instruction files where it is compatible
   with Conduit's existing memory rules, including editor-specific instruction
   files used by Copilot, Cursor, Gemini, and AGENTS-style agents.

### 5. Add True Multi-Session UI After The Server Spine

Status: depends on the local server.

Conduit already has resume/search and subagents, but not OpenCode-style
multiple user sessions active in one project. After the server spine exists:

1. Add a session list panel with idle, busy, compacting, and error states.
2. Allow creating a new session without exiting the current one.
3. Allow switching sessions while a different session is running.
4. Add parent/child session navigation for forks or worktree sessions.
5. Add per-session diff, todo, context, and cost summaries.

This is the point where OpenCode's client/server model starts paying off for
Conduit without rewriting the agent loop first.

### 6. Add Session Share Links As An Optional Integration

Status: useful, but needs a hosted endpoint or configured enterprise endpoint.

OpenCode's share feature creates a remote share record, stores a local
share id/secret/url, then syncs session, message, part, model, and diff changes
to the remote endpoint. Conduit can copy the local mechanics, but it needs a
Conduit-owned or user-configured share service before this is useful.

Implement in two phases:

1. Local share export bundle:
   - Create a signed or checksummed bundle containing session metadata,
     messages, attachments references, model metadata, and git diff status.
   - Add `conduit import <bundle-or-url>` to restore a read-only copy.
   - This gives users shareable artifacts without running a public service.
2. Remote share service:
   - Add config for `share.endpoint`, `share.enabled`, and `share.auto`.
   - POST to create a share, persist id/secret/url, and queue sync updates.
   - DELETE to unshare.
   - Sync with batching and failure logging.

Conduit-specific constraints:

- Default sharing should be disabled.
- Redaction must be explicit before any remote sync exists.
- Do not upload local file contents merely because paths appear in a transcript.
- Include a trust warning for secrets in terminal output, diffs, and tool
  results.

### 7. Editor And Desktop Surfaces

Status: attractive, but should wait for the server spine.

OpenCode ships desktop and editor clients by wrapping or attaching to its server.
The editor path is ACP-related: OpenCode's Zed extension declares an agent
server that runs the `acp` command, and the ACP implementation maps editor
sessions and prompts back onto OpenCode sessions through its SDK. The desktop
path is related to the same server/client split, but it is not ACP-first:
OpenCode's desktop app starts a local sidecar server and connects to it over
HTTP with sidecar credentials.

Conduit should avoid building a separate UI runtime until the Go server API is
usable, and should treat ACP as the editor-facing protocol layered on top of
that runtime.

Recommended path:

1. Implement `conduit acp` after the local server has stable session, prompt,
   permission, and event APIs.
2. Zed extension first, because OpenCode's extension is mostly an ACP
   agent-server declaration and binary target mapping.
3. VS Code/Cursor/Windsurf extension second, using ACP where the editor
   supports it and falling back to the attach API only when needed.
4. Desktop last. A desktop app should wrap the local server, not duplicate the
   TUI runtime.

Conduit-specific constraints:

- Do not make desktop a prerequisite for normal terminal use.
- Keep ACP as a client protocol, not the internal agent loop.
- Keep the server API stable enough for editor clients before packaging them.
- Keep releases tied to the existing GoReleaser distribution plan.

## Feature Decisions

| OpenCode feature | Conduit recommendation | Why |
| --- | --- | --- |
| STATUS/PARITY docs | Re-evaluate before big slices | They may over-center Claude Code parity instead of Conduit's current product model. |
| LSP enabled | Build now | Existing LSP foundation needs coverage, status, and broader server registry. |
| Multi-session | Build after server spine | Needs run-state ownership and event fanout. |
| Share links | Build optional export first | Remote sharing needs endpoint, auth, and redaction policy. |
| GitHub Copilot | Investigate after `/accounts` expansion | Useful, but auth and API stability must be verified. |
| ChatGPT Plus/Pro | Investigate after `/accounts` expansion | Do not assume a stable supported OAuth/API path. |
| 75+ providers | Build catalog-assisted setup | Models.dev or Catwalk can improve setup while preserving Conduit roles. |
| Local models | Continue current MCP/OpenAI-compatible path | Already supported; catalog can make it easier. |
| Config schema | Build with catalog/config work | Crush shows this is a small feature with high setup UX value. |
| Background jobs | Fold into runtime polish | Helpful for long shell commands once output/kill/list are tracked. |
| File-read tracker | Fold into server spine | Useful for attach clients, summaries, and stale context warnings. |
| Terminal interface | Keep primary | It is the core Conduit product. |
| Desktop app | Defer | Needs server API first. |
| IDE extension | Defer, Zed first | Needs server API plus `conduit acp`. |

## Non-Goals

- Do not port OpenCode's TypeScript runtime wholesale.
- Do not replace Conduit's Claude Code wire/header verification with OpenCode
  parity.
- Do not let Claude Code compatibility block multi-model or role-based
  architecture. It is an account/provider compatibility path.
- Do not add dynamic provider execution that bypasses typed adapters for
  streaming, tool calls, errors, and token accounting.
- Do not enable remote sharing by default.
- Do not revive currently descoped bridge/team/remote-trigger features unless
  the server spine explicitly changes that decision in `STATUS.md`.

## Suggested Milestones

### C-O0: Documentation Governance

Deliverables:

- Decide whether `STATUS.md` remains active, is renamed, or is archived.
- Decide whether `PARITY.md` remains active, is renamed, or is narrowed to
  Claude-compatible wire/account behavior.
- Create replacement docs if needed: capability matrix, compatibility contract,
  and roadmap.
- Update contributor rules so future changes update the right document.

Verification:

- Existing wire-check workflow still has a clear owner document.
- New capability tracker answers current Conduit product questions without
  requiring Claude Code context.
- Full verify.

### C-O1: LSP Confidence

Deliverables:

- LSP tool tests.
- Status display.
- Configurable server registry.
- Expanded server detection for the top languages used in this repo family.
- Focused diagnostics, references, and restart actions if they improve model
  call reliability.

Verification:

- Unit tests for client behavior and server matching.
- `make build`
- `make test-race`
- `make lint`
- `make verify`

### C-O2: Model Catalog

Deliverables:

- Catalog fetch/cache package with Models.dev and Catwalk evaluated.
- `/models --refresh`, `update-providers`, or equivalent command.
- Provider picker catalog data.
- Provider credentials unlock all matching catalog models in `/models`; users
  should not have to add one provider entry per model.
- Provider setup should collect account/endpoint/credential only; model
  selection belongs in Ctrl+M `/models`.
- The model picker should support type-to-filter search while preserving
  provider headers that still contain at least one matching model.
- Capability and cost display in model/provider UI.

Verification:

- Offline cache tests.
- Bad JSON and network failure tests.
- Provider validation tests.
- Full verify.

### C-O3: Provider Auth

Deliverables:

- Provider-auth method interface.
- `/accounts` expansion for provider accounts.
- API-key flows for common catalog providers.
- Secure storage integration.
- Provider entries reference stored provider credentials by alias/ID instead of
  embedding secrets, and role selections can bind any catalog model under that
  credential.
- Settings UI for connect/switch/disconnect/rotate/validate.

Verification:

- Secure storage tests with fake backend.
- Missing/revoked credential tests.
- Full verify.

### C-O4: Local Server And Attach

Deliverables:

- `conduit serve`
- `conduit attach`
- Session/message/permission/event endpoints.
- Local-only default transport, with Unix socket or named pipe preferred if
  practical.
- Explicit auth for non-local transports.
- Per-session prompt queue behavior.
- File-read tracker endpoints.

Verification:

- HTTP API tests.
- SSE fanout tests.
- Race tests for session run state and abort.
- Manual two-client smoke test.
- Full verify.

### C-O5: Multi-Session UI

Deliverables:

- Session switcher panel.
- New session creation.
- Background session status.
- Session fork/parent-child navigation if the server API supports it.

Verification:

- TUI model tests for switching and busy sessions.
- Manual smoke test with two active sessions.
- Full verify.

### C-O6: Share And Import

Deliverables:

- Local share bundle export/import.
- Optional remote share endpoint config.
- Share/unshare commands or session actions.
- Redaction checklist before remote upload.

Verification:

- Bundle round-trip tests.
- Remote sync fake-server tests.
- Redaction tests.
- Full verify.

## First Slice To Pick

Start with C-O0. The compatibility/status documents should describe Conduit's
current product model before the roadmap grows more OpenCode- or Crush-shaped.
That slice is also small enough to land first and will make later `STATUS.md`
and `PARITY.md` updates less confusing.

After C-O0, pick C-O1 if the next goal is confidence in the existing runtime,
or C-O2 if the next goal is provider/account setup. C-O1 strengthens an
existing Conduit subsystem without server/API churn; C-O2 improves multi-model
setup without disturbing the agent loop.

After either slice lands, update `STATUS.md` with the new state and add any
intentional divergences to `PARITY.md` if they affect Claude Code parity.
