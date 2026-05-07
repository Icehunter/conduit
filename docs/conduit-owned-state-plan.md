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
- MCP disabled server state
- workspace trust state
- startup counter
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

Status: implemented for transcript, resume/search, session memory roots,
auto-memory roots, and session-derived stats fallback.

Conduit session/project artifacts now write to Conduit paths:

- `~/.claude/projects` -> `~/.conduit/projects`
- resume history
- session summaries
- session memory
- auto-memory output
- RTK scans and session-derived stats

Claude project history remains a read-only fallback/import path so existing
history stays visible. Session listing merges Conduit and Claude history while
deduping by session id with Conduit winning. Once the workspace trust gate is
satisfied, Conduit starts a best-effort background import of missing Claude
session files into Conduit's project store.

### 2. Untangle Plugin Storage

Status: implemented with fully separate `~/.conduit/plugins` storage.

Conduit now owns:

- `installed_plugins.json`
- `known_marketplaces.json`
- `install-counts-cache.json`
- plugin cache directories
- marketplace materialization directories
- plugin MCP discovery inputs
- plugin install/uninstall command writes

When `~/.conduit/plugins` does not exist, Conduit imports the legacy
`~/.claude/plugins` tree once and rewrites registry paths that pointed inside
Claude's plugin directory to the matching Conduit-owned paths. Legacy manually
dropped `~/.claude/plugins/<name>` directories remain a read-only fallback so
existing local plugin experiments still load until copied or installed through
Conduit.

### 3. Finish Trust and MCP State Separation

Status: implemented for workspace trust, startup count, MCP disabled server
state, and project-scoped MCP `.mcp.json` approval state.

These values now live in `~/.conduit/conduit.json` under
`projects[abs-cwd]`. Claude's `~/.claude.json` is read only as an import or
compatibility fallback for existing trust/disabled state; Conduit writes do not
modify it.

### 4. Stronger Provider Schema

Provider definitions should become explicitly typed rather than loosely keyed:

- `claudeSubscription`
- `anthropicApi`
- `openaiCompatible`
- `mcpLocal`

Current `conduit.json` provider kind values intentionally remain the existing
kebab-case wire strings while the provider UI and role picker are landing:
`claude-subscription`, `anthropic-api`, `openai-compatible`, and `mcp`. The
camelCase names above are a future schema migration, not a prerequisite for
Gemini/OpenAI-compatible configuration support.

Add validation and migration for legacy keys like:

```text
claude-subscription.<account>.<model>
mcp.<server>
anthropic-api.<account>.<model>
openai-compatible.<credential>.<model>
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

Untangle plugin storage.

Session/project state, trust/MCP runtime state, and plugin install/cache state
are now Conduit-owned. The next boundary is provider schema validation and UI
follow-through: make provider definitions strongly typed, reject broken role
references early, and finish visible Conduit-owned path labels in settings and
config surfaces.
