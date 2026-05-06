# Conduit-Owned State Plan

This plan captures the next stage after provider roles and account-scoped
configuration landed. The guiding rule is:

- Claude Code files are an import and compatibility source.
- Conduit files are the runtime source of truth after first load.

Conduit should read Claude global settings only when Conduit has no saved
configuration yet. Once `~/.conduit/conduit.json` exists, Conduit's saved
values must win on the next and every later startup.

## Current State

Conduit now owns these runtime files:

- `~/.conduit/conduit.json`
- `~/.conduit/mcp.json`
- `~/.conduit/plan_usage_cache.json`

`conduit.json` has a typed config surface with `schemaVersion`. Existing files
are upgraded in place on load without losing account metadata such as
`display_name`, `organization_name`, or `subscription_type`.

Conduit-owned writes now go to `~/.conduit/conduit.json` for:

- active provider and provider role bindings
- account metadata
- permission mode
- theme
- output style
- usage footer toggle
- onboarding completion
- MCP project-server approval state
- plugin enabled state
- `/config` reads and writes

Keybindings prefer `~/.conduit/keybindings.json` and fall back to
`~/.claude/keybindings.json` when Conduit has no keybindings file.

Plan usage cache prefers `~/.conduit/plan_usage_cache.json`, falls back to
`~/.claude/plan_usage_cache.json`, and writes only to Conduit. Backoff-only
cache entries should render as rate-limited instead of showing endless
`loading...`.

## Import Rules

On first load:

1. If `~/.conduit/conduit.json` does not exist, read Claude global settings from
   `~/.claude/settings.json`.
2. Write the imported values into `~/.conduit/conduit.json` with
   `schemaVersion`.
3. Load Conduit config as the active user-global config.

On later loads:

1. Do not merge Claude global settings into Conduit runtime settings.
2. Load project `.claude/settings.json` and `.claude/settings.local.json` where
   parity requires project-local behavior.
3. Apply Conduit user config after project settings so Conduit's saved runtime
   choices win.

This is intentional. If Claude Code changes its global settings later, Conduit
should not silently change providers, modes, themes, or other runtime state.

## Remaining Work

### 1. Move Session and Project State

Move Conduit session/project artifacts from Claude paths to Conduit paths:

- `~/.claude/projects` -> `~/.conduit/projects`
- resume history
- session summaries
- session memory
- auto-memory output
- RTK scans and session-derived stats

Keep a Claude fallback/import path so existing history remains visible. Once a
Conduit project directory exists, write and read the Conduit path first.

### 2. Untangle Plugin Storage

Plugin enabled state is now Conduit-owned, but install/cache directories still
lean Claude-compatible.

Decide between:

- shared plugin storage with Claude Code for compatibility
- fully separate `~/.conduit/plugins`

If fully separate, migrate:

- `installed_plugins.json`
- known marketplaces
- plugin cache directories
- plugin MCP discovery
- plugin install/uninstall commands

### 3. Finish Trust and MCP State Separation

MCP project-server approval state now writes to Conduit config, but trust/global
MCP mechanics still have Claude-era assumptions in comments and some paths.

Move or design:

- workspace trust state
- disabled MCP server state
- per-project MCP approval records

Likely destination:

- user-global state in `~/.conduit/conduit.json`
- project-specific state under `~/.conduit/projects/<sanitized-cwd>/state.json`

### 4. Stronger Provider Schema

Provider definitions should become explicitly typed rather than loosely keyed:

- `claudeSubscription`
- `anthropicApi`
- `openaiCompatible`
- `mcpLocal`

Add validation and migration for legacy keys like:

```text
claude-subscription.<account>.<model>
mcp.<server>
anthropic-api.<account>.<model>
```

The config API should reject broken provider references early and give a useful
message in Ctrl+M or `/config`.

### 5. UI and Command Follow-Through

Update visible labels and docs to say Conduit where the runtime file is now
Conduit-owned:

- `/config`
- settings panel Config tab
- settings path/status text
- MCP panel empty-state copy
- keybindings docs if copied into Conduit

The UI should be clear when a value came from an initial Claude import versus
when it is now Conduit-owned.

### 6. Optional: Raw JSON Exit

Keep raw JSON helpers only at the edges:

- first-run Claude import
- unknown-preserving migration helpers
- compatibility reads for Claude-owned project files

Normal Conduit writes should go through typed helpers. This makes future schema
migrations and config validation much easier.

## Suggested Next Slice

Move session/project state to `~/.conduit/projects`.

This is the largest remaining source of "why is Conduit touching Claude files?"
and it affects user-visible behavior:

- `/resume`
- session persistence
- memory
- RTK history
- stats panels

Do it with a compatibility fallback:

1. Resolve Conduit project dir first.
2. If missing, read Claude project dir.
3. Once Conduit writes a session for a project, prefer Conduit from then on.
4. Keep import behavior non-destructive.

