# Verified Local Coding Implementation Plan

> For agentic workers: execute the approved plan with test-first changes and independent cross-review. User authorized implementation in chat; no further design approval or Jira is required.

**Goal:** Deliver optional verified local coding, privacy-controlled episode data, endpoint affinity, and offline improvement tooling.

**Architecture:** Retain the provider registry, MCP transport, permission gate and existing transcript format. Add small verification, routing and episode packages, integrate them at runtime boundaries, and expose offline dataset/evaluation tools.

**Tech Stack:** Existing Go toolchain, standard library, existing SQLite and in-process RTK; optional Python training dependencies remain external.

**Spec:** ../specs/2026-10-07-verified-local-coding.md

## Global constraints

- Disabled features preserve existing runtime behavior and wire format.
- No live Qwen dependency for development; use HTTP/MCP fixtures.
- No autonomous training, model promotion, upload or publication.
- Every executable mutation keeps normal permissions and cancellation.
- Finish with make build and make verify; update STATUS.md and project memory accurately.

## Review focus

- A completion gate must never test pre-flush files and then publish untested edits.
- A role resolution error must never send a local model name to an inherited cloud client.
- Cancelled/dismissed reviews and verification passes are not explicit human preferences.
- Malformed policy, changed check definitions and active writers cannot silently bypass checks.
- Crashed schedulers and incomplete/redacted traces must not become capacity leaks or training samples.

## Work breakdown

### 1. Provider and settings foundations (routing worker)
- [x] Add failing tests for task-restricted MCP adapters, endpoint identity and old-config compatibility.
- [x] Implement task=implement/fix capability negotiation in LocalImplement; preserve draft-only behavior.
- [x] Add optional review role, endpoint identities, pool settings and SQLite affinity/request leases, with cancellation/crash/contended reservation tests.
- [x] Add definitionOfDone and episodes settings using the interfaces agreed with runtime workers; regenerate schemas after integration.

### 2. Verification and completion (verification worker)
- [x] Add failing tests for successful/failed/cancelled commands and policy validation.
- [x] Implement standalone verification runner, structured results and policy/workspace fingerprints with process-group cancellation and bounded output.
- [x] Add an optional completion callback to LoopConfig, distinguish natural completion from yields/exhaustion and buffer candidate text in enabled mode.
- [x] Tests prove failure-feedback continuation is bounded, failed candidates never publish success, and nil callback is backward compatible.

### 3. Episode and offline foundation (episodes worker)
- [x] Add failing tests for all-channel sanitization, append ordering, truncated journal recovery and export eligibility.
- [x] Implement separate append-only episode events, source/decision provenance and conservative SFT/DPO export.
- [x] Implement held-out manifest evaluation support and offline training/export command preparation with pinned artifacts, no implicit installation or execution.
- [x] Test split leakage, mismatched DPO context, deterministic manifests and failure handling.

### 4. Runtime integration (primary)
- [x] Write failing end-to-end tests using a mock local provider and real temporary workspace.
- [x] Wire policy loading, trusted/authorized checks, pre-verification flush, local repair routing and optional independent review into shared runtime and headless setup.
- [x] Wire bounded-task requests, patch application, verification, review and explicit acceptance. Arbitrary chat/child/tool trajectories remain outside SFT until complete conditioning can be captured.
- [x] Wire pool selection, persisted assignments and lease lifetime into actual local calls.
- [x] Expose documented offline CLI commands and sample opt-in config.

### 5. Verification and delivery (primary + cross-review)
- [x] Run package checks during implementation and resolve integration failures.
- [x] Cross-review other workers' changes, repair actionable findings and add regression tests.
- [x] Run make build and make verify; inspect status/diff for unwanted changes.
- [x] Update STATUS.md, compatibility notes and MEMORY.md with real behavior and deployment limitations.

## Execution ledger

- Baseline: clean main at 639843ee78a7fcccee3b4bad9588bdcb82814a91; make verify passed during architecture review.
- Working branch: codex/verified-local-coding in the managed worktree attached to this chat.
- Ruling: follow current go.mod/CI version pins rather than stale prose version numbers.
- Ruling: user allowed Qwen to shut down during implementation; initial validation used mock endpoints and local commands. The user subsequently enabled one endpoint and authorized a live test.

- Delivery: full make verify passed (fmt, vet, zero lint issues, race tests); native build, Linux/Windows cross-compilation and four Python pipeline tests passed.
- Independent cross-review repaired source-permission bypass, implicit diff acceptance, token-continuation loss, endpoint fallback, omitted-trace export, cross-session DPO pairing and candidate/editor attribution races.
- Live validation (2026-10-08): single-endpoint Qwen3.8-27B via local-router passed deliberate failing candidate → real verifier output → repaired candidate. First run took 8.48 seconds; independent audit caught terminal attempt metadata defaulting to zero. Added a failing regression, fixed the event, rebuilt, reran make verify successfully and repeated the live test in 3.88 seconds; terminal attempt is now 1 and all leases are released. See docs/verified-local-live-smoke.md.
- Deployment limits: no GPU training or live multi-endpoint distribution test; MCP traces without validated normalized server conditioning remain ineligible; streaming role pools are explicitly unsupported. The follow-up `2026-10-08-mcp-request-traces.md` adds opt-in local-router trace capture and consumption.
- No commit, merge, upload or model promotion was performed. Original main checkout remains clean.
