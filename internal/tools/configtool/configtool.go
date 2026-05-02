// Package configtool implements the ConfigTool (get/set conduit settings).
//
// Supported settings are a curated subset of the real Claude Code settings —
// those that conduit actually reads. Port of src/tools/ConfigTool/.
package configtool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/icehunter/conduit/internal/tool"
)

const toolName = "Config"

// globalSettingsPath returns ~/.claude/settings.json.
func globalSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// input is the JSON input for Config.
type input struct {
	Setting string `json:"setting"`
	Value   any    `json:"value,omitempty"`
}

// ConfigTool reads or writes supported conduit settings.
type ConfigTool struct {
	// SettingsPath overrides the global settings path (for testing).
	SettingsPath string
}

func (t *ConfigTool) Name() string        { return toolName }
func (t *ConfigTool) Description() string { return description }
func (t *ConfigTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"setting": {
			"type": "string",
			"description": "The setting key (e.g. \"model\", \"permissions.defaultMode\")"
		},
		"value": {
			"description": "The new value. Omit to get current value."
		}
	},
	"required": ["setting"],
	"additionalProperties": false
}`)
}
func (t *ConfigTool) IsReadOnly(raw json.RawMessage) bool {
	var inp input
	_ = json.Unmarshal(raw, &inp)
	return inp.Value == nil
}
func (t *ConfigTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *ConfigTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var inp input
	if err := json.Unmarshal(raw, &inp); err != nil {
		return tool.ErrorResult("invalid input: " + err.Error()), nil
	}
	if inp.Setting == "" {
		return tool.ErrorResult("setting is required"), nil
	}

	path := t.SettingsPath
	if path == "" {
		path = globalSettingsPath()
	}

	settings := t.loadSettings(path)

	if inp.Value == nil {
		// GET operation.
		val := getField(settings, inp.Setting)
		out, _ := json.MarshalIndent(map[string]any{
			"setting": inp.Setting,
			"value":   val,
		}, "", "  ")
		return tool.TextResult(string(out)), nil
	}

	// SET operation.
	prev := getField(settings, inp.Setting)
	if err := setField(settings, inp.Setting, inp.Value); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot set %q: %v", inp.Setting, err)), nil
	}
	if err := t.saveSettings(path, settings); err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot save settings: %v", err)), nil
	}
	out, _ := json.MarshalIndent(map[string]any{
		"setting":       inp.Setting,
		"previousValue": prev,
		"newValue":      inp.Value,
	}, "", "  ")
	return tool.TextResult(string(out)), nil
}

// rawSettings is a generic map for reading/writing settings.json.
type rawSettings map[string]any

func (t *ConfigTool) loadSettings(path string) rawSettings {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(rawSettings)
	}
	var s rawSettings
	_ = json.Unmarshal(data, &s)
	if s == nil {
		s = make(rawSettings)
	}
	return s
}

func (t *ConfigTool) saveSettings(path string, s rawSettings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Supported settings and their dot-path keys.
var supportedSettings = map[string]bool{
	"model":                    true,
	"permissions.defaultMode":  true,
	"permissions.allow":        true,
	"permissions.deny":         true,
	"env":                      true,
}

func getField(s rawSettings, key string) any {
	switch key {
	case "model":
		return s["model"]
	case "permissions.defaultMode":
		if p, ok := s["permissions"].(map[string]any); ok {
			return p["defaultMode"]
		}
		return nil
	case "permissions.allow":
		if p, ok := s["permissions"].(map[string]any); ok {
			return p["allow"]
		}
		return nil
	case "permissions.deny":
		if p, ok := s["permissions"].(map[string]any); ok {
			return p["deny"]
		}
		return nil
	case "env":
		return s["env"]
	default:
		return s[key]
	}
}

func setField(s rawSettings, key string, value any) error {
	if !supportedSettings[key] {
		return fmt.Errorf("unsupported setting %q (supported: model, permissions.defaultMode, permissions.allow, permissions.deny, env)", key)
	}
	switch key {
	case "model":
		s["model"] = value
	case "permissions.defaultMode":
		p := ensureMap(s, "permissions")
		p["defaultMode"] = value
	case "permissions.allow":
		p := ensureMap(s, "permissions")
		p["allow"] = value
	case "permissions.deny":
		p := ensureMap(s, "permissions")
		p["deny"] = value
	case "env":
		s["env"] = value
	default:
		s[key] = value
	}
	return nil
}

func ensureMap(s rawSettings, key string) map[string]any {
	if m, ok := s[key].(map[string]any); ok {
		return m
	}
	m := make(map[string]any)
	s[key] = m
	return m
}

const description = `Read or write conduit settings. Omit value to get current setting. Supported settings: model, permissions.defaultMode, permissions.allow, permissions.deny, env.`
