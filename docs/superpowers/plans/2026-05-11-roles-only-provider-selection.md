# Roles-only provider selection Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove `ActiveProvider` and rely solely on role bindings with fallback to `roles.default`, without changing council mode behavior.

**Architecture:** Strip `activeProvider` from settings/config/runtime. Role resolution returns the provider bound to the role or falls back to the default role. Council continues to use explicit provider keys.

**Tech Stack:** Go, Bubble Tea TUI, settings persistence, tests via `go test`.

---

## File Structure (planned changes)

- Modify:
  - `internal/settings/settings.go` (remove `ActiveProvider` fields/merge logic)
  - `internal/settings/conduitconfig.go` (drop `activeProvider` serialization)
  - `internal/settings/settingsprovider.go` (role resolution fallback; remove activeProvider writes/reads)
  - `cmd/conduit/mainrepl.go` (stop threading InitialActiveProvider)
  - `internal/tui/run.go` + `internal/tui/model.go` (remove InitialActiveProvider and activeProvider state)
  - `internal/tui/providers.go` (role resolution without activeProvider fallback)
  - `internal/tui/providerspanel.go` (stop mutating ActiveProvider on rename)
  - Tests:
    - `internal/settings/settings_test.go`
    - `internal/tui/render_test.go`
    - `internal/tui/updatehandlers_test.go` (if needed)
- No changes to council files beyond ensuring existing behavior still compiles.

---

## Chunk 1: Settings + config layer (remove ActiveProvider fields)

### Task 1: Remove ActiveProvider from settings structs + merge logic

**Files:**
- Modify: `internal/settings/settings.go`
- Test: `internal/settings/settings_test.go`

- [ ] **Step 1: Write failing test update**
  - Update tests that expect `ActiveProvider` to be merged.
  - Example: remove assertions on `merged.ActiveProvider`.

- [ ] **Step 2: Run tests to verify failure**
  - Run: `go test ./internal/settings -run ActiveProvider`
  - Expected: FAIL because fields still exist in code but tests updated.

- [ ] **Step 3: Update settings structs and merge**
  - Remove `ActiveProvider` from `Settings` and `Merged`.
  - Remove merge of `ActiveProvider` in `loadPaths`.

- [ ] **Step 4: Run tests to verify pass**
  - Run: `go test ./internal/settings -run ActiveProvider`
  - Expected: PASS

- [ ] **Step 5: Commit**
  - (Per instruction, no commit)

---

### Task 2: Remove ActiveProvider from ConduitConfig

**Files:**
- Modify: `internal/settings/conduitconfig.go`
- Test: `internal/settings/settings_test.go` (if any serialization tests)

- [ ] **Step 1: Update struct + load**
  - Remove `ActiveProvider` field from `ConduitConfig`.
  - Remove `ActiveProvider: s.ActiveProvider` from `conduitConfigFromSettings`.

- [ ] **Step 2: Run tests**
  - Run: `go test ./internal/settings -run ConduitConfig`
  - Expected: PASS

---

## Chunk 2: Role resolution logic (fallback to default)

### Task 3: Update ProviderForRole and role persistence

**Files:**
- Modify: `internal/settings/settingsprovider.go`
- Test: `internal/settings/settings_test.go`

- [ ] **Step 1: Update tests**
  - Add case: missing role returns default role provider.
  - Remove references to activeProvider fallback.

- [ ] **Step 2: Implement logic**
  - `ProviderForRole`: resolve role → provider; if missing, resolve default role.
  - Remove activeProvider checks in `ProviderForRole`.
  - Remove writes to `activeProvider` in `SaveRoleProvider`, `DeleteProviderEntry`, `ClearRoleProvider`.

- [ ] **Step 3: Run tests**
  - Run: `go test ./internal/settings -run ProviderForRole`
  - Expected: PASS

---

## Chunk 3: Runtime/TUI removal of ActiveProvider

### Task 4: Remove InitialActiveProvider wiring

**Files:**
- Modify: `cmd/conduit/mainrepl.go`
- Modify: `internal/tui/run.go`
- Modify: `internal/tui/model.go`

- [ ] **Step 1: Remove InitialActiveProvider fields from RunOptions/Config**
- [ ] **Step 2: Remove initialization in `New`**
  - Remove `m.activeProvider` field and related initialization.
  - Ensure local-mode defaults now derive from role-bound MCP provider (if applicable).
- [ ] **Step 3: Run tests**
  - Run: `go test ./internal/tui -run Init`
  - Expected: PASS

---

### Task 5: Remove activeProvider usage in provider resolution

**Files:**
- Modify: `internal/tui/providers.go`
- Modify: `internal/tui/providerspanel.go`

- [ ] **Step 1: Update `providerValueForRole`/`providerForCurrentMode`**
  - Remove fallback to `m.activeProvider`.
  - Use `roles[default]` fallback only.
- [ ] **Step 2: Update providers panel**
  - Remove updates to `cfg.ActiveProvider` during rename.
- [ ] **Step 3: Run tests**
  - Run: `go test ./internal/tui -run Provider`
  - Expected: PASS

---

## Chunk 4: Council safety + validation

### Task 6: Ensure council behavior unchanged

**Files:**
- Modify: none unless build breaks
- Test: `internal/tui/council_test.go`

- [ ] **Step 1: Run council tests**
  - Run: `go test ./internal/tui -run Council`
  - Expected: PASS

---

## Chunk 5: Full verification

- [ ] **Step 1: Run full test suite**
  - Run: `make test-race`
  - Expected: PASS

- [ ] **Step 2: Run lint and verify**
  - Run: `make verify`
  - Expected: PASS
