# Local-router request traces

Conduit's bounded coding workflow can record the actual request sent by local-router, including its expanded task profile and sampling parameters. This closes the missing-context gap for eligible MCP coding episodes. It does not enable recording or label a patch automatically.

## Enable and use

Build the updated local-router (`npm run build`) and Conduit (`make build`). Reconnect any already-running MCP client to load the new router process. Existing provider registrations need no changes.

Enable `episodes.enabled` and `episodes.includeContent` with an explicit `episodes.allowedPaths` list, as described in [training](training.md). Conduit discovers the router's boolean `include_trace` capability and requests it for these bounded coding calls. Content-disabled sessions do not request traces. Older servers still generate drafts, but their unobserved context stays ineligible for training.

The trace is returned as structured MCP data, separate from the answer text. It contains the exact expanded system/user messages and HTTP request parameters, requested model, an opaque endpoint hash, the router call ID, public completion, finish reason and optional token counts. It contains no HTTP authorization headers, API keys, raw endpoint URLs or private reasoning. No immutable model revision is inferred from a model name.

Conduit validates the version and shape, binds the final user message to the prompt it submitted, checks model identity and verifies that the traced answer matches the draft. A malformed or missing requested trace fails before a patch is applied. Supported but incomplete traces cannot produce training examples. Per-call IDs and usage are recorded as provenance without changing the digest of the model's input; the actual upstream endpoint hash remains part of context identity.

## Privacy and eligibility

All trace content passes through the same source allowlists and secret redaction as other episode events. Redacted, omitted, incomplete or evaluation traces remain excluded from export. Replacing repository/home paths also counts as redaction: transformed text is no longer the exact model input. A complete trace still requires passing verification and explicit acceptance for SFT; DPO additionally requires comparable conditioning and explicit human preference labels. A failed attempt and a repair conditioned on new diagnostics are not automatically a DPO pair.

Trace version 1 supports inline text generation only. The router rejects `include_trace:true` with `files`, `root` or `targets`, so hidden file expansion or router-side writes cannot bypass the consumer's source policy. Conduit already supplies authorized sources inline and applies patches itself. Other MCP callers retain the existing behavior unless they request the new field.

The router's usage ledger remains counts/paths/timings only. Its debug log remains a separate diagnostic facility and is not used as training evidence.

## Cross-project regression

After building both projects, run:

```sh
python3 scripts/verify_mcp_trace.py --router /absolute/path/to/local-router
```

This launches the real Conduit and router processes against a temporary synthetic HTTP backend; Qwen is not required. It checks exact equality between the HTTP request and recorded model input, real failed-check feedback and repair, protected test contents, explicit acceptance before SFT export, secret/path-redacted exclusion and content-disabled omission. Synthetic acceptance labels exist only in temporary fixture journals. The printed temporary directory contains the evidence.

The wire contract is documented in [the implementation plan](superpowers/plans/2026-10-08-mcp-request-traces.md). Multi-endpoint load telemetry and GPU training are separate features.

Validation on 2026-10-08: all four cross-project cases passed using the installed router checkout; router tests passed 493/493 with typecheck/build; Conduit `make build` and `make verify` passed with zero lint issues and race-clean tests. Temporary cross-project evidence: `/private/var/folders/kt/vwb3_29d0jn_khp2730cn3f40000gn/T/conduit-mcp-trace-17mti_u6`.

After Qwen restarted, a live trace smoke also passed: two real `qwen3.8-27b` calls repaired an intentionally failing candidate in 2.68 seconds. Both unredacted, complete requests matched the actual HTTP messages/model/parameters, captured at the fetch boundary without headers or private reasoning. SFT export produced zero rows before a synthetic acceptance label and one row afterward; the exported conditioning and completion matched the live request and accepted candidate. This label/data exists only in the temporary smoke fixture. Evidence: `/private/tmp/conduit-live-trace-mix_vm5j/audit.json`, with `wire.jsonl`, episode JSONL and `test-export.jsonl` beside it. No training was run.
