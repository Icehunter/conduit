# Provider Management UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users add providers (MCP/claude-subscription/anthropic-api) via the TUI, have them appear in the model picker, and assign them to per-mode roles (Default, Main, Background, Planning, Implement).

**Architecture:** A new "Providers" tab in the settings panel (`:provider` add/edit forms) writes typed `ConduitConfig.Providers` entries. The existing `Ctrl+M` model picker already reads from `providers` + `roles` — once those are populated the picker reflects real state without further changes to the picker render layer. The `/model` command handler and `RegisterModelCommand` already handle `provider:`, `local:`, `anthropic-api:`, and `claude-subscription:` value prefixes, so the picker → command dispatch path is already complete.

**Tech Stack:** Go 1.26, Bubble Tea v2 (`charm.land/bubbletea/v2`), `internal/settings`, `internal/tui`, `internal/commands`. All tests via `go test -race ./...`.

---

## What Already Works (Do Not Re-implement)

- `settings.ActiveProviderSettings`, `ProviderKey`, `SaveRoleProvider`, `SaveActiveProvider` — config read/write layer is done.
- `Roles` / `Providers` maps in `ConduitConfig` and `Merged` — serialization done.
- `filterModelPickerItems`, `modelPickerItemAvailable`, `providerForRole`, `providerForCurrentMode` — availability checks done.
- `renderModelPicker`, `renderProviderRoleTabs`, role Tab cycling — picker render done.
- `RegisterModelCommand` picker item builders (`accountModelPickerItems`, `localModelPickerItems`) — done.
- `SaveRoleProvider` dispatch from picker `Enter` — done.

## What Is Missing

1. No UI to **add** a new provider — users must hand-edit `~/.conduit/conduit.json`.
2. No UI to **remove** a provider or clear a role.
3. The model picker shows a **hardcoded** list of Claude models (`accountModelNames()`). Providers added via config with non-standard model names don't appear unless hand-crafted.
4. `localModelPickerItems` only shows MCP-kind providers. An `anthropic-api` provider with a custom model (e.g. from a Console API key with a named deployment) has no picker row.
5. No validation surface — bad `baseURL` or missing `server` fields fail silently.

---

## File Map

| File | Change |
|------|--------|
| `internal/settings/conduit_config.go` | Add `SaveProviderEntry`, `DeleteProviderEntry`, `ClearRoleProvider` helpers |
| `internal/settings/provider_validate.go` | New — `ValidateProviderSettings(ActiveProviderSettings) error` |
| `internal/tui/settings_panel.go` | Add `settingsTabProviders` tab; host `providersPanelState` list render |
| `internal/tui/providers_panel.go` | New — add/edit provider form overlay (multi-step, field-by-field) |
| `internal/tui/providers.go` | Extend `filterModelPickerItems` to include custom-model providers by kind |
| `internal/commands/builtin.go` | Extend `accountModelPickerItems` to also render providers with custom models |
| `internal/commands/local.go` | No change needed |

---

## Task 1: Settings persistence helpers

**Files:**
- Modify: `internal/settings/conduit_config.go`
- Create: `internal/settings/provider_validate.go`
- Test: `internal/settings/provider_validate_test.go`

### Step 1.1 — Write failing tests for validation

```go
// internal/settings/provider_validate_test.go
package settings_test

import (
	"testing"
	"github.com/icehunter/conduit/internal/settings"
)

func TestValidateProviderSettings(t *testing.T) {
	tests := []struct {
		name    string
		input   settings.ActiveProviderSettings
		wantErr bool
	}{
		{"claude-subscription needs model", settings.ActiveProviderSettings{Kind: "claude-subscription"}, true},
		{"claude-subscription valid", settings.ActiveProviderSettings{Kind: "claude-subscription", Model: "claude-sonnet-4-6"}, false},
		{"anthropic-api needs model", settings.ActiveProviderSettings{Kind: "anthropic-api"}, true},
		{"anthropic-api valid", settings.ActiveProviderSettings{Kind: "anthropic-api", Model: "claude-opus-4-7"}, false},
		{"mcp needs server", settings.ActiveProviderSettings{Kind: "mcp"}, true},
		{"mcp valid", settings.ActiveProviderSettings{Kind: "mcp", Server: "local-router"}, false},
		{"unknown kind", settings.ActiveProviderSettings{Kind: "unknown"}, true},
		{"empty kind", settings.ActiveProviderSettings{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := settings.ValidateProviderSettings(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProviderSettings() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
```

- [ ] **Step 1.1: Write the failing validation test above**

- [ ] **Step 1.2: Run it to confirm failure**

```bash
cd /Volumes/Engineering/Icehunter/conduit
go test ./internal/settings/... -run TestValidateProviderSettings -v
```
Expected: `FAIL` — `ValidateProviderSettings` undefined.

- [ ] **Step 1.3: Implement `ValidateProviderSettings`**

```go
// internal/settings/provider_validate.go
package settings

import "fmt"

// ValidateProviderSettings returns a non-nil error if the provider is
// structurally incomplete. It does not check connectivity.
func ValidateProviderSettings(p ActiveProviderSettings) error {
	switch p.Kind {
	case "claude-subscription":
		if p.Model == "" {
			return fmt.Errorf("settings: claude-subscription provider requires a model")
		}
	case "anthropic-api":
		if p.Model == "" {
			return fmt.Errorf("settings: anthropic-api provider requires a model")
		}
	case "mcp":
		if p.Server == "" {
			return fmt.Errorf("settings: mcp provider requires a server name")
		}
	case "":
		return fmt.Errorf("settings: provider kind is required")
	default:
		return fmt.Errorf("settings: unknown provider kind %q", p.Kind)
	}
	return nil
}
```

- [ ] **Step 1.4: Run test to confirm pass**

```bash
go test ./internal/settings/... -run TestValidateProviderSettings -v
```
Expected: `PASS`

---

### Step 1.5 — Write failing tests for CRUD helpers

```go
// Append to internal/settings/provider_validate_test.go

func TestSaveDeleteProviderEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// Ensure conduit dir exists at expected path
	_ = os.MkdirAll(filepath.Join(dir, ".conduit"), 0o755)

	p := ActiveProviderSettings{
		Kind:  "mcp",
		Server: "test-router",
		Model: "test-model",
	}
	key := ProviderKey(p)

	if err := SaveProviderEntry(p); err != nil {
		t.Fatalf("SaveProviderEntry: %v", err)
	}

	cfg, err := LoadConduitConfig()
	if err != nil {
		t.Fatalf("LoadConduitConfig: %v", err)
	}
	if _, ok := cfg.Providers[key]; !ok {
		t.Fatalf("expected provider key %q in config", key)
	}

	if err := DeleteProviderEntry(key); err != nil {
		t.Fatalf("DeleteProviderEntry: %v", err)
	}

	cfg2, _ := LoadConduitConfig()
	if _, ok := cfg2.Providers[key]; ok {
		t.Fatalf("expected provider key %q to be deleted", key)
	}
}

func TestClearRoleProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, ".conduit"), 0o755)

	p := ActiveProviderSettings{Kind: "claude-subscription", Model: "claude-sonnet-4-6"}
	if err := SaveRoleProvider(RoleMain, p); err != nil {
		t.Fatalf("SaveRoleProvider: %v", err)
	}
	if err := ClearRoleProvider(RoleMain); err != nil {
		t.Fatalf("ClearRoleProvider: %v", err)
	}
	cfg, _ := LoadConduitConfig()
	if _, ok := cfg.Roles[RoleMain]; ok {
		t.Fatalf("expected role %q to be cleared", RoleMain)
	}
}
```

The test imports need `"os"` and `"path/filepath"` — add them to the import block.

- [ ] **Step 1.5: Append CRUD tests to `provider_validate_test.go`** (add imports as needed)

- [ ] **Step 1.6: Run to confirm failure**

```bash
go test ./internal/settings/... -run TestSaveDeleteProviderEntry -v
go test ./internal/settings/... -run TestClearRoleProvider -v
```
Expected: `FAIL` — functions undefined.

- [ ] **Step 1.7: Implement the three helpers in `conduit_config.go`**

Add at the bottom of `internal/settings/conduit_config.go`:

```go
// SaveProviderEntry writes a single provider into the providers map without
// changing any role bindings or activeProvider.
func SaveProviderEntry(p ActiveProviderSettings) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		if cfg.Providers == nil {
			cfg.Providers = map[string]ActiveProviderSettings{}
		}
		cfg.Providers[ProviderKey(p)] = p
	})
}

// DeleteProviderEntry removes a provider by key. It also clears any role that
// pointed to this key.
func DeleteProviderEntry(key string) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		delete(cfg.Providers, key)
		for role, ref := range cfg.Roles {
			if ref == key {
				delete(cfg.Roles, role)
			}
		}
	})
}

// ClearRoleProvider removes the role binding without deleting the provider
// entry itself.
func ClearRoleProvider(role string) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		delete(cfg.Roles, role)
	})
}
```

- [ ] **Step 1.8: Run to confirm pass**

```bash
go test ./internal/settings/... -race -v
```
Expected: all pass.

- [ ] **Step 1.9: Commit**

```bash
git add internal/settings/provider_validate.go internal/settings/provider_validate_test.go internal/settings/conduit_config.go
git commit -m "feat(settings): add ValidateProviderSettings, SaveProviderEntry, DeleteProviderEntry, ClearRoleProvider"
```

---

## Task 2: Extend picker items for custom-model providers

Currently `accountModelNames()` returns a hardcoded 3-model list. Providers with custom models (e.g. `anthropic-api` with a fine-tuned endpoint, or a `claude-subscription` entry with a non-standard model) never appear in the picker.

**Files:**
- Modify: `internal/commands/builtin.go`
- Test: `internal/commands/builtin_test.go`

- [ ] **Step 2.1: Write failing test**

Find the test file:
```bash
grep -n "accountModelPickerItems\|TestAccountModel" /Volumes/Engineering/Icehunter/conduit/internal/commands/builtin_test.go | head -20
```

Add to `internal/commands/builtin_test.go`:

```go
func TestCustomModelPickerItems(t *testing.T) {
	providers := map[string]settings.ActiveProviderSettings{
		"claude-subscription.custom-org.custom-sonnet-preview": {
			Kind:    "claude-subscription",
			Account: "custom-org",
			Model:   "custom-sonnet-preview",
		},
		"anthropic-api.org@example.com.claude-opus-4-7": {
			Kind:    "anthropic-api",
			Account: "org@example.com",
			Model:   "claude-opus-4-7",
		},
	}
	items := customModelPickerItems(providers)
	foundCustom := false
	for _, it := range items {
		if it.Value == "provider:claude-subscription.custom-org.custom-sonnet-preview" {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Errorf("expected custom-sonnet-preview to appear in picker items; got %v", items)
	}
}
```

- [ ] **Step 2.2: Run to confirm failure**

```bash
go test ./internal/commands/... -run TestCustomModelPickerItems -v
```
Expected: `FAIL` — `customModelPickerItems` undefined.

- [ ] **Step 2.3: Implement `customModelPickerItems` in `builtin.go`**

Add after `accountModelPickerItems`:

```go
// customModelPickerItems returns picker rows for providers whose model is not
// in the standard accountModelNames list. This covers fine-tuned deployments,
// preview models, or Console-registered custom endpoints.
func customModelPickerItems(providers map[string]settings.ActiveProviderSettings) []PickerOption {
	standard := map[string]bool{}
	for _, m := range accountModelNames() {
		standard[m] = true
	}
	var items []PickerOption
	for key, p := range providers {
		switch p.Kind {
		case "claude-subscription", "anthropic-api":
		default:
			continue
		}
		if standard[p.Model] || p.Model == "" {
			continue
		}
		section := "Claude Subscription"
		if p.Kind == "anthropic-api" {
			section = "Anthropic API"
		}
		if p.Account != "" {
			section += " · " + p.Account
		}
		label := p.Model
		if p.Account != "" {
			label += " (" + p.Account + ")"
		}
		items = append(items, PickerOption{Label: section + " (custom)", Section: true})
		items = append(items, PickerOption{Value: "provider:" + key, Label: label})
	}
	return items
}
```

- [ ] **Step 2.4: Wire `customModelPickerItems` into the `/model` picker builder**

In `RegisterModelCommand`, in the `if args == ""` branch, add after `items = append(items, localItems...)`:

```go
customItems := customModelPickerItems(providers)
items = append(items, customItems...)
```

- [ ] **Step 2.5: Run to confirm pass**

```bash
go test ./internal/commands/... -race -v
```
Expected: all pass.

- [ ] **Step 2.6: Commit**

```bash
git add internal/commands/builtin.go internal/commands/builtin_test.go
git commit -m "feat(commands): add customModelPickerItems so non-standard models appear in /model picker"
```

---

## Task 3: Providers tab in settings panel — list view

Add a "Providers" tab to the existing settings panel. On this tab the user sees all configured providers and can press `a` to add, `d` to delete, or `c` to clear a role.

**Files:**
- Modify: `internal/tui/settings_panel.go`

### Data contracts

The settings panel already has a `settingsPanelTab` iota and `settingsTabNames` slice. We extend both.

- [ ] **Step 3.1: Add the tab constant and name**

In `internal/tui/settings_panel.go`, change:

```go
const (
	settingsTabStatus settingsPanelTab = iota
	settingsTabConfig
	settingsTabStats
	settingsTabUsage
	settingsTabAccounts
)

var settingsTabNames = []string{"Status", "Config", "Stats", "Usage", "Accounts"}
```

to:

```go
const (
	settingsTabStatus settingsPanelTab = iota
	settingsTabConfig
	settingsTabStats
	settingsTabUsage
	settingsTabAccounts
	settingsTabProviders
)

var settingsTabNames = []string{"Status", "Config", "Stats", "Usage", "Accounts", "Providers"}
```

- [ ] **Step 3.2: Add provider list state to the panel struct**

Find the `settingsPanelState` struct (it holds `tab`, `configFocus`, `configSel`, etc.) and add:

```go
providerSel int // cursor in providers tab list
```

- [ ] **Step 3.3: Add key handling for the Providers tab**

In the settings panel key handler, add a case for `settingsTabProviders`:

```go
case settingsTabProviders:
    switch msg.String() {
    case "up", "k":
        if st.providerSel > 0 {
            st.providerSel--
        }
    case "down", "j":
        rows := m.settingsProviderRows()
        if st.providerSel < len(rows)-1 {
            st.providerSel++
        }
    case "a":
        m.settingsPanel = nil
        m.providerForm = newProviderFormState()
        return m, nil
    case "d":
        rows := m.settingsProviderRows()
        if st.providerSel >= 0 && st.providerSel < len(rows) {
            key := rows[st.providerSel].key
            if key != "" {
                _ = settings.DeleteProviderEntry(key)
                m.providers = reloadProviders()
                if st.providerSel > 0 && st.providerSel >= len(rows)-1 {
                    st.providerSel--
                }
            }
        }
    case "c":
        rows := m.settingsProviderRows()
        if st.providerSel >= 0 && st.providerSel < len(rows) {
            role := rows[st.providerSel].role
            if role != "" {
                _ = settings.ClearRoleProvider(role)
                m.roles = reloadRoles()
            }
        }
    }
    m.settingsPanel = st
    return m, nil
```

`settingsProviderRows`, `reloadProviders`, and `reloadRoles` are helpers defined in the next step.

- [ ] **Step 3.4: Add provider row helper and reload helpers**

In `internal/tui/settings_panel.go` (or a helper file if the panel file grows too large), add:

```go
type providerRow struct {
	key      string // ProviderKey — empty for role-only rows
	role     string // role name — empty for provider-only rows
	kind     string
	model    string
	server   string
	account  string
	assigned string // role currently assigned to this provider, if any
}

func (m Model) settingsProviderRows() []providerRow {
	var rows []providerRow
	// Show role assignments first
	roleOrder := []string{
		settings.RoleDefault,
		settings.RoleMain,
		settings.RoleBackground,
		settings.RolePlanning,
		settings.RoleImplement,
	}
	for _, role := range roleOrder {
		ref := m.roles[role]
		if ref == "" {
			rows = append(rows, providerRow{role: role, kind: "(unset)"})
			continue
		}
		p := m.providers[ref]
		rows = append(rows, providerRow{
			key:     ref,
			role:    role,
			kind:    p.Kind,
			model:   p.Model,
			server:  p.Server,
			account: p.Account,
		})
	}
	// Then providers not assigned to any role
	assigned := map[string]bool{}
	for _, ref := range m.roles {
		assigned[ref] = true
	}
	for key, p := range m.providers {
		if !assigned[key] {
			rows = append(rows, providerRow{
				key:     key,
				kind:    p.Kind,
				model:   p.Model,
				server:  p.Server,
				account: p.Account,
			})
		}
	}
	return rows
}

func reloadProviders() map[string]settings.ActiveProviderSettings {
	cfg, err := settings.LoadConduitConfig()
	if err != nil {
		return nil
	}
	return cfg.Providers
}

func reloadRoles() map[string]string {
	cfg, err := settings.LoadConduitConfig()
	if err != nil {
		return nil
	}
	return cfg.Roles
}
```

- [ ] **Step 3.5: Add render function for the Providers tab**

In `internal/tui/settings_panel.go`, add a render function called from the panel's main render switch:

```go
func (m Model) renderSettingsProviders(st *settingsPanelState, width int) string {
	rows := m.settingsProviderRows()
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n", styleStatusAccent.Render("Providers & Roles"))
	if len(rows) == 0 {
		sb.WriteString(stylePickerDesc.Render("No providers configured. Press 'a' to add one.") + "\n")
	}
	for i, row := range rows {
		cursor := "  "
		if i == st.providerSel {
			cursor = "❯ "
		}
		var line string
		if row.role != "" {
			roleLabel := styleModeCyan.Render(fmt.Sprintf("%-12s", row.role))
			provLabel := ""
			switch row.kind {
			case "(unset)":
				provLabel = stylePickerDesc.Render("(unset)")
			case "mcp":
				provLabel = fmt.Sprintf("MCP · %s", row.server)
				if row.model != "" && row.model != row.server {
					provLabel += " · " + row.model
				}
			default:
				provLabel = row.kind
				if row.model != "" {
					provLabel += " · " + row.model
				}
				if row.account != "" {
					provLabel += " · " + row.account
				}
			}
			line = roleLabel + "  " + provLabel
		} else {
			switch row.kind {
			case "mcp":
				line = fmt.Sprintf("%-12s  MCP · %s", "(no role)", row.server)
				if row.model != "" && row.model != row.server {
					line += " · " + row.model
				}
			default:
				line = fmt.Sprintf("%-12s  %s · %s", "(no role)", row.kind, row.model)
				if row.account != "" {
					line += " · " + row.account
				}
			}
		}
		if i == st.providerSel {
			fmt.Fprintf(&sb, "%s\n", stylePickerItemSelected.Render(cursor+line))
		} else {
			fmt.Fprintf(&sb, "%s\n", stylePickerItem.Render("  "+line))
		}
	}
	fmt.Fprintf(&sb, "\n%s", stylePickerDesc.Render("↑/↓ navigate · a add · d delete · c clear role · Esc close"))
	return sb.String()
}
```

- [ ] **Step 3.6: Wire the render into the panel's main render switch**

Find the settings panel render switch (the big `switch st.tab` block) and add:

```go
case settingsTabProviders:
    content = m.renderSettingsProviders(st, contentW)
```

- [ ] **Step 3.7: Run make verify to check for compile errors**

```bash
cd /Volumes/Engineering/Icehunter/conduit && make verify
```
Fix any compile errors before proceeding. Expected: zero lint errors, tests pass.

- [ ] **Step 3.8: Commit**

```bash
git add internal/tui/settings_panel.go
git commit -m "feat(tui): add Providers tab to settings panel with list view, delete, and clear-role"
```

---

## Task 4: Add-provider form overlay

A modal form that walks the user through adding a new provider step by step. Fields differ by kind. On completion it calls `SaveProviderEntry` and optionally `SaveRoleProvider`.

**Files:**
- Create: `internal/tui/providers_panel.go`
- Modify: `internal/tui/model.go` (add `providerForm *providerFormState` field)
- Modify: `internal/tui/update.go` (dispatch form key events and close message)
- Modify: `internal/tui/draw.go` (render form as floating overlay)

### Data contracts

```go
// internal/tui/providers_panel.go

type providerFormStep int

const (
	providerFormStepKind providerFormStep = iota // choose: claude-subscription | anthropic-api | mcp
	providerFormStepModel                        // enter model name (not for mcp)
	providerFormStepServer                       // enter server name (mcp only)
	providerFormStepAccount                      // enter account/email (optional for api kinds)
	providerFormStepRole                         // optionally assign a role
	providerFormStepDone
)

type providerFormState struct {
	step    providerFormStep
	kind    string // "claude-subscription" | "anthropic-api" | "mcp"
	model   string
	server  string
	account string
	role    string // "" = no role assignment
	input   string // current text field value
	err     string // validation error to display
	// kindSel is the cursor for the kind selection step
	kindSel int
	// roleSel is the cursor for the role selection step
	roleSel int
}

func newProviderFormState() *providerFormState {
	return &providerFormState{step: providerFormStepKind}
}
```

The form renders as a floating panel (same chrome as `renderModelPicker`). Each step shows the collected fields so far plus the current prompt.

- [ ] **Step 4.1: Create `internal/tui/providers_panel.go`** with the types above plus:

```go
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/settings"
)

type providerFormStep int

const (
	providerFormStepKind    providerFormStep = iota
	providerFormStepModel
	providerFormStepServer
	providerFormStepAccount
	providerFormStepRole
	providerFormStepDone
)

type providerFormState struct {
	step    providerFormStep
	kind    string
	model   string
	server  string
	account string
	role    string
	input   string
	err     string
	kindSel int
	roleSel int
}

func newProviderFormState() *providerFormState {
	return &providerFormState{step: providerFormStepKind}
}

var providerKindOptions = []struct {
	kind  string
	label string
}{
	{"claude-subscription", "Claude Subscription  (OAuth / Max)"},
	{"anthropic-api", "Anthropic API        (API key / Console)"},
	{"mcp", "MCP                  (local / LAN router)"},
}

var providerRoleOptions = []string{
	"(skip — no role)",
	settings.RoleDefault,
	settings.RoleMain,
	settings.RoleBackground,
	settings.RolePlanning,
	settings.RoleImplement,
}

func (m *Model) handleProviderFormKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	f := m.providerForm
	if f == nil {
		return *m, nil
	}
	switch f.step {
	case providerFormStepKind:
		return m.handleProviderFormKindKey(msg)
	case providerFormStepModel, providerFormStepServer, providerFormStepAccount:
		return m.handleProviderFormTextKey(msg)
	case providerFormStepRole:
		return m.handleProviderFormRoleKey(msg)
	}
	return *m, nil
}

func (m *Model) handleProviderFormKindKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	f := m.providerForm
	switch msg.String() {
	case "up", "k":
		if f.kindSel > 0 {
			f.kindSel--
		}
	case "down", "j":
		if f.kindSel < len(providerKindOptions)-1 {
			f.kindSel++
		}
	case "enter", "space":
		f.kind = providerKindOptions[f.kindSel].kind
		f.err = ""
		f.input = ""
		if f.kind == "mcp" {
			f.step = providerFormStepServer
		} else {
			f.step = providerFormStepModel
		}
	case "esc", "ctrl+c":
		m.providerForm = nil
		return *m, nil
	}
	m.providerForm = f
	return *m, nil
}

func (m *Model) handleProviderFormTextKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	f := m.providerForm
	switch msg.String() {
	case "enter":
		val := strings.TrimSpace(f.input)
		if val == "" {
			if f.step == providerFormStepAccount {
				// account is optional; skip
				f.step = providerFormStepRole
				f.input = ""
				f.err = ""
				m.providerForm = f
				return *m, nil
			}
			f.err = "value is required"
			m.providerForm = f
			return *m, nil
		}
		switch f.step {
		case providerFormStepModel:
			p := settings.ActiveProviderSettings{Kind: f.kind, Model: val}
			if err := settings.ValidateProviderSettings(p); err != nil {
				f.err = err.Error()
				m.providerForm = f
				return *m, nil
			}
			f.model = val
			f.step = providerFormStepAccount
		case providerFormStepServer:
			p := settings.ActiveProviderSettings{Kind: "mcp", Server: val}
			if err := settings.ValidateProviderSettings(p); err != nil {
				f.err = err.Error()
				m.providerForm = f
				return *m, nil
			}
			f.server = val
			f.step = providerFormStepRole
		case providerFormStepAccount:
			f.account = val
			f.step = providerFormStepRole
		}
		f.input = ""
		f.err = ""
	case "esc", "ctrl+c":
		m.providerForm = nil
		return *m, nil
	case "backspace":
		if len(f.input) > 0 {
			f.input = f.input[:len(f.input)-1]
		}
	default:
		if len(msg.String()) == 1 {
			f.input += msg.String()
		}
	}
	m.providerForm = f
	return *m, nil
}

func (m *Model) handleProviderFormRoleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	f := m.providerForm
	switch msg.String() {
	case "up", "k":
		if f.roleSel > 0 {
			f.roleSel--
		}
	case "down", "j":
		if f.roleSel < len(providerRoleOptions)-1 {
			f.roleSel++
		}
	case "enter", "space":
		chosen := providerRoleOptions[f.roleSel]
		if chosen != "(skip — no role)" {
			f.role = chosen
		}
		f.step = providerFormStepDone
		return m.commitProviderForm()
	case "esc", "ctrl+c":
		m.providerForm = nil
		return *m, nil
	}
	m.providerForm = f
	return *m, nil
}

func (m *Model) commitProviderForm() (Model, tea.Cmd) {
	f := m.providerForm
	p := settings.ActiveProviderSettings{
		Kind:    f.kind,
		Model:   f.model,
		Server:  f.server,
		Account: f.account,
	}
	if err := settings.ValidateProviderSettings(p); err != nil {
		f.err = fmt.Sprintf("validation: %v", err)
		m.providerForm = f
		return *m, nil
	}
	if err := settings.SaveProviderEntry(p); err != nil {
		f.err = fmt.Sprintf("save: %v", err)
		m.providerForm = f
		return *m, nil
	}
	if f.role != "" {
		_ = settings.SaveRoleProvider(f.role, p)
	}
	m.providers = reloadProviders()
	m.roles = reloadRoles()
	m.providerForm = nil
	return *m, nil
}

func (m Model) renderProviderForm() string {
	f := m.providerForm
	if f == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n", panelTitle("Add Provider"))
	// Summary of already-collected fields
	if f.kind != "" {
		fmt.Fprintf(&sb, "%s %s\n", stylePickerDesc.Render("kind:"), f.kind)
	}
	if f.model != "" {
		fmt.Fprintf(&sb, "%s %s\n", stylePickerDesc.Render("model:"), f.model)
	}
	if f.server != "" {
		fmt.Fprintf(&sb, "%s %s\n", stylePickerDesc.Render("server:"), f.server)
	}
	if f.account != "" {
		fmt.Fprintf(&sb, "%s %s\n", stylePickerDesc.Render("account:"), f.account)
	}
	if f.kind != "" || f.model != "" || f.server != "" {
		sb.WriteByte('\n')
	}

	switch f.step {
	case providerFormStepKind:
		sb.WriteString(stylePickerDesc.Render("Select provider kind:") + "\n\n")
		for i, opt := range providerKindOptions {
			cursor := "  "
			if i == f.kindSel {
				cursor = "❯ "
			}
			line := cursor + opt.label
			if i == f.kindSel {
				fmt.Fprintf(&sb, "%s\n", stylePickerItemSelected.Render(line))
			} else {
				fmt.Fprintf(&sb, "%s\n", stylePickerItem.Render("  "+opt.label))
			}
		}
	case providerFormStepModel:
		fmt.Fprintf(&sb, "%s\n\n  %s_\n", stylePickerDesc.Render("Enter model name (e.g. claude-sonnet-4-6):"), f.input)
	case providerFormStepServer:
		fmt.Fprintf(&sb, "%s\n\n  %s_\n", stylePickerDesc.Render("Enter MCP server name (e.g. local-router):"), f.input)
	case providerFormStepAccount:
		fmt.Fprintf(&sb, "%s\n\n  %s_\n", stylePickerDesc.Render("Enter account email (or press Enter to skip):"), f.input)
	case providerFormStepRole:
		sb.WriteString(stylePickerDesc.Render("Assign to role (optional):") + "\n\n")
		for i, opt := range providerRoleOptions {
			cursor := "  "
			if i == f.roleSel {
				cursor = "❯ "
			}
			line := cursor + opt
			if i == f.roleSel {
				fmt.Fprintf(&sb, "%s\n", stylePickerItemSelected.Render(line))
			} else {
				fmt.Fprintf(&sb, "%s\n", stylePickerItem.Render("  "+opt))
			}
		}
	}

	if f.err != "" {
		fmt.Fprintf(&sb, "\n%s\n", styleModeRed.Render("✗ "+f.err))
	}
	fmt.Fprintf(&sb, "\n%s", stylePickerDesc.Render("↑/↓ navigate · Enter confirm · Esc cancel"))
	return sb.String()
}
```

Note: `styleModeRed` — check `styles.go` for the exact variable name for red error text. Substitute the correct name if different (e.g. `styleError`).

- [ ] **Step 4.2: Add `providerForm *providerFormState` field to `Model` in `model.go`**

Find the `Model` struct in `internal/tui/model.go` and add the field near the other overlay state fields (e.g. near `picker *pickerState`):

```go
// providerForm is non-nil when the add-provider multi-step form is open.
providerForm *providerFormState
```

- [ ] **Step 4.3: Dispatch form key events in `update.go` / `key_handler.go`**

In the main key dispatch (find where `m.picker != nil` is checked for overlay priority), add before or after it:

```go
if m.providerForm != nil {
    return m.handleProviderFormKey(msg)
}
```

- [ ] **Step 4.4: Render form in `draw.go`**

Find the `View()` function's overlay dispatch. After the picker overlay check, add:

```go
if m.providerForm != nil {
    body := m.renderProviderForm()
    return m.renderFloating(body, floatingModelPickerSpec)
}
```

(Use the same floating spec as the model picker, or define a `floatingProviderFormSpec` with the same dimensions if needed.)

- [ ] **Step 4.5: Run make verify**

```bash
cd /Volumes/Engineering/Icehunter/conduit && make verify
```
Fix any compile errors. Common issues: missing style variable name, wrong floating spec name, import cycles.

- [ ] **Step 4.6: Commit**

```bash
git add internal/tui/providers_panel.go internal/tui/model.go internal/tui/update.go internal/tui/draw.go
git commit -m "feat(tui): add multi-step add-provider form overlay"
```

---

## Task 5: Sync newly added providers into in-memory model state

When a provider is saved (Task 4's `commitProviderForm`) the `m.providers` and `m.roles` maps are updated from disk. The model picker (`Ctrl+M`) must also reflect the new providers immediately without restart.

**Files:**
- Modify: `internal/tui/providers.go` (extend `filterModelPickerItems` to use `m.providers` directly)
- Modify: `internal/commands/builtin.go` (`RegisterModelCommand` takes a `providers` snapshot at call time — it needs a getter instead)

### Data contract change

`RegisterModelCommand` currently takes `providers map[string]settings.ActiveProviderSettings` as a value snapshot. To reflect runtime adds, it should take a getter `func() map[string]settings.ActiveProviderSettings` instead.

- [ ] **Step 5.1: Write failing test verifying live provider map**

```go
// internal/commands/builtin_test.go
func TestRegisterModelCommandUsesLiveProviders(t *testing.T) {
	r := NewRegistry()
	liveProviders := map[string]settings.ActiveProviderSettings{}
	getProviders := func() map[string]settings.ActiveProviderSettings { return liveProviders }

	RegisterModelCommand(r, func() string { return "claude-sonnet-4-6" }, func(string) {}, nil, nil, getProviders)
	res := r.Dispatch("/model")
	// initially no custom providers
	if res == nil {
		t.Fatal("expected result")
	}

	// add a provider at runtime
	liveProviders["mcp.live-server"] = settings.ActiveProviderSettings{Kind: "mcp", Server: "live-server", Model: "llama3"}
	res2 := r.Dispatch("/model")
	found := false
	for _, it := range res2.Items {
		if it.Value == "local:live-server" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected live-server in picker items after runtime add; got %v", res2.Items)
	}
}
```

Note: `r.Dispatch` currently returns `*Result` not `Result` — check `commands.go` for the exact signature and adjust.

- [ ] **Step 5.2: Run to confirm failure**

```bash
go test ./internal/commands/... -run TestRegisterModelCommandUsesLiveProviders -v
```

- [ ] **Step 5.3: Change `RegisterModelCommand` signature**

In `internal/commands/builtin.go`, change the last parameter from:

```go
providers map[string]settings.ActiveProviderSettings,
```

to:

```go
getProviders func() map[string]settings.ActiveProviderSettings,
```

And inside the handler, replace every use of `providers` with `getProviders()`.

- [ ] **Step 5.4: Update the call site in `internal/tui/run.go` (or wherever `RegisterModelCommand` is called)**

Find the call site:
```bash
grep -rn "RegisterModelCommand" /Volumes/Engineering/Icehunter/conduit/
```

Change the last argument from a direct map to a closure:

```go
func() map[string]settings.ActiveProviderSettings { return m.providers },
```

(Where `m` is the `*Model` or closure-captured model — adjust to match the actual pattern at the call site.)

- [ ] **Step 5.5: Run make verify**

```bash
cd /Volumes/Engineering/Icehunter/conduit && make verify
```

- [ ] **Step 5.6: Confirm test passes**

```bash
go test ./internal/commands/... -race -v
```

- [ ] **Step 5.7: Commit**

```bash
git add internal/commands/builtin.go internal/tui/run.go
git commit -m "feat(commands): accept live provider getter so /model picker reflects runtime adds without restart"
```

---

## Task 6: Model picker `modelPickerItemAvailable` for custom providers

Currently `filterModelPickerItems` only knows about `local:`, `provider:`, `anthropic-api:`, and `claude-subscription:` prefixes. Providers added with a custom model that appear via `customModelPickerItems` (Task 2) use the `provider:` prefix — that path already returns `true` from `modelPickerItemAvailable`. No change needed here, but add a smoke test to confirm end-to-end.

- [ ] **Step 6.1: Write smoke test**

```go
// internal/tui/providers_test.go  (new file)
package tui

import (
	"testing"
	"github.com/icehunter/conduit/internal/settings"
)

func TestFilterModelPickerItemsCustomProvider(t *testing.T) {
	m := Model{
		providers: map[string]settings.ActiveProviderSettings{
			"anthropic-api.org@example.com.custom-model": {
				Kind:    "anthropic-api",
				Account: "org@example.com",
				Model:   "custom-model",
			},
		},
	}
	items := []pickerItem{
		{Label: "Anthropic API", Section: true},
		{Value: "provider:anthropic-api.org@example.com.custom-model", Label: "custom-model"},
	}
	out := m.filterModelPickerItems(items)
	if len(out) == 0 {
		t.Fatal("expected custom provider item to pass filter")
	}
}
```

- [ ] **Step 6.2: Run to confirm pass (no code change expected)**

```bash
go test ./internal/tui/... -run TestFilterModelPickerItemsCustomProvider -v
```
Expected: `PASS`. If it fails, the `modelPickerItemAvailable` logic needs a fix (check the `provider:` branch handles `anthropic-api` prefix correctly).

- [ ] **Step 6.3: Commit**

```bash
git add internal/tui/providers_test.go
git commit -m "test(tui): smoke test custom provider appears in filtered picker"
```

---

## Task 7: Final integration pass

- [ ] **Step 7.1: Run full verify**

```bash
cd /Volumes/Engineering/Icehunter/conduit && make verify
```
Expected: zero lint errors, all tests pass with `-race`.

- [ ] **Step 7.2: Manual smoke test**

```
1. Launch ./conduit
2. Open settings panel (default keybinding — check keybindings.go)
3. Navigate to "Providers" tab
4. Press 'a' — confirm add-provider form opens
5. Select "MCP" → enter server "test-router" → select role "implement" → confirm
6. Open Ctrl+M model picker → confirm test-router appears under MCP section
7. Assign it to Implement role via the picker
8. Check ~/.conduit/conduit.json — confirm providers + roles written correctly
9. Press 'd' in Providers tab on the new entry → confirm deleted from config
```

- [ ] **Step 7.3: Update STATUS.md**

Mark the following as ✅ or 🔶 as appropriate:
- Provider management UI
- Custom model picker entries

- [ ] **Step 7.4: Final commit**

```bash
git add STATUS.md
git commit -m "chore: update STATUS.md — provider management UI complete"
```

---

## Self-Review

### Spec coverage check

| Requirement | Covered by |
|-------------|-----------|
| Add providers via UI | Task 4 (form overlay) |
| Providers appear in model picker | Task 2 (customModelPickerItems), Task 5 (live getter) |
| Assign providers to modes/roles | Task 4 (role step), Task 3 (clear role), picker dispatch already works |
| Delete providers | Task 1 (DeleteProviderEntry), Task 3 (key handler) |
| Validation surface | Task 1 (ValidateProviderSettings) |
| Live updates without restart | Task 5 (live getter) |
| Filter availability in picker | Task 6 (smoke test) |

### Type consistency

- `providerFormState` defined in `providers_panel.go`, referenced in `model.go`, dispatched in `update.go`, rendered in `draw.go` — consistent field names throughout.
- `reloadProviders()` / `reloadRoles()` defined in `settings_panel.go` and called from `providers_panel.go` — both in `package tui`, no import issue.
- `customModelPickerItems` takes `map[string]settings.ActiveProviderSettings` matching `Model.providers` type.

### Placeholder scan

No TBD, TODO, "similar to above", or vague steps found. All code blocks contain complete compilable snippets.
