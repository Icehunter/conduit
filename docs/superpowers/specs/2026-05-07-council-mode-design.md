# Council Mode & `/feedback` Command — Design Spec

**Date:** 2026-05-07  
**Status:** Approved

---

## Overview

Two features:

1. **Council Mode** — a new first-class permission mode where planning tasks are debated by multiple LLMs before a plan is presented. The live debate streams into chat as labelled display-only messages. Main context cost is a single synthesis message.
2. **`/feedback` command** — opens a pre-filled GitHub issue URL in the system browser. No API keys required.

---

## 1. Council Mode

### 1.1 New permission mode

`ModeCouncil` joins the existing set in `internal/permissions/permissions.go`:

```go
ModeCouncil Mode = "council"
```

Behaviour while active:
- Read-only tools auto-allow (same as `ModePlan`)
- Mutating tools ask
- When the model calls `ExitPlanMode`, the **council flow** fires instead of the standard plan-approval picker
- After council reaches consensus the standard plan-approval picker appears (auto / accept-edits / default / reject)

### 1.2 Shift+Tab cycle order

```
default → plan → council → acceptEdits → bypass → default
```

`currentProviderRole()` in `internal/tui/providers.go` maps `ModeCouncil` → `RolePlanning` (read-only, no implementation tools).

### 1.3 Direct mode picker — `Ctrl+\` 

A new keybind opens a **mode picker overlay** (picker-above-input, same pattern as `/model` and `/theme`). Lists all five modes; current mode marked with `●`. One keypress to jump directly. Registered in `internal/tui/keyhandlerbuiltins.go`.

---

## 2. Council Model Roster

### 2.1 Model picker — Council tab

The `/model` picker gains a **Council** tab alongside the existing role tabs. In this tab:

- Space **toggles** a model into/out of the council roster (multi-select; `●` = selected, `○` = not)
- Enter confirms and closes
- 1 or more models may be selected
- With 1 model: debate is skipped; that model plans directly (no cross-model discussion)

### 2.2 Persistence

Council roster stored in `settings.json` under `roles.council` as a list of provider keys:

```json
{
  "roles": {
    "council": ["provider:gemini-flash", "provider:gpt-4o"]
  }
}
```

Existing `roles` map is `map[string]string` (single provider per role). Council needs `[]string`. Options:
- Add `councilProviders []string` as a separate top-level settings field alongside `roles`
- Store as a comma-joined string in `roles["council"]` (parsed on read)

**Decision:** add `CouncilProviders []string` as a dedicated field in `internal/settings/settings.go` `Merged` struct. Cleaner than abusing the string map.

### 2.3 Working-row badge

While council runs, the mode badge shows:
- `[council · 2]` (compact, width < 80)
- `[council · Gemini, GPT-4o]` (wide terminals)

---

## 3. Council Orchestration

### 3.1 Trigger

`ExitPlanMode.Execute()` in `internal/tools/planmodetool/planmodetool.go` checks `currentMode == ModeCouncil`. If true, it sends a `councilStartMsg` to the TUI instead of opening the plan-approval picker. The plan text from `ExitPlanMode` becomes the **seed prompt** for Round 0.

### 3.2 Round 0 — Parallel propose

All council models spawned as isolated sub-agents simultaneously:
- Mode: `ModeBypassPermissions`  
- Tools: read-only set only (`Read`, `Glob`, `Grep`, `WebFetch`, `WebSearch`)
- Prompt: seed plan + codebase context summary
- Client: per-model `api.Client` built from each provider's settings via `NewProviderAPIClient`

As each model responds, its output is sent to the TUI as a `RoleCouncil` message labelled with the model/provider name. These messages are **never appended to `m.history`** — zero main-context cost.

### 3.3 Rounds 1–N — Sequential critique

Each model receives all prior `RoleCouncil` messages and responds in turn. Any model may include `<council-agree/>` anywhere in its response to signal agreement.

**Consensus:** when all models have signalled `<council-agree/>`, or the max-rounds cap is reached (default: 4, configurable via `councilMaxRounds` in settings), the debate ends.

### 3.4 Synthesis

The primary Claude instance (main loop) receives a compact summary prompt:

```
The following council debate has concluded. Synthesise the agreed points 
into a single clear implementation plan:

<debate>
[all RoleCouncil messages]
</debate>
```

The synthesised plan is appended to `m.history` as a single `RoleCouncil` display message labelled `Council:`, then the standard **plan-approval picker** opens.

### 3.5 Cost display

The assistant-info row after the `Council:` message shows:
```
◇ Council · 3 models · 2 rounds · $0.18
```

Accumulated from each sub-agent's token usage reported back via the existing `pluginInstallMsg`/cost-tracking pattern.

### 3.6 Display-only message role

New `RoleCouncil` in `internal/tui/model.go`. Rendered with a model-label prefix in a distinct style (e.g. `styleModeYellow` for the label, `stylePickerDesc` for the body). Not included in `m.history` serialisation to the API.

---

## 4. New Components

| Component | File | Purpose |
|-----------|------|---------|
| `ModeCouncil` constant | `internal/permissions/permissions.go` | New mode |
| `CouncilProviders []string` | `internal/settings/settings.go` | Roster persistence |
| `councilMaxRounds int` | `internal/settings/settings.go` | Cap on debate rounds |
| `RoleCouncil` | `internal/tui/model.go` | Display-only message role |
| `councilStartMsg` | `internal/tui/model.go` | Triggers council flow from ExitPlanMode |
| `handleCouncilFlow()` | `internal/tui/council.go` (new) | Orchestrates rounds, streaming, consensus detection |
| Mode picker overlay | `internal/tui/pickerpanel.go` | `Ctrl+\` direct mode picker |
| Council tab in model picker | `internal/tui/pickerpanel.go` | Multi-select roster |
| `internal/browser/` | `browser.go` + `browser_unix.go` + `browser_windows.go` | Platform-aware browser open |

---

## 5. `/feedback` Command

### 5.1 Registration

New command in `internal/commands/misc.go` (alongside `/doctor`, `/status`).

### 5.2 Flow

1. AskUserQuestion panel opens: `"Describe the issue or feedback:"` (Tab to type; paste supported)
2. On submit, construct the GitHub issue URL:
   ```
   https://github.com/icehunter/conduit/issues/new
     ?title=[Feedback] <first 60 chars of text, URL-encoded>
     &body=<feedback text>\n\n---\nVersion: X.Y.Z | OS: darwin | Mode: council
     &labels=feedback
   ```
3. Open URL via `internal/browser.Open(url)`
4. Flash message: `"Browser opened — finish submitting on GitHub"`

### 5.3 `internal/browser` package

```
browser.go           — Open(url string) error (calls platform impl)
browser_unix.go      — //go:build !windows — exec.Command("open"/"xdg-open", url)
browser_windows.go   — //go:build windows  — exec.Command("cmd", "/c", "start", url)
```

`"open"` on macOS, `"xdg-open"` on Linux (detected via `runtime.GOOS`).

---

## 6. Settings additions

```json
{
  "councilProviders": ["provider:gemini-flash", "provider:gpt-4o"],
  "councilMaxRounds": 4
}
```

Both fields optional; default to empty roster (council mode with no roster = falls back to plan mode behaviour) and 4 rounds respectively.

---

## 7. Verification

1. **Mode cycling** — Shift+Tab from `plan` lands on `council`; another press goes to `acceptEdits`
2. **Mode picker** — `Ctrl+\` opens picker; selecting `council` transitions mode; badge updates
3. **Council tab** — open `/model`, switch to Council tab, toggle Gemini + GPT-4o with Space, confirm with Enter; verify `settings.json` has `councilProviders`
4. **Single-model council** — roster with 1 model; `ExitPlanMode` in council mode skips debate, goes straight to synthesis and plan-approval picker
5. **Full council flow** — roster with 2+ models; `ExitPlanMode`; verify Round 0 messages appear in chat labelled by model; verify critique rounds appear; verify `<council-agree/>` detection; verify synthesis message and plan-approval picker
6. **Main context isolation** — after council completes, check `m.history`; only the `Council:` synthesis message should be present, not the debate messages
7. **`/feedback`** — type `/feedback`, submit text, verify browser opens with correct pre-filled URL
8. **`make verify`** — zero lint errors, race-clean tests
