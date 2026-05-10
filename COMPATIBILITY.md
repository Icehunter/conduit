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
| `Version` (Claude Code version claim) | `cmd/conduit/main.go` | `2.1.138` |
| `SDKPackageVersion` | `internal/api/client.go` | `0.93.0` |
| `anthropic-version` header | `internal/api/client.go` | `2023-06-01` |
| OAuth client ID | `internal/auth/flow.go` | see source |
| Token URL | `internal/auth/flow.go` | see source |
| OAuth beta header | `internal/app/auth.go` | `oidc-federation-2026-04-01` |

Run `make verify-wire` to check these against the current upstream fingerprint.

---

## Active beta headers

Conduit sends 11 beta headers. Upstream CC v2.1.138 advertises 2 via the
extractor pattern. The extras are valid API features — this is marked DIVERGED
in `verify.mjs`, not a blocking incompatibility. Capture with mitmproxy if a
regression appears.

---

## Intentional divergences

| Area | CC behavior | Conduit behavior | Why |
|------|-------------|-----------------|-----|
| `ExitPlanMode` approval | Returns bool | Returns `PlanApprovalDecision` struct; user picks auto/accept-edits/default/chat | Richer plan flow with council path |
| System prompt | Byte-identical to CC TS | Conduit-authored equivalent | Avoids IP reproduction; same behavioral sections |
| BashTool on Windows | `BashTool` registered | `Shell` (PowerShell) registered instead | Go `os/exec` on Windows uses PowerShell |
| Beta header count | 2 detected | 11 sent | Extra betas are valid API features; no API rejection observed |
| Tool names `mcp`/`mcp__` | Pass-through aliases | `ListMcpResources`/`ReadMcpResource` | Conduit's MCP surface is explicit, not aliased |
| Auto-updater | npm self-replace | Passive GitHub Release notifier | Conduit ships as a static binary |

---

## Wire sync log

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

Bridge/IDE integration, remote sessions, team swarm messaging, AWS auth,
mTLS, GrowthBook feature flags, Anthropic-internal analytics, voice STT,
KAIROS assistant mode, and ULTRAPLAN are intentionally excluded. They do not
affect the wire format for normal Claude Max/Pro subscription usage.
