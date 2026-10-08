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

OpenAI-compatible providers use the same provider map, with `baseURL` carrying
the OpenAI-compatible endpoint. Gemini can be represented this way:

```json
{
  "providers": {
    "openai-compatible.gemini-personal.gemini-2.5-pro": {
      "kind": "openai-compatible",
      "model": "gemini-2.5-pro",
      "credential": "gemini-personal",
      "baseURL": "https://generativelanguage.googleapis.com/v1beta/openai/"
    }
  },
  "roles": {
    "planning": "openai-compatible.gemini-personal.gemini-2.5-pro"
  }
}
```

### Driving a Local Model Through the Full Agent Loop (not LocalImplement)

`LocalImplement`/`local-router` is a deliberately bounded, single-shot draft
helper (see "Observed Router Contract" above) — the local model never sees
tool results, so it can't self-correct across turns. A local llama.cpp/LM
Studio endpoint can instead be configured as a normal `openai-compatible`
provider and assigned to a role (e.g. `main` or a dedicated role). Conduit's
existing OpenAI-compatible adapter (`internal/api/openaicompat.go`) then
drives it through the same multi-turn tool-use loop used for Claude/Gemini —
the local model gets real `Edit`/`Bash`/`Read` tool access, sees actual
test/build output, and can retry across turns. This is the mechanism that
gives pi.dev-style "multi-turn re-eval" behavior; verified live against a
qwen3.8-27b LM Studio instance (turn 1 issues a real tool call, turn 2
consumes the fed-back tool result and produces a final answer).

Reasoning models such as qwen3.8-27b need `chat_template_kwargs:
{"enable_thinking": false}` in the request body or they spend the whole
token budget on hidden reasoning and return empty content — the same
workaround `local-router/src/local-client.ts` already applies. Conduit's
`ExtraBody` provider field carries this through:

```json
{
  "providers": {
    "openai-compatible.local-lmstudio.qwen3.8-27b": {
      "kind": "openai-compatible",
      "model": "qwen3.8-27b",
      "credential": "local-lmstudio",
      "baseURL": "http://192.168.0.71:1234/v1",
      "extraBody": {
        "chat_template_kwargs": { "enable_thinking": false }
      }
    }
  },
  "roles": {
    "implement": "openai-compatible.local-lmstudio.qwen3.8-27b"
  }
}
```

`credential` still needs a stored alias (`settings.SaveProviderCredential`)
even though LM Studio doesn't enforce auth — any placeholder value works.
`extraBody` is config-file-only for now (no Ctrl+M/`/providers` UI field
yet); hand-edit `~/.conduit/conduit.json`.

`extraBody` is applied as a JSON merge patch (RFC 7396): nested objects merge
key by key with the fields conduit generates, any other value replaces the
generated one, and `null` deletes it (e.g. `"stream_options": null` for a
server that rejects it). So `chat_template_kwargs` above is merged with
conduit's own `chat_template_kwargs.preserve_thinking`, not swapped for it.

#### Reasoning on OpenAI-compatible servers

- **Effort.** `/effort` reaches the model as `reasoning_effort`: a thinking
  budget of 8000+ tokens is `high`, 2000+ `medium`, anything lower `low`;
  `normal` (no budget) sends nothing. It goes only to reasoning-capable model
  families (o-series, gpt-5, gpt-oss, DeepSeek, Qwen3/QwQ, Grok 3 mini/4,
  Gemini 2.5/3, GLM 4.5+, Kimi K2 thinking) and to local/LAN servers; a 400
  that names `reasoning_effort` is retried once without it and the model is
  remembered. It is skipped when `extraBody` turns
  thinking off (`chat_template_kwargs.enable_thinking: false`, top-level
  `enable_thinking: false`, `thinking.type: "disabled"` or
  `reasoning.enabled: false`), and an `extraBody` `reasoning_effort` wins
  (`null` removes it). The Responses transport sends the same scale as
  `reasoning.effort`.
- **Streaming.** `delta.reasoning_content` (llama.cpp, vLLM, DeepSeek, Qwen,
  Kimi) and `delta.reasoning` (OpenRouter, Ollama, Groq) become a thinking
  block. So does a `<think>…</think>` block the server leaves in
  `delta.content` (no reasoning parser), split live across chunks. A
  `<think>` counts only in leading position (nothing visible yet) or right
  after a `</think>`; elsewhere it is prose, as are tags inside Markdown code
  spans or fences. For reasoning-capable models whose template puts `<think>`
  in the prompt (local hosts with thinking requested only), output up to a bare
  `</think>` is thinking; it is held until that tag, a `<think>`, a reasoning
  field or tool call, the end, or 2 KiB. The official OpenAI endpoint is
  never split. A host that sends a finish_reason but neither usage nor `[DONE]`
  and keeps the connection open is cut off 2 s after the finish_reason.
- **Replay.** The provider field `reasoningReplay` decides when an assistant
  turn's reasoning goes back as `reasoning_content` (or `reasoning`, when that
  is the field the server streams). Without it, Qwen3 / DeepSeek-R1 / GLM chat
  templates re-render prior turns without their `<think>` block and llama.cpp
  re-processes the whole prompt every turn.
  - `auto` (default): every turn for localhost / LAN / `*.local` servers and
    for DeepSeek and MiMo models (they require it); tool-call turns only for
    Kimi models and OpenRouter (they require it there); never for Claude
    models, whose reasoning only replays with a signature.
  - `on`: every turn that has reasoning, on any server. Use it for a
    self-hosted server on a public hostname.
  - `off`: never.

  Only reasoning that came from an OpenAI-compatible stream is replayed;
  Claude's signed thinking stays on the Anthropic wire. When replay applies to
  every turn and the model is Qwen, conduit also sends
  `chat_template_kwargs.preserve_thinking: true`, which makes Qwen3.6+
  templates keep `<think>` on older turns as well (oh-my-pi `ac958d5905`,
  `3cc61d3014`).

```json
{
  "kind": "openai-compatible",
  "model": "qwen3.8-27b",
  "credential": "local-lmstudio",
  "baseURL": "http://192.168.0.71:1234/v1",
  "reasoningReplay": "auto"
}
```

### Tools for models without native tool support

The provider field `toolCallFormat` is `native` (default: the `tools` request
field), `hermes`, `qwen3` or `auto`. For `hermes` and `qwen3` conduit sends no
`tools`: the schemas and the `<tool_call>` calling rules go into the system
prompt, and `internal/api/texttools.go` parses the reply. The parser runs on
the visible text after the `<think>` split (a call inside reasoning is not a
call) and is chunk-boundary safe and linear in the input. A call must start a
line, or end the reply, so prose or file content that mentions the tag stays
text, and fenced blocks are skipped (`FuzzTextToolParser` checks chunking invariance and that a
stray backtick never hides a later call). Prior calls and results are
sent back as text in the same syntax. `qwen3` differs from `hermes` only in a
prompt line asking for reasoning in `<think>` before calls. `auto` stays
native until the server rejects `tools` with a "does not support tools" style
400/422, then uses `hermes` for that model; a missing `tool_calls` in a reply
is not a signal. Not done: the XML-style `<function=…>` qwen variant, a
`<tool_response>` fabrication guard, and the other oh-my-pi syntaxes (glm,
kimi, deepseek, harmony, gemma).

The provider can be saved, validated, shown in Ctrl+M, and assigned to a role.
`/providers` opens the Settings Providers tab to add a Gemini/OpenAI-compatible
provider; the API key is stored through Conduit's secure storage under the
credential name rather than written into `conduit.json`. `account` is only for
OAuth-backed Claude/Anthropic accounts; `credential` is a local secret alias for
API-key-backed providers.

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
- background: memory extraction, session-memory updates, auto-dream
  consolidation, compaction helpers, and Task/SkillTool sub-agent calls
- planning: plan mode and architecture/planning prompts

Current implementation:

- The foreground loop keeps using the mode-selected provider/model.
- Background helpers resolve `roles.background` dynamically when the role points
  at a Claude/API-shaped provider.
- MCP/local background providers are intentionally ignored until the true
  provider abstraction exists, because these helpers currently call the
  Anthropic Messages API directly.
- Task and SkillTool sub-agents use the background model so side work does not
  change the visible chat model.

This can later feed Ctrl+M and `/model` so users can switch a role rather than
only switch one global Claude model.

### Slice 6: True Provider Abstraction

Add a provider interface only after the above surfaces are stable.

Likely provider types:

- `claude-subscription`: current OAuth flow
- `anthropic-api`: Anthropic Messages with `x-api-key`
- `openai-compatible`: OpenAI-compatible HTTP API
- `mcp-local`: local router through MCP

First account-backed step:

- Claude.ai and Anthropic Console logins are now persisted with an account kind.
- Claude.ai accounts continue to use OAuth bearer auth and the Claude
  subscription provider kind.
- Anthropic Console accounts use their minted API key when available and persist
  as `anthropic-api` providers.
- Model/role assignment preserves the active account-backed provider kind, so
  choosing a model while signed into Console does not silently write it back as
  a Claude subscription provider.
- Ctrl+M and `/models` label the model section from the active account kind:
  `Claude Subscription` for Claude.ai and `Anthropic API` for Console. Picker
  values carry the provider kind through selection.
- Welcome card, `/status`, and the usage footer use the same provider display
  labels, e.g. `Claude Subscription · <model> · <email>`,
  `Anthropic API · <model> · <email>`, or `MCP · <model> · <server>`.
- OpenAI-compatible providers now have config-level shape (`baseURL`,
  `credential`, `model`) plus picker/role plumbing, so Gemini/OpenAI-compatible
  entries do not fall through to Claude labels. Display strings include the
  credential alias so multiple Gemini/OpenAI-compatible entries can be
  distinguished. The adapter translates Conduit's Anthropic-shaped tool
  definitions and streamed OpenAI-compatible `tool_calls` into the existing
  `tool_use` loop, so Gemini/OpenAI-compatible chat can participate in normal
  agent tool execution.

This is the large refactor because Conduit currently assumes Anthropic wire
format in parts of its API client, thinking config, rate-limit handling, and
usage accounting.

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
