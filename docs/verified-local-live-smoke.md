# Single-endpoint live repair smoke

On 2026-10-08, the built `conduit code` command completed a real repair loop through local-router's `local_implement` MCP tool and the running `qwen3.8-27b` endpoint. Only synthetic Python source was sent. User/global configuration was unchanged; each run used a temporary repository and isolated Conduit/MCP settings.

## Exercise and evidence

The task deliberately instructed the first attempt to replace an `add(a, b)` stub with `return a - b`. The protected verifier ran `python3 -B check.py` against four addition cases. Its real failure (`expected 5, got -1`) was fed back to Qwen, which changed the implementation to `return a + b`. All four cases passed, and Conduit returned `status: verified`, `attempts: 2`.

| Check | Observed result |
| --- | --- |
| Initial live run | Verified in 8.48 seconds; independent audit found incorrect terminal attempt metadata |
| Repeated live run after correction | Verified in 3.88 seconds; two live generation calls |
| Protected verifier | Byte-identical before/after; SHA-256 `806f6c87fb2d8c040c6184e1456b7511e88136e613c439c54fa8bf41d89fb5cc` |
| JSONL sequence | Start, request, response, candidate, failed verification, repair request, response, candidate, passed verification, end |
| Terminal attempt | `attempt_id: "1"`, matching the second attempt |
| Routing | One persistent implement binding to `live-qwen-single`; zero outstanding request leases |
| Privacy/training eligibility | MCP requests retain `context_complete: false`; hidden router conditioning prevents training export |

The terminal event previously omitted its attempt field, so it defaulted to zero after successful repair. `TestRunRepairsLocalCandidateBeforeCompletion` reproduced the problem before the fix. The event now records the last attempted index. The regression passed after correction, followed by `make build` and `make verify` (zero lint issues, race-clean tests).

## Local artifacts

Temporary artifacts may be removed by system cleanup:

- Initial run: `/private/tmp/conduit-live-smoke-bh14up9g`
- Corrected run: `/private/tmp/conduit-live-smoke-fixed-az449_6m`
- Required checks: `/private/tmp/conduit-live-smoke-verify.log`

Each run directory contains `repo/`, `prompt.txt`, `run.py`, `result.json`, `stdout.json`, `stderr.log`, router diagnostics, and an isolated `config/coding/` with episode JSONL and routing SQLite evidence. The repeat run's journal is `config/coding/episodes/a4017a9a-dd4b-4984-8c88-3d158d55a712.jsonl`.

This validates one local endpoint, sticky binding and lease cleanup. It does not validate live load distribution across multiple endpoints, an automated cloud review call, or GPU training/export. Those remain separate checks; automatic review was disabled for this synthetic smoke.

## Follow-up with structured traces

After adding router trace support, the user restarted the endpoint and a second live fixture at `/private/tmp/conduit-live-trace-mix_vm5j` passed in 2.68 seconds (two calls, failed candidate followed by a verified repair). Both traces were complete and unredacted. The actual outbound messages/model/parameters matched the recorded requests exactly. This fixture used a path-free failure diagnostic so privacy normalization did not alter the model input.

Export returned no SFT rows until a synthetic acceptance label was appended in the temporary fixture; it then returned one row whose messages and completion matched the live evidence. No production labels or training run were created. See [request-trace validation](mcp-request-traces.md) for details; the earlier incomplete-context observations above describe the pre-trace runs.
