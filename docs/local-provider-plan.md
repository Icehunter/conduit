# Local Providers and Delegation Plan

This note captures the current plan for local model/provider work in Conduit.
The near-term goal is not to ship a public MCP provider. The local router is a
private/friends workflow, and Conduit should treat it as a configurable local
execution surface.

## Current Context

- Conduit is still primarily a Claude Code parity port.
- MCP hosting is already implemented in Conduit.
- The local router currently lives at:
  `/Volumes/Engineering/Icehunter/local-router`
- The local router currently exposes:
  - server key/name: `local-router`
  - tools: `local_direct` and `local_implement`
  - env vars: `LOCAL_LLM_BASE_URL` and `LOCAL_LLM_MODEL`
  - output tag: `<local_output>`
- The user-level `~/.claude.json` can point `local-router` at:
  `/Volumes/Engineering/Icehunter/local-router/dist/server.js`
- Current private LAN target example:

```json
{
  "mcpServers": {
    "local-router": {
      "command": "node",
      "args": [
        "/Volumes/Engineering/Icehunter/local-router/dist/server.js"
      ],
      "env": {
        "LOCAL_LLM_BASE_URL": "http://192.168.0.71:1234",
        "LOCAL_LLM_MODEL": "qwen3-coder"
      }
    }
  }
}
```

- Conduit's existing `/model` and Ctrl+M picker are Claude-model oriented.
- Conduit's main loop is Anthropic Messages-shaped, so full arbitrary provider
  support requires adapters for streaming, tool calls, token accounting, errors,
  and model capabilities.

## Observed Router Contract

The current router is a stdio MCP server running as a local Node subprocess on
the Conduit/Claude machine. It has no filesystem access. Its job is to relay
prompt text to an OpenAI-compatible `/v1/chat/completions` server, which may be
running on another private LAN machine such as a Windows PC.

Current tool inputs:

- `prompt`: required string. The complete prompt, including any file contents,
  instructions, diffs, or repo context the caller wants the local model to see.
- `system`: optional system prompt override.
- `output_format`: optional `code`, `diff`, or `explanation` directive.
- `mode`: optional `delegate` or `direct`.
- `include_review_reminder`: optional boolean wrapper override.

Current behavior:

- `local_direct` returns the raw model response by default.
- `local_implement` wraps the response in `<local_output>` and appends a review
  reminder by default.
- For Conduit's intended use, `local_implement` should be prompted with
  `output_format: "diff"` and should return a unified diff with no prose.
- `local_implement` does not read files. Conduit, Claude, or a future provider
  must read/select context and send it in the `prompt` field.
- The local-router review reminder has been made provider/model-neutral, and
  Conduit passes `include_review_reminder: false` for `LocalImplement` so the
  main loop owns review/integration policy.

## Clarified Use Cases

### 1. Target N Local Servers Directly

The user should be able to configure multiple local servers, each with a
different model, and target one explicitly:

```text
/local qwen "review this diff"
/local coder "implement the small parser fix"
/local fast "summarize this error"
```

This path should not require Claude to decide to call the local tool. It should
invoke the selected local endpoint directly and show the result in the TUI.

### 2. Let the Agent Loop Delegate to Local Servers

The main agent, whether Claude or another future provider, should be able to
offload work to configured local endpoints as tools:

```text
Use the fast local model to inspect the failing test output, then integrate its
findings into your final answer.
```

The local result should come back into the active loop as tool output, so the
orchestrating model can reason over it, merge it with repo context, and decide
what to do next.

This should avoid hardcoded review prompts such as "use Sonnet" when the target
model is explicitly local or configured differently.

Important boundary: `local_implement` is not intended to be a remote autonomous
agent. It is a bounded implementation-draft helper:

```text
Implement this feature with these requirements. Here are the relevant files,
helper functions, current diff, and constraints. Do not broaden scope. Return
a unified diff. No prose.
```

The orchestrator remains responsible for choosing context, applying edits,
running tests, and deciding whether the result is acceptable.

## What "Sidecar" Means Here

Sidecar means "local model endpoint beside the main agent," not a public product
surface. In practice, "local" means private/local-trust infrastructure: it may
run on the same MacBook or on a Windows PC elsewhere on the LAN.

It has two modes:

- Direct command mode: the user calls a local endpoint with `/local`.
- Delegated tool mode: the active agent calls a local endpoint as an MCP/tool
  during the loop.

The sidecar does not have to own the whole conversation initially. It can be
stateless per request, return a bounded answer, and let the main agent decide how
to use it.

## Default / Main / Background / Planning Roles

These roles are useful, but they are not required for the first local-server
slices.

- Default: the provider used in default permission mode.
- Main: the provider used for accept-edits/auto mode and future main-task
  orchestration.
- Background: cheaper/faster model for summaries, memory extraction, indexing,
  speculative checks, or helper tasks where perfect reasoning is not required.
- Planning: strongest model for plan mode, architecture decisions, or tasks that
  benefit from more reasoning before edits.

Example desired role mapping:

```json
{
  "models": {
    "default": { "provider": "mcp", "model": "qwen3-coder" },
    "main": { "provider": "claude-subscription", "model": "claude-sonnet-4-6" },
    "background": { "provider": "claude-subscription", "model": "claude-haiku-4-5-20251001" },
    "planning": { "provider": "claude-subscription", "model": "claude-opus-4-7" }
  }
}
```

This should be deferred until after the local server targeting path is solid.

## Proposed Config Shape

Start with a Conduit-local config file at `~/.conduit/conduit.json`, loaded
after Claude-compatible settings. Conduit does not need to preserve the
temporary `localMode` format. The durable shape is a named provider registry
plus role bindings. `activeProvider` remains as a compatibility/current-default
field and is mirrored into `providers` + `roles.default` when Conduit switches
the Default provider.

MCP server process configuration lives separately in `~/.conduit/mcp.json`,
loaded after Claude/project/plugin MCP sources so Conduit can add or override
servers without mutating Claude config:

```json
{
  "mcpServers": {
    "local-router": {
      "command": "node",
      "args": [
        "/Volumes/Engineering/Icehunter/local-router/dist/server.js"
      ],
      "env": {
        "LOCAL_LLM_BASE_URL": "http://192.168.0.71:1234",
        "LOCAL_LLM_MODEL": "qwen3-coder"
      }
    }
  }
}
```

Example:

```json
{
  "accounts": {
    "active": "syndicated.life@gmail.com",
    "accounts": {
      "syndicated.life@gmail.com": {
        "email": "syndicated.life@gmail.com",
        "added_at": "2026-05-06T04:50:00Z"
      }
    }
  },
  "activeProvider": {
    "kind": "mcp",
    "server": "local-router",
    "directTool": "local_direct",
    "implementTool": "local_implement",
    "model": "qwen3-coder"
  },
  "providers": {
    "claude-subscription.claude-sonnet-4-6": {
      "kind": "claude-subscription",
      "model": "claude-sonnet-4-6",
      "account": "syndicated.life@gmail.com"
    },
    "mcp.local-router": {
      "kind": "mcp",
      "server": "local-router",
      "directTool": "local_direct",
      "implementTool": "local_implement",
      "model": "qwen3-coder"
    }
  },
  "roles": {
    "default": "mcp.local-router",
    "main": "claude-subscription.claude-sonnet-4-6",
    "implement": "mcp.local-router",
    "planning": "claude-subscription.claude-opus-4-7",
    "background": "claude-subscription.claude-haiku-4-5"
  }
}
```

Ctrl+M is the role assignment UI. Tab cycles Default, Main, Background,
Planning, and Implement roles; Enter assigns the highlighted provider to the
current role. Permission modes select roles: default mode uses Default, plan
mode uses Planning, and accept-edits/auto use Main.

Claude subscription providers use the same shape with auth-specific fields:

```json
{
  "providers": {
    "claude-subscription.claude-sonnet-4-6": {
      "kind": "claude-subscription",
      "model": "claude-sonnet-4-6",
      "account": "syndicated.life@gmail.com"
    }
  },
  "roles": {
    "default": "mcp.local-router",
    "main": "claude-subscription.claude-sonnet-4-6"
  },
  "activeProvider": {
    "kind": "claude-subscription",
    "model": "claude-sonnet-4-6",
    "account": "syndicated.life@gmail.com"
  }
}
```

API-key providers can use `credential` later:

```json
{
  "providers": {
    "anthropic-api.default": {
      "kind": "anthropic-api",
      "model": "claude-opus-4-7",
      "credential": "default"
    }
  },
  "roles": {
    "planning": "anthropic-api.default"
  }
}
```

The server names map to existing MCP configuration. For example,
`local-router` normalizes to a Conduit MCP tool prefix like:

```text
mcp__local_router__
```

So `local-router/local_direct` becomes:

```text
mcp__local_router__local_direct
```

## Updated Implementation Plan

### Slice 1: Provider Switching

Make Ctrl+M and `/model` switch a full provider, not only a Claude model name.
The command should emit one provider-switch path for:

- Claude subscription providers
- MCP/local providers
- future API-key providers

Behavior:

- Persist the selected provider to `activeProvider`.
- Route normal chat through the selected provider.
- Store MCP/local chat results as assistant turns in conversation history so
  follow-up prompts keep the local context. Persist provider metadata beside the
  transcript entry so `/resume` can render local turns with the right label and
  formatting without sending that metadata to upstream APIs.
- Keep local debug commands dispatchable, but hide them from command discovery.

### Slice 2: Hidden Direct MCP Debug Calls

Keep direct local invocation as a temporary escape hatch:

```text
/local <prompt>
/local <target> <prompt>
```

These commands should be hidden from `/help` and the command picker once
provider switching works. They remain useful while developing the offload path.

For the current router, Conduit should call the tool with:

```json
{
  "prompt": "<user prompt>",
  "mode": "direct",
  "include_review_reminder": false
}
```

### Slice 3: Multiple Local Targets

Support N local entries cleanly:

```text
/local list
/local qwen <prompt>
/local fast <prompt>
/local status
```

Validation should report:

- target not found
- MCP server not connected
- tool not found
- tool returned an error

Current implementation:

- Ctrl+M lists MCP/local providers from both connected MCP servers and
  configured `providers` entries in `~/.conduit/conduit.json`.
- `/local list` reports configured and connected local targets.
- `/local <target> <prompt>` can target a configured MCP provider by server
  name and uses that provider's configured direct tool when present.
- `/model local:<target>` preserves configured MCP provider tool names instead
  of forcing `local_direct` / `local_implement`.

### Slice 4: Agent-Visible Local Delegation Tools

Expose configured local providers as agent tools, probably with stable names:

```text
LocalDirect
LocalImplement
```

or per-target tools:

```text
LocalDirect_qwen
LocalImplement_qwen
LocalDirect_fast
```

The tool descriptions should make the target model/provider explicit so Claude,
Anthropic API, OpenAI, or any future orchestrator can choose correctly.

The returned local output should be injected as normal tool output into the
agent loop. The orchestrator then integrates or rejects it.

Initial implementation:

- `LocalImplement` is registered when Conduit can resolve a connected MCP
  server exposing `local_implement`.
- It prefers `roles.implement`, otherwise falls back to a connected
  `local-router`/first available `local_implement` server.
- It resolves `roles.implement` dynamically for each tool description and
  execution, so Ctrl+M Implement role changes take effect without restart.
- It sends a bounded prompt to the MCP tool with `output_format=diff` and
  `include_review_reminder=false`.
- It returns draft output as normal tool output for the main agent to review.

For the current router, the agent-visible implementation path still needs
Conduit or the orchestrating model to assemble context. The router does not
fetch files. The first implementation should make this explicit in the tool
description and input schema.

Tool instructions should emphasize bounded scope:

- Send only the files/snippets/error output needed for this implementation.
- Include explicit requirements and non-goals.
- Ask for a unified diff, no prose.
- Tell the local model not to invent surrounding systems or do extra work.
- Treat the result as a draft that the orchestrator will review and apply.

### Slice 4: Remove Hardcoded Model Assumptions in Delegation Prompts

Status: implemented for local-router's wrapper and Conduit's `LocalImplement`
call path; keep auditing newly added prompt templates for provider-specific
assumptions.

Audit local-router prompt templates and Conduit-side prompt-injection commands
for hardcoded assumptions such as "Sonnet".

Replace those with:

- selected target name
- selected provider/model display name
- neutral wording like "the local model" or "the delegated model"
- a configurable reviewer role/model once model roles exist

This is required before local delegation feels honest.

### Slice 5: Role-Based Models

Add `main`, `background`, and `planning` model roles after local targeting is
working.

Initial wiring:

- main: normal chat loop
- background: memory extraction, compact helpers, low-risk helper tasks
- planning: plan mode and architecture/planning prompts

This can later feed Ctrl+M and `/model` so users can switch a role rather than
only switch one global Claude model.

### Slice 6: True Provider Abstraction

Add a provider interface only after the above surfaces are stable.

Likely provider types:

- `claude-subscription`: current OAuth flow
- `anthropic-api`: Anthropic Messages with `x-api-key`
- `openai-compatible`: OpenAI-compatible HTTP API
- `mcp-local`: local router through MCP

This is the large refactor because Conduit currently assumes Anthropic wire
format in its API client, streaming loop, thinking config, tool calls, rate-limit
handling, and usage accounting.

## Open Design Questions

- Should direct `/local` results be appended to the main conversation history by
  default, or only displayed as transcript/system output?
- Should `/local_implement` be a separate slash command, or should `/local
  --implement <target> <prompt>` be enough?
- Should project-scoped local provider config also exist, or is the user-level
  `~/.conduit/conduit.json` overlay enough for the first pass?
- Should per-target local tools be registered dynamically at startup, or should
  Conduit expose one generic local tool that takes `target` as an input field?
- Should local-router startup/env management remain outside Conduit, or should
  Conduit eventually help launch and supervise local servers?

## Recommendation

Build in this order:

1. Direct `/local`.
2. Multiple local targets.
3. Agent-visible local delegation tools.
4. Prompt/model-assumption cleanup.
5. Role-based model selection.
6. Full provider abstraction.

This keeps the first useful feature small while preserving a path toward
Crush-style provider/model flexibility.
