# MCP Request Traces Implementation Plan

**Goal:** Make authorized, complete local-router text generations eligible for Conduit's existing privacy-controlled episode export.

**Approved design:** The user approved an optional versioned structured trace containing the exact expanded request, sampling settings, endpoint/model identity, raw answer, finish reason and usage, plus Conduit consumption. Keep legacy tool output and existing privacy/acceptance rules. No training, model promotion or remote uploads.

## Contract (version 1)

The tools advertise optional boolean `include_trace`. When true, return the existing text content plus `structuredContent.local_router_trace`:

```json
{
  "version": 1,
  "request_id": "router-generated-id",
  "endpoint_id": "sha256:<64 lowercase hex digits>",
  "complete": true,
  "request": {
    "model": "the-requested-model",
    "messages": [{"role":"system","content":"exact expanded system"},{"role":"user","content":"exact expanded prompt"}],
    "parameters": {"max_tokens":8192,"temperature":0.2,"top_p":0.8,"top_k":20,"min_p":0.05,"repeat_penalty":1.1,"chat_template_kwargs":{"enable_thinking":false}}
  },
  "response": {
    "content": "unwrapped model response",
    "finish_reason": "stop",
    "usage": {"prompt_tokens":123,"completion_tokens":45}
  }
}
```

`parameters` contains exactly the other fields sent in the HTTP JSON body (optional keys stay absent). Capture from the actual request body at the fetch boundary, not a reconstruction. `usage` is null when absent; missing or unsupported finish reasons never certify a complete trace. Endpoint identity hashes the normalized upstream URL with credentials/query/fragment removed; no raw URL or credentials enter the trace. Request IDs use the router's existing usage call ID. No invented model revisions.

This first version supports inline text calls only: reject `include_trace:true` combined with `files`, `root` or `targets` before upstream/file effects. No hidden file reads or router writes can enter an apparently complete trace. Non-text/tool/reasoning response content is not exposed and prevents `complete:true`. Debug logging remains separate, and the usage ledger never gains prompt/response bodies. No trace is returned or persisted when not requested.

Conduit preserves `structuredContent` through MCP transport, discovers boolean `include_trace` capability, and requests it only for enabled content recording. Validate version, IDs, supported text shape, actual model identity, request parameters and response equality before certifying context. Malformed or missing requested traces fail explicitly before edits; legitimate incomplete traces stay ineligible. Legacy servers continue as before with `context_complete:false`.

Bind the trace to the submitted input: this bounded caller sends no examples, system override, max-lines, files, root or targets, and always uses `output_format:code`. Require exactly one system and one user message. The final user content must equal the submitted prompt plus `\n\n---\n\nReturn only code. No prose, no fences unless syntactically required by the language.`. This expansion is part of the version-1 protocol; unrelated cached input must not certify a candidate even if output/model match.

Record normalized requests once, then response and candidate, so the candidate digest uses the actual expanded input. Per-call trace IDs, usage and finish reason are provenance metadata, excluded from the conditioning digest to preserve valid repeated-context DPO pairs. The actual hashed upstream endpoint remains part of context identity, so reconfiguring a router behind an unchanged provider alias cannot silently pair different backends. Content still traverses the existing path allowlists, secret redaction and export gates. Every privacy transformation, including repository/home path substitution, marks the event redacted and excludes it from export. Passing verification alone never creates a human preference label.

The version-1 parameter shape requires `max_tokens`, `temperature`, `top_p`, `top_k`, `min_p` and `repeat_penalty`; only the optional `chat_template_kwargs` object containing boolean `enable_thinking` may accompany them. Reject unknown request parameters, including hidden tools or system inputs; text-only training cannot certify those inputs.

## Work and verification

- [x] Router worker: add failing wire-equality/default/validation tests; implement capture and structured reply; run `npm test`, `npm run typecheck`, `npm run build`.
- [x] Conduit worker: add failing discovery/transport/normalized-recording tests; implement trace validation and provenance; test legacy, malformed, incomplete, privacy and stable-context cases.
- [x] Integration: run built Conduit against built router with a synthetic HTTP backend; prove the recorded input equals the actual HTTP body and export is gated on explicit acceptance. No GPU dependency.
- [x] Independent review: check correctness, reachable transport paths and evidence locations; fix confirmed defects.
- [x] Delivery: rebuild both projects, run `make verify`, update status/memory/docs, and copy only tested router changes/build to its original installed checkout while preserving user files.

## Parallel execution contract

Two producers, one per repository; no shared write paths. Reports: `{component, files, tests:[{command,result}], concerns}`. Expected producer count: 2. Review on fresh context; source author never approves their own changes. Anchors are actual compiler/tests and the cross-project subprocess test. The validated candidate-postimage lesson remains a constraint; this change must not weaken patch attribution or verification.

## Execution evidence

- Both expected producer reports received (2/2); independent reviewer approved after fixes. Review caught input correspondence, upstream endpoint identity and strict parameter-schema gaps; integration caught unflagged path normalization. Each received a failing regression before correction.
- Router: 493 tests, typecheck and build pass in installed `/Volumes/Engineering/Icehunter/local-router`. Only README, client/server, new trace module and trace tests changed; existing untracked user files preserved. Existing client processes require reconnect to load the build.
- Conduit: `make build` and final `make verify` pass; log `/private/tmp/conduit-mcp-traces-verify-final.log`. Work stays in the existing `codex/verified-local-coding` managed worktree; original checkout remains untouched.
- Cross-process: all four cases pass (complete/explicit acceptance, secret redaction, absolute-path redaction, content-disabled). Evidence directory `/private/var/folders/kt/vwb3_29d0jn_khp2730cn3f40000gn/T/conduit-mcp-trace-17mti_u6`.
- Initial validation used the synthetic backend because the local endpoint was unavailable. After the user restarted it, a live Qwen smoke passed in 2.68s with two complete wire-equal traces and accepted-only SFT export (0 rows before a synthetic fixture label, 1 afterward). Evidence: `/private/tmp/conduit-live-trace-mix_vm5j`. No training, upload, promotion or commit performed.
