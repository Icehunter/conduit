# Repository Layout

conduit is a parity-focused Go port of Claude Code. Keep modules small enough
to navigate, but prefer moves that preserve behavior over rewrites that make
the port harder to compare with the TypeScript source.

## Top Level

- `cmd/conduit/` is the binary entrypoint. It owns flag parsing and the REPL /
  print-mode control flow.
- `internal/app/` owns startup composition helpers used by the entrypoint:
  API client construction, auth/profile refresh helpers, tool registry
  construction, metadata, and session id generation.
- `internal/agent/` owns the main query loop, tool orchestration, compaction,
  and agent events.
- `internal/api/` owns Anthropic wire format, streaming, retry, proxy, and rate
  limit headers.
- `internal/tool/` defines the tool interface and registry.
- `internal/tools/` contains one package per Claude-compatible tool. Package
  directories use lowercase names ending in `tool` when that mirrors an upstream
  tool name.
- `internal/sessionstats/` loads Claude/conduit session usage stats from the
  stats cache or JSONL transcripts. TUI code renders these stats but does not
  scan the filesystem directly.
- `internal/tui/` owns Bubble Tea state, update dispatch, layout, panels, and
  terminal rendering.
- `internal/rtk/` owns all in-process command-output filtering. Do not trim
  shell output at call sites; add or adjust an RTK classifier instead.

## TUI Files

The TUI package intentionally stays a single package so unexported Bubble Tea
state and helper methods can move between files without creating import cycles.

- `model.go` contains the core model/config/message types, constructor, and
  `Init`.
- `update.go` contains the primary Bubble Tea message dispatch.
- `keyhandler.go` contains key dispatch, text submission, attachment sending,
  and interrupt/scrollback shortcuts.
- `commandresults.go` applies slash-command results to TUI state.
- `providers.go` resolves Claude/API/MCP provider state and role-specific model
  routing.
- `agentevents.go` maps streamed agent events into display messages.
- `layoutview.go`, `usagefooter.go`, `coordinatorfooter.go`, and `paint.go`
  handle frame layout and footer rendering.
- `attachmentspicker.go` handles paste expansion, at-mentions, slash command
  picker rendering, and file completion.
- `history.go` converts API history into display messages and persists/export
  helpers.
- `settingspanel.go` handles settings panel navigation, status, and config.
- `settingsstats.go` handles settings stats rendering, usage, charts, and
  factoids using data loaded by `internal/sessionstats/`.
- `pluginpanel.go` defines plugin panel state, with data loading, key handling,
  and rendering split into `pluginpaneldata.go`, `pluginpanelkeys.go`, and
  `pluginpanelrender.go`.
- `*panel.go` files implement individual full-screen or floating panels.

## Naming

Use all-lowercase concatenated file names — no underscores in regular `.go` files.
The sole exceptions are Go's built-in conventions: `_test.go` (test files) and
`_<goos>.go` / `_<goarch>.go` build-constraint suffixes (`_unix.go`, `_windows.go`).
Package directories should also be all lowercase with no underscores.

When a file move changes a path referenced by `PARITY.md` or `STATUS.md`, update
the relevant row in the same change.
