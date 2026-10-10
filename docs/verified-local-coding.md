# Verified local coding

This optional Conduit extension keeps a cloud model in the planner role and sends bounded implementation tasks to the configured local implementer. `LocalImplement` applies changes itself (see below). The new `CodingTask` tool owns application, verification, bounded repair and optional independent review.

## Enable in one repository

Add a `definitionOfDone` object to `.conduit/settings.json` (shared) or `.conduit/settings.local.json` (local). Existing user/project settings precedence applies; a later policy object replaces the whole earlier object. Missing/disabled policies retain the legacy completion path. Malformed existing policy files fail explicitly.

```json
{
  "definitionOfDone": {
    "enabled": true,
    "checks": [
      {"id": "build", "command": "make build", "timeoutSeconds": 180},
      {"id": "verify", "command": "make verify", "timeoutSeconds": 600}
    ],
    "maxRepairAttempts": 2,
    "totalTimeoutSeconds": 1200,
    "requireReview": true,
    "protectedPaths": ["**/*_test.go", "scripts/checks/**"]
  },
  "episodes": {
    "enabled": true,
    "includeContent": false,
    "allowedPaths": ["internal/**", "cmd/**"],
    "excludedPaths": ["**/fixtures/private/**"],
    "retentionDays": 30
  }
}
```

Use the provider picker to assign `main`/`planning` to the cloud, `implement` to the local endpoint, and optionally `review` to a different model. Missing review falls back to main/default; a broken explicit assignment fails. With verification enabled, changing the editing permission mode keeps the main planner selected. `CodingTask` invokes the implement role. Planning/council turns do not claim verified completion.

Trust the repository through the normal UI before executing checks. Existing Read/Edit and Bash (Windows: Shell) permissions apply separately. Headless operation cannot answer approval prompts; configure only the rules you intend to allow.

```sh
conduit code --file internal/example/example.go "Implement this bounded change"
```

`--file` repeats, includes new files, and is relative to the Git worktree root. Tasks accept at most 32 UTF-8 files, 64 KiB of initial source and a 16 KiB task prompt. The normal TUI and enabled `--print` path expose the same tool and completion enforcement. `code` exits nonzero for blocked/incomplete work.

## LocalImplement and the implement guard

`LocalImplement` no longer returns drafts. It takes a required `targets` list (1-20 repository-relative files to create or modify) and calls the local-router `local_implement` tool in apply mode: the server asks the model for edit blocks, writes only those files, and returns `[local-router] APPLIED: wrote N file(s) ...` plus the diff. Review the diff; you may fix only those target files afterward.

Requirements: the local-router instance must advertise `targets` (it does so only when `writeRoots` is configured) and `root`. If it does not, the call fails with an explicit error; there is no draft fallback. Router errors (for example `writeRoots` missing) are returned verbatim. Each target is permission-checked as `Edit` on its absolute path before the server is called, so deny rules, protected paths and approval prompts apply as for `CodingTask`.

The guard is about routing code edits, not about locking the main model down. While an MCP implement provider is reachable:

- Code edits go through `LocalImplement`. The main model may use Edit/Write/MultiEdit/HashEdit/NotebookEdit and AstGrep rewrites only on files the implementer touched in this session: `LocalImplement` targets that were APPLIED and files written by `CodingTask`.
- Raw MCP implement tools (for example `mcp__local-coder__local_implement`) are refused; call `LocalImplement`.
- Everything else is unchanged: the main model can still read files, run commands (Bash goes through the normal permission gate, so git add/commit/push, mkdir, `gofmt -w` and the like behave as in any session) and call other tools, subject to the usual permission rules.
- The edit restriction holds in every permission mode (including auto and accept-edits) and in sub-agents.

Reachability is evaluated on every tool call. An MCP implement provider counts as reachable when a connected MCP server currently exposes its implement tool (the same check that registers `LocalImplement`); with a `rolePools` pool for `implement`, any reachable member is enough. If the local implementer is not running, has not connected yet, or drops mid-session, the guard is inactive and the main model edits files directly. The first edit made during an outage carries a one-line notice in its tool result ("local implementer is unreachable"); it repeats only after the implementer has come back and gone down again. `CodingTask` uses the same implement role and the same check, so an unreachable provider never leaves it as the only path.

When the implement provider is the one the agent runs on, or no implement role is configured, the guard is inactive.

## Completion contract

The runtime captures sources, asks for constrained JSON edits, checks source preimages and permissions, applies edits, executes every required command, and feeds failures to the same implementer. A review receives current sources, cumulative task patch and actual check evidence. After review, checks run again against the same workspace digest. A model's text alone never establishes completion.

The final loop callback flushes explicitly approved staged edits before checks. Dismissal/cancellation is not approval. Limits, interruption, missing output and yielded turns remain incomplete. Failed checks have bounded output, deadlines and process-group cancellation. Configure foreground commands; deliberately detached processes are outside the cancellation guarantee, especially on Windows. Dirty worktrees are allowed; no reset or rollback overwrites external edits. Partial I/O failures are surfaced.

Check strings and protected file contents are frozen at session start. Makefile/makefile/GNUmakefile, package.json, pyproject.toml, Cargo.toml, and project policy files are always protected. Add test fixtures and scripts to `protectedPaths`; patterns use `**`. Automatic tasks cannot modify protected definitions. Change them explicitly and restart to accept a new policy. Checks that modify tracked/nonignored source are rejected. Ignore ordinary build artifacts in Git.

Participating coding operations share a workspace lock. Registered background jobs, subagents, workflows, hooks and teams block final completion while active. Fingerprints catch editor changes during checks; this is not an OS sandbox or a lock on external editors/processes. Switching worktrees requires restarting the verified runtime.

## Multiple endpoints

Each existing provider registration may have `endpointId`. Old provider keys are unchanged when it is absent. Distinct IDs allow the same model on several OpenAI-compatible URLs or separately registered MCP servers. `credential` remains an existing secure-store alias; never put API keys in project settings.

```json
{
  "providerPools": {
    "local-coders": {
      "members": ["REGISTERED_PROVIDER_A", "REGISTERED_PROVIDER_B"],
      "maxConcurrent": {"REGISTERED_PROVIDER_A": 1, "REGISTERED_PROVIDER_B": 1}
    }
  },
  "rolePools": {"implement": "local-coders"}
}
```

SQLite assigns new session/role pairs using available capacity and keeps their endpoint affinity across requests/restarts. A full pinned endpoint queues; disappearing endpoints fail explicitly. Request leases expire after crashes and renew during generation. Use the same physical `endpointId` for registrations sharing a GPU so capacity is shared. Capacity counts Conduit requests using this routing store; external Codex/ChatGPT traffic requires server telemetry and is not counted. Pools support bounded implement/review calls; streaming agent-role pools explicitly fail instead of pretending to hold a request lease.

## Episode data and improvement

Journals live under `$CONDUIT_CONFIG_DIR/coding/episodes` (default `~/.conduit/coding/episodes`). A coding episode is one bounded implementation task, with actual known model conditioning, provider metadata, responses, candidate patches, verification evidence, reviewer decisions and terminal status. It is separate from mutable chat transcripts. Arbitrary chat/subagent/tool trajectories are not treated as complete SFT examples.

Content recording requires both `includeContent:true` and matching allowed source paths. Metadata-only recording cannot produce training examples. All event payloads pass through secret/private-reasoning sanitization before disk; excluded sources suppress the entire payload, including labels. Redacted/incomplete traces are withheld from export. This reduces accidental disclosure; source-bearing datasets remain private code and are not anonymized public data. Nothing is uploaded.

Updated local-router servers expose opt-in [structured request traces](mcp-request-traces.md). Conduit validates and records their actual system/profile-expanded messages and sampling parameters; complete, unredacted traces can satisfy the context requirement for training export. Older servers and incomplete traces remain ineligible. Inlining files alone does not prove full conditioning. Direct OpenAI-compatible text calls without unknown request transformations can also produce complete examples. Missing immutable model revisions are not invented.

Explicit human acceptance is recorded with `conduit episodes accept/reject`; verification or dismissed diffs never supply a human preference. Model review can be opted into for separately labeled SFT. DPO requires explicit human choices with identical context and base: failed draft versus repaired draft with extra diagnostics is not automatically a preference pair. All tasks sharing repository root commits are grouped together across clones/worktrees for splitting; unrelated copied code still requires a maintained held-out registry.

See [training and evaluation](training.md) for CLI commands, pinned evaluation manifests and the offline QLoRA/adapter-merge pipeline. Evaluation disables coding capture. Training, uploads and model promotion are never automatic. A [single-endpoint Qwen3.8-27B MCP repair smoke](verified-local-live-smoke.md) passed on 2026-10-08. GPU training, other live providers and multi-endpoint load distribution still require separate smoke tests on the target machine.
