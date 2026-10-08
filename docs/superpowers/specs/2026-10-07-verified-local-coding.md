# Verified local coding

Approved scope: the architecture design in the chat, implemented without Jira or a live Qwen dependency. Conduit owns task state, verification, repair, and labels; local-router remains an inference transport. All new behavior is opt-in. Existing Anthropic wire behavior and legacy execution remain compatible.

## Runtime contract

- Repository definitionOfDone resolves shared and local settings, validates command IDs/cwd/time limits, and freezes the policy for a run.
- Completion candidates are checked only on a natural completed coding turn. Pauses, failures, token exhaustion, cancellation, and user yields cannot count as verification.
- Pending changes flush before checks. Typed command outcomes, policy/workspace fingerprints, foreground execution, cancellation and bounded repair establish completion. Model text remains provisional until checks pass.
- Cloud orchestration uses LocalImplement for bounded local implementation and fix tasks. Optional review resolves explicitly; configured target failures cannot silently retain a different transport.
- Endpoint identity is stable, pools have durable session-role affinity and separate expiring request leases. Load refers to participating Conduit processes, not unobserved external clients.

## Recording and offline work

- A separate opt-in append-only episode ledger records sanitized structured requests, outputs, tool execution, patch candidates, check results and decisions. Recording omissions and incomplete writes prevent export eligibility.
- Test pass, patch application, model review and explicit acceptance are separate signals. DPO pairs require matching conditioning context and base state. Evaluation-origin samples never enter training.
- Offline export/evaluation/training entry points require explicit invocation. Training pins model, tokenizer, dataset and adapter configuration; promotion is never automatic. Hardware-dependent training and a live Qwen smoke test are not run during development.

## Constraints

Preserve the repository's existing Go version pins. RTK filtering remains in-process. No live credentials, inference, uploads, merges, or releases in tests. Required final checks: make build, make verify. Document accurately any portion still requiring deployment validation.
