---
paths: "internal/tui/**"
---

# Bubble Tea v2 TUI Rules

## Core Discipline

`Update()` must be **pure and non-blocking**. It receives a `tea.Msg`, returns `(Model, tea.Cmd)`. Any side-effect (I/O, goroutine, timer) must go through a `tea.Cmd`.

```go
// WRONG — blocking in Update
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    data, _ := os.ReadFile("...") // blocks the event loop
    ...
}

// RIGHT — dispatch as Cmd
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    return m, func() tea.Msg {
        data, err := os.ReadFile("...")
        return fileLoadedMsg{data: data, err: err}
    }
}
```

## v2 API Changes (from v1)

- `tea.KeyMsg` → `tea.KeyPressMsg`
- `case " ":` → `case "space":`
- `View()` returns `tea.View` (not `string`); set `v.AltScreen = true` and `v.KeyboardEnhancements.DisambiguateEscapeCodes = true`
- `tea.WithAltScreen()` option removed — declare alt-screen in `View()`
- `viewport.New(w, h)` → `viewport.New(viewport.WithWidth(w), viewport.WithHeight(h))`
- `m.vp.Width = x` → `m.vp.SetWidth(x)`, reads: `m.vp.Width()`
- `textarea.Style` → `textarea.StyleState`; `ta.FocusedStyle` → `ta.Styles.Focused`

## Panel / Overlay Pattern

Overlays (MCP panel, plugin panel, settings panel, doctor panel) follow this pattern:

1. State lives in a `*xyzPanelState` field on `Model` (nil = closed)
2. Key dispatch in `handleKey` checks the panel first
3. Close by setting the field to `nil` — use a `done()` closure to `return` immediately after, preventing the nil field from being re-set by subsequent code
4. Render: `View()` checks `m.xyzPanel != nil` and returns the panel's full-screen render

## Styles

- All style vars live in `internal/tui/styles.go` — rebuild on theme change via `theme.OnChange`
- Use `fmt.Fprintf(sb, ...)` not `sb.WriteString(fmt.Sprintf(...))` (enforced by staticcheck QF1012)
- In functions taking `sb *strings.Builder` as a parameter, use `fmt.Fprintf(sb, ...)` (not `fmt.Fprintf(&sb, ...)`)
