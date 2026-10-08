# Project working memory

- Verified-local-coding work: docs/superpowers/plans/2026-10-07-verified-local-coding.md tracks implementation and checks.
- This project work has no Jira ticket (explicit user instruction, 2026-10-07). Do not ask again for this scope.
- Qwen may be offline during development; use MCP/API fixtures. The authorized single-endpoint Qwen3.8-27B MCP smoke passed on 2026-10-08; multi-endpoint load distribution remains fixture-tested only.
- Preserve Go pins from go.mod and CI; AGENTS.md/CLAUDE.md prose may lag those files.
- Never infer explicit preference labels from pending-edit Approved: dismissal/cancellation paths can approve implicitly.
- Derive candidate postimages from validated edits, then bind verification's initial/final workspace digests to them. A fresh disk read is not proof of model authorship; concurrent editor changes block repair.
- Coding policies load strictly before settings migrations. Keep enabled task providers/policies frozen for a session and require restart after switching worktrees.
- MCP task profiles add hidden conditioning. Updated local-router exposes opt-in v1 structured traces; Conduit validates actual messages/parameters against the submitted prompt/model/answer. Legacy or incomplete traces stay training-ineligible. Keep per-call IDs/usage outside the context digest, but bind the actual upstream endpoint hash.
- Delivery checks: make verify (format/vet/lint/race) passed; native build and Linux/Windows cross-compilation passed; Python pipeline tests 4/4. Live inference subsequently passed; no GPU training or model promotion was performed.
- Terminal coding events must carry the last zero-based attempt index; defaulting to zero misattributes repaired episodes. A failing regression exposed this before the fix; a repeated live run confirmed attempt 1 and zero remaining routing leases (docs/verified-local-live-smoke.md).
- Any post-call privacy transformation, including root/home path substitution, must mark an event redacted. Transformed text no longer equals the model's conditioning; do not export it as exact training input. Normalizing diagnostics before a model call is a separate behavior change, not a reason to weaken this gate.
- MCP trace delivery (2026-10-08): router installed checkout rebuilt; 493 tests/typecheck pass. Conduit make build/verify pass; actual Conduit+router subprocess integration passes exact capture, explicit SFT acceptance, secret/path redaction exclusion and content-disabled omission. After restart, live Qwen trace smoke passed in 2.68s: two complete wire-equal request traces, real failure→repair, zero SFT rows before synthetic acceptance and one afterward. Evidence: /private/tmp/conduit-live-trace-mix_vm5j. No training performed.
