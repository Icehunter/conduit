# Diff-First Review Gate — Design Spec (v1.6)

**Status:** Design only. No implementation in v1.5.  
**Author:** Agent-authored after council sequencing discussion (2026-05-08)  
**Supersedes:** None — this is the first design pass.

---

## Problem

Plan-approval exists but it gates the *intent*, not the *outcome*. The user approves what the agent said it would do, then watches all the edits land on disk before they've seen a single diff. The approval comes too early.

Diff-First Review Gate moves approval to the right moment: **after the agent has computed every change, before any of it hits disk**.

---

## User-facing behavior

1. Agent completes a multi-file task (e.g. via FileEditTool × N, FileWriteTool × M).
2. Instead of writing immediately, changes are held in a per-session **pending-edits table** (in-memory; keyed by absolute path).
3. When the agent's turn ends, the TUI shows a new **Diff Review overlay** — a full-screen modal listing every pending file.
4. The user navigates file-by-file:
   - **Approve file** — change is applied to disk.
   - **Request change** — file is returned to the agent as a follow-up instruction.
   - **Revert file** — pending change discarded; original not touched.
   - **Approve all** — bulk-apply.
   - **Revert all** — discard everything.
5. Approved changes are written atomically (temp-file rename). The session JSONL records which chunks were approved and which were reverted.

---

## Permission mode interaction

| Mode | Behaviour |
|------|-----------|
| `auto` (bypass) | Diff overlay is **skipped**; changes land immediately. This is the existing behaviour and must not regress. |
| `accept-edits` | Diff overlay fires for every multi-file write batch. The overlay is the "accept" step. |
| `default` | Unchanged — per-call permission prompts still fire at the tool level. Diff overlay is **not** shown (user already approved per call). |

> Rationale: `accept-edits` mode was always semantically "let writes through but ask about shell calls". The diff overlay completes that promise by showing *what* was written rather than just asking *whether* to write.

---

## Architecture

### Pending-edits table (`internal/pendingedits/`)

```go
type Table struct {
    mu      sync.Mutex
    entries map[string]*Entry  // keyed by abs path
}

type Entry struct {
    Path        string
    OrigContent []byte  // nil when file is new
    NewContent  []byte
    Op          Op      // OpWrite | OpEdit | OpDelete
    ToolName    string  // "FileEditTool" | "FileWriteTool" etc.
}

type Op int
const (
    OpWrite Op = iota
    OpEdit
    OpDelete
)
```

`Table` is a session-scoped singleton (same lifecycle as the agent loop). FileEditTool and FileWriteTool call `table.Stage(entry)` instead of writing directly. A `Flush(paths)` method writes only the approved subset atomically.

### FileEditTool / FileWriteTool changes

- Both tools gain an optional `Stager` interface injection at construction time:
  ```go
  type Stager interface { Stage(e pendingedits.Entry) }
  ```
- When `Stager == nil` (auto mode, background sub-agents): existing write-direct path unchanged.
- When `Stager != nil` (accept-edits mode, foreground agent): stage instead of write. Tool result text says `"Staged: <path> — awaiting review"`.

This keeps the tool implementations clean and testable in isolation.

### Diff rendering (`internal/tui/diffreview.go`)

New TUI overlay following the panel pattern from `planApproval` and `mcpPanel`:

- State struct `diffReviewState` holds the pending-edits list + cursor.
- Per-file diff rendered as unified diff (color-coded + / - lines). No external diff binary — computed in-process via a simple Myers diff over lines.
- Mouse-scrollable per-file viewport.
- File list on the left (mini panel), diff on the right.
- Key bindings: `↑/↓` navigate files, `a` approve, `r` request change, `x` revert, `A` approve all, `X` revert all, `Esc` cancel (implicit revert all).

### Session JSONL additions

New record type `pending_edit_result`:
```json
{
  "type": "pending_edit_result",
  "path": "internal/auth/middleware.go",
  "op": "edit",
  "action": "approved",      // "approved" | "reverted" | "requested"
  "tool_name": "FileEditTool",
  "ts": "2026-05-08T12:34:56Z"
}
```
Written after each file decision. Enables `/diff` and post-session audit to show which agent writes the user actually accepted.

### Diff Review trigger

`internal/tui/run.go` — at end-of-turn (after the agent loop's `OnEndTurn` fires):
1. If `mode == accept-edits` AND `pendingEdits.Len() > 0`:
2. Send `diffReviewAskMsg{entries: pendingEdits.Drain()}` to the TUI.
3. TUI opens the overlay; blocks via channel until user finishes review.
4. Flush approved entries, discard reverted entries, queue requested-change entries as a follow-up user message.

---

## Open questions

1. **Partial approval mid-edit-chain:** If the agent made 5 edits to `middleware.go` across multiple FileEditTool calls, they all stage separately. The diff overlay shows them merged. If the user reverts the middle two, the pending changes may be incoherent — do we warn or allow? *Proposal:* Allow, but display "N staged patches — approving all or none recommended" when a file has >1 pending entry.

2. **Parallel tool calls:** Agent may call FileEditTool × 3 concurrently. All 3 stage concurrently (Table is mutex-protected). The overlay shows all 3 as separate entries. This is safe.

3. **Hook interaction:** `PostToolUse` hooks currently receive the tool output. In staging mode, the tool result says "staged" not "written". Hooks that pattern-match on write confirmations may need updating. *Proposal:* Deliver the hook *after* the file is actually flushed (approved), not after staging. New `PostFileFlush` hook event.

4. **Sub-agent writes:** Background sub-agents (council members, summariser) must NOT go through the staging table — they run in `ModeBypassPermissions` and their writes are intentionally immediate. The `Stager` injection handles this: sub-agents get `Stager == nil` at construction.

5. **`/rewind` interaction:** A rewind checkpoint taken during a staged-but-not-approved session would restore the pre-edit state, which is correct. No special handling needed.

---

## Test plan

- Unit: `pendingedits.Table` — concurrent Stage/Flush/Drain with -race.
- Unit: `diffreview` Myers diff — known inputs → expected `+/-` line output.
- Unit: FileEditTool with injected mock Stager — assert Stage is called, disk unchanged.
- Integration: fake agent turn ending with 3 staged edits → review msg fired → all approved → assert all 3 files written.
- Integration: revert path → assert no disk writes.
- Integration: `mode == auto` → assert staging is bypassed, disk written directly.

---

## LOC estimate

| Component | Est. LOC |
|-----------|---------|
| `internal/pendingedits/` (table, entry, flush) | ~250 |
| FileEditTool / FileWriteTool Stager injection | ~80 |
| `internal/tui/diffreview.go` (overlay, diff render) | ~700 |
| `internal/tui/update.go` additions | ~60 |
| `internal/tui/run.go` end-of-turn trigger | ~80 |
| Session JSONL additions | ~40 |
| Tests | ~400 |
| **Total** | **~1610** |

This is correctly sized as a standalone v1.6 milestone. It touches four packages that already have test coverage, so the risk is bounded.

---

## API surface changes needed before implementation

1. `FileEditTool.New()` / `FileWriteTool.New()` must accept an optional `Stager` — either via functional options or a second constructor `NewWithStager(s Stager)`.
2. `internal/app/registry.go` `BuildRegistry` needs the stager wired in when mode == `accept-edits`. The stager is created in `cmd/conduit/mainrepl.go` and passed through `RunOptions`.
3. `agent.LoopConfig` gains `Stager pendingedits.Stager` — nil for sub-agents.
4. New `tea.Msg` types: `diffReviewAskMsg`, `diffReviewDoneMsg`.
5. New `agent.OnEndTurn` hook fires *after* all tool results, *before* next user turn — this already exists; we add a `StagedEditsReady` callback to it.
