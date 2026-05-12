# Tree-Based Tool Rendering

Conduit uses Crush-inspired tree rendering for tool calls, showing hierarchical output with box-drawing characters instead of simple space indentation or ornamental separator lines.

## Visual Style

### Sequential Tool Calls (Compact View)
Instead of horizontal separators between each tool:
```
  ├─ ✓ BashTool ran · make build
  ├─ ✓ GrepTool searched · pattern="TODO" 
  ╰─ ✓ ViewTool read · internal/tui/render.go
```

### Expanded Tool Output
When a tool has output, it appears as a child in the tree:
```
  ✓ BashTool ran 1.2s · go test ./...
  ╰─  ok  internal/agent
      ok  internal/api
      ok  internal/tools
```

### Tree Connectors
- `╰─` — Last item in a group (last tool call or final output line)
- `├─` — Middle item in a group
- `│`  — Continuation line (for deeply nested structures)

## Benefits

1. **Less visual noise** — No horizontal separator lines `───────` between tool calls
2. **Clearer grouping** — Tree structure shows related tool calls at a glance
3. **Consistent with Crush** — Matches Crush's proven UX patterns
4. **Scannable hierarchy** — Box-drawing characters guide the eye

## Click to Expand

Since there may be multiple expandable items on screen simultaneously, the interaction hints say:
- `[click to expand]` — Not "space to expand" (ambiguous which item)
- `[click to collapse]` — Clear per-item interaction

## Implementation

- **Grouping**: `refreshViewport()` in `layoutview.go` groups consecutive `RoleTool` messages
- **Rendering**: Each tool in a group gets a tree prefix (`├─` or `╰─`)
- **Tree package**: Uses `lipgloss/v2/tree` with custom `crushTreeEnumerator()`

See:
- `internal/tui/layoutview.go:refreshViewport()` — groups tool calls
- `internal/tui/render.go:renderToolMessageWithPrefix()` — applies tree prefix
- `internal/tui/render.go:crushTreeEnumerator()` — emits `├─` / `╰─`
