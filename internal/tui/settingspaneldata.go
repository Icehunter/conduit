package tui

// Data preparation and display-name helpers for the settings panel.
// Includes config item catalog construction, filtering, and value converters.

import (
	"os"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/theme"
)

// ──────────────────────────────────────────────────────────────────────────────
// Config item catalog
// ──────────────────────────────────────────────────────────────────────────────

func (p *settingsPanelState) rebuildConfigItems() {
	snap := statusSnapshot{}
	if p.getStatus != nil {
		snap = p.getStatus()
	}

	data, _ := os.ReadFile(settings.ConduitSettingsPath())

	getBool := func(key string, def bool) bool {
		if strings.Contains(string(data), `"`+key+`":false`) ||
			strings.Contains(string(data), `"`+key+`": false`) {
			return false
		}
		if strings.Contains(string(data), `"`+key+`":true`) ||
			strings.Contains(string(data), `"`+key+`": true`) {
			return true
		}
		return def
	}
	getStr := func(key, def string) string {
		for _, sep := range []string{`":"`, `": "`} {
			prefix := `"` + key + sep
			if idx := strings.Index(string(data), prefix); idx >= 0 {
				rest := string(data)[idx+len(prefix):]
				if end := strings.IndexByte(rest, '"'); end >= 0 {
					return rest[:end]
				}
			}
		}
		return def
	}

	permMode := snap.permMode
	if permMode == "" {
		permMode = "default"
	}
	effort := snap.effort
	if effort == "" {
		effort = "normal"
	}
	model := snap.model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// Resolve display name for permission mode.
	permDisplay := permModeDisplay(permMode)
	outputStyle := getStr("outputStyle", "default")
	outputDisplay := outputStyleDisplay(outputStyle)

	p.configItems = []settingItem{
		{id: "autoCompactEnabled", label: "Auto-compact", kind: "bool",
			on: getBool("autoCompactEnabled", true)},
		{id: "spinnerTipsEnabled", label: "Show tips", kind: "bool",
			on: getBool("spinnerTipsEnabled", true)},
		{id: "prefersReducedMotion", label: "Reduce motion", kind: "bool",
			on: getBool("prefersReducedMotion", false)},
		{id: "thinkingEnabled", label: "Thinking mode", kind: "bool",
			on: getBool("alwaysThinkingEnabled", true)},
		{id: "fileCheckpointingEnabled", label: "Rewind code (checkpoints)", kind: "bool",
			on: getBool("fileCheckpointingEnabled", true)},
		{id: "verbose", label: "Verbose output", kind: "bool",
			on: getBool("verbose", false)},
		{id: "terminalProgressBarEnabled", label: "Terminal progress bar", kind: "bool",
			on: getBool("terminalProgressBarEnabled", true)},
		{id: "showTurnDuration", label: "Show turn duration", kind: "bool",
			on: getBool("showTurnDuration", true)},
		{
			id:         "defaultPermissionMode",
			label:      "Default permission mode",
			kind:       "enum",
			value:      permDisplay,
			options:    []string{"Default", "Plan Mode", "Accept Edits", "Auto Mode", "Don't Ask"},
			optionVals: []string{"default", "plan", "acceptEdits", "auto", "bypassPermissions"},
		},
		{id: "respectGitignore", label: "Respect .gitignore in file picker", kind: "bool",
			on: getBool("respectGitignore", true)},
		{id: "copyFullResponse", label: "Skip the /copy picker", kind: "bool",
			on: getBool("copyFullResponse", false)},
		{
			id:      "autoUpdatesChannel",
			label:   "Auto-update channel",
			kind:    "enum",
			value:   getStr("autoUpdatesChannel", "latest"),
			options: []string{"latest", "beta", "disabled"},
		},
		func() settingItem {
			// theme.AvailableThemes() includes any user-defined themes loaded
			// from settings.json's "themes" map (registered at startup via
			// theme.SetUserThemes), followed by all six built-in CC palettes.
			// We list everything so users who share settings.json with Claude
			// Code don't have conduit silently rewrite their CC theme pref.
			names := theme.AvailableThemes()
			return settingItem{
				id:    "theme",
				label: "Theme",
				kind:  "enum",
				value: func() string {
					v := getStr("theme", "dark")
					if v == "" {
						return "dark"
					}
					return v
				}(),
				options:    names,
				optionVals: names,
			}
		}(),
		{
			id:         "notifChannel",
			label:      "Local notifications",
			kind:       "enum",
			value:      getStr("preferredNotifChannel", "auto"),
			options:    []string{"auto", "iterm2", "terminal_bell", "disabled"},
			optionVals: []string{"auto", "iterm2", "terminal_bell", "notifications_disabled"},
		},
		{id: "agentPushNotifEnabled", label: "Push when Claude decides", kind: "bool",
			on: getBool("agentPushNotifEnabled", false)},
		{
			id:    "outputStyle",
			label: "Output style",
			kind:  "enum",
			value: outputDisplay,
			// Stored values match CC's built-in names exactly: "default"
			// is lowercase, "Explanatory" and "Learning" are capitalized.
			// Mismatched casing here would make the panel display "Default"
			// no matter what was actually selected.
			options:    []string{"Default", "Explanatory", "Learning"},
			optionVals: []string{"default", "Explanatory", "Learning"},
		},
		{
			id:         "model",
			label:      "Model",
			kind:       "enum",
			value:      modelDisplayName(model),
			options:    []string{"Default (recommended)", "Opus", "Haiku"},
			optionVals: []string{"claude-sonnet-4-6", "claude-opus-4-7", "claude-haiku-4-5-20251001"},
		},
		{
			id:      "effort",
			label:   "Thinking effort",
			kind:    "enum",
			value:   effort,
			options: []string{"low", "normal", "high", "max"},
		},
	}
}

func permModeDisplay(val string) string {
	switch val {
	case "plan":
		return "Plan Mode"
	case "acceptEdits":
		return "Accept Edits"
	case "auto":
		return "Auto Mode"
	case "bypassPermissions":
		return "Don't Ask"
	default:
		return "Default"
	}
}

// permModeStoredVal converts a display name back to the stored value.
// Called from model.go's saveConfigFn.
func permModeStoredVal(display string) string {
	switch display {
	case "Plan Mode":
		return "plan"
	case "Accept Edits":
		return "acceptEdits"
	case "Auto Mode":
		return "auto"
	case "Don't Ask":
		return "bypassPermissions"
	default:
		return "default"
	}
}

// outputStyleStoredVal converts a display name back to the stored value.
// Built-in style names are case-sensitive — "Explanatory" and "Learning"
// stay capitalized; only "default" is lowercase.
func outputStyleStoredVal(display string) string {
	switch display {
	case "Explanatory":
		return "Explanatory"
	case "Learning":
		return "Learning"
	default:
		return "default"
	}
}

func outputStyleDisplay(val string) string {
	switch val {
	case "Explanatory", "explanatory":
		return "Explanatory"
	case "Learning", "learning":
		return "Learning"
	default:
		return "Default"
	}
}

func modelDisplayName(modelID string) string {
	switch {
	case strings.Contains(modelID, "opus"):
		return "Opus"
	case strings.Contains(modelID, "haiku"):
		return "Haiku"
	default:
		return "Default (recommended)"
	}
}

func (p *settingsPanelState) applyFilter() {
	q := strings.ToLower(p.search)
	p.filteredIdx = nil
	for i, item := range p.configItems {
		if q == "" || strings.Contains(strings.ToLower(item.label), q) {
			p.filteredIdx = append(p.filteredIdx, i)
		}
	}
	if p.selected >= len(p.filteredIdx) {
		p.selected = max(0, len(p.filteredIdx)-1)
	}
}

func (p *settingsPanelState) rebuildConfigItemsFromSnap(snap statusSnapshot) {
	for i := range p.configItems {
		id := p.configItems[i].id
		if p.dirtyIDs[id] {
			continue // user changed this — don't clobber
		}
		switch id {
		case "model":
			if snap.model != "" {
				p.configItems[i].value = modelDisplayName(snap.model)
			}
		case "effort":
			if snap.effort != "" {
				p.configItems[i].value = snap.effort
			}
		case "defaultPermissionMode":
			if snap.permMode != "" {
				p.configItems[i].value = permModeDisplay(snap.permMode)
			}
		}
	}
}
