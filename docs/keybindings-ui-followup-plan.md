# Keybindings UI Follow-Up Plan

Status: planned.

Conduit already loads user keybindings from `~/.conduit/keybindings.json`, falls
back to `~/.claude/keybindings.json` when Conduit has no file, resolves active
bindings at runtime, and shows the active resolver in `/keybindings`.

The missing piece is display consistency: some UI copy still hardcodes default
shortcuts even when a user remaps those actions.

## Goal

Every UI hint that names a configurable action should render the active
keystroke for that action instead of a hardcoded default.

Examples:

- Welcome card Start rows:
  - command picker
  - model picker
  - permission mode cycling
- Help overlay shortcut table
- Picker footer hints where actions map to `select:*`
- Settings/plugin panel footer hints where actions map to configurable actions

## Proposed Shape

Add a small TUI helper around the active keybinding resolver:

```text
ShortcutLabel(contexts, action, fallback) string
ShortcutLabels(contexts, action, fallback...) []string
```

The helper should:

- search active contexts in resolver order
- skip explicitly unbound bindings
- return a stable, human-readable key string
- fall back to current literal copy when no binding exists
- leave non-configurable keys hardcoded, such as terminal scroll keys or text
  editing keys that are handled directly by Bubble Tea widgets

## Slices

1. Add resolver reverse lookup from action -> bindings.
2. Add TUI formatting helper for display labels.
3. Wire welcome card rendering through `Model` so it can access live bindings.
4. Replace help overlay hardcoded configurable rows with action-backed rows.
5. Update picker/settings/plugin footer hints where the action is configurable.
6. Add focused tests for remapping `chat:modelPicker`, `chat:commandPicker`,
   and `chat:cycleMode` and verifying welcome/help display changes.

## Non-Goals

- Do not make every raw key mention dynamic immediately. Some hints describe
  component-local behavior, not user keybindings.
- Do not add chord support in this slice.
- Do not change keybinding storage again.
