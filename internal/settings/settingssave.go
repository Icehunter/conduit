package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SaveOutputStyle persists the active output style name to the user settings file.
func SaveOutputStyle(name string) error {
	return SaveConduitOutputStyle(name)
}

// SetPluginEnabled sets enabledPlugins[pluginID] in the user settings file.
func SetPluginEnabled(pluginID string, enabled bool) error {
	return SaveConduitEnabledPlugin(pluginID, enabled)
}

// PluginEnabled reports whether an installed plugin should be active. The
// enabledPlugins map is sparse: missing entries are enabled by default.
func PluginEnabled(pluginID string) bool {
	cfg, err := LoadConduitConfig()
	if err != nil || cfg.EnabledPlugins == nil {
		return true
	}
	enabled, ok := cfg.EnabledPlugins[pluginID]
	if !ok {
		return true
	}
	return enabled
}

// RemovePlugin removes a plugin from enabledPlugins in the user settings file.
func RemovePlugin(pluginID string) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		if cfg.EnabledPlugins != nil {
			delete(cfg.EnabledPlugins, pluginID)
		}
	})
}

// ApproveMcpjsonServer records an approval decision for a project-scope MCP
// server. Choices: "yes" → add to enabledMcpjsonServers; "yes_all" → set
// enableAllProjectMcpServers=true and add to enabled; "no" → add to
// disabledMcpjsonServers. Idempotent; preserves all other settings keys.
func ApproveMcpjsonServer(name, choice string) error {
	path := ConduitSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := readRawObject(path)
	if err != nil {
		return err
	}

	enabled := decodeStringList(raw["enabledMcpjsonServers"])
	disabled := decodeStringList(raw["disabledMcpjsonServers"])

	switch choice {
	case "yes", "yes_all":
		enabled = appendUnique(enabled, name)
		disabled = removeFrom(disabled, name)
		if choice == "yes_all" {
			raw["enableAllProjectMcpServers"] = json.RawMessage("true")
		}
	case "no":
		disabled = appendUnique(disabled, name)
		enabled = removeFrom(enabled, name)
	default:
		return fmt.Errorf("ApproveMcpjsonServer: unknown choice %q", choice)
	}

	if b, err := json.Marshal(enabled); err == nil {
		raw["enabledMcpjsonServers"] = b
	}
	if b, err := json.Marshal(disabled); err == nil {
		raw["disabledMcpjsonServers"] = b
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func decodeStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func removeFrom(list []string, s string) []string {
	out := list[:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// SavePermissionsField updates a single sub-field under "permissions" in the
// user settings file (e.g. "defaultMode", "allow", "deny") while preserving
// the other sub-fields and all unrelated top-level keys.
//
// Pass value=nil to delete the sub-field. The "permissions" object itself is
// removed if it becomes empty.
func SavePermissionsField(field string, value interface{}) error {
	if field == "" {
		return fmt.Errorf("settings: SavePermissionsField: field is required")
	}
	return savePermissionsField(UserSettingsPath(), "SavePermissionsField", field, value)
}

// SaveConduitPermissionsField updates a single sub-field under "permissions"
// in ~/.conduit/conduit.json. Conduit-owned runtime preferences, such as the
// active permission mode, should use this overlay instead of mutating Claude
// Code's settings.json.
func SaveConduitPermissionsField(field string, value interface{}) error {
	if field == "" {
		return fmt.Errorf("settings: SaveConduitPermissionsField: field is required")
	}
	return savePermissionsField(ConduitSettingsPath(), "SaveConduitPermissionsField", field, value)
}

func savePermissionsField(path, op, field string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("settings: %s: mkdir: %w", op, err)
	}
	raw, err := readRawObject(path)
	if err != nil {
		return fmt.Errorf("settings: %s: read: %w", op, err)
	}

	perms := make(map[string]json.RawMessage)
	if r, ok := raw["permissions"]; ok && len(r) > 0 {
		if err := json.Unmarshal(r, &perms); err != nil {
			return fmt.Errorf("settings: %s: parse permissions: %w", op, err)
		}
	}

	if value == nil {
		delete(perms, field)
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("settings: %s: marshal value: %w", op, err)
		}
		perms[field] = encoded
	}

	if len(perms) == 0 {
		delete(raw, "permissions")
	} else {
		encoded, err := json.Marshal(perms)
		if err != nil {
			return fmt.Errorf("settings: %s: marshal permissions: %w", op, err)
		}
		raw["permissions"] = encoded
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: %s: marshal settings: %w", op, err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("settings: %s: write: %w", op, err)
	}
	return nil
}

// SaveRawKey persists an arbitrary key/value to the user settings file using
// raw-map preservation so no other fields are disturbed.
func SaveRawKey(key string, value interface{}) error {
	return SaveConduitRawKey(key, value)
}

// SaveConduitRawKey persists a conduit-only key/value to ~/.conduit/conduit.json
// using raw-map preservation so no other fields are disturbed.
func SaveConduitRawKey(key string, value interface{}) error {
	path := ConduitSettingsPath()
	return saveRawKey(path, key, value)
}

func saveRawKey(path, key string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := readRawObject(path)
	if err != nil {
		return err
	}
	if value == nil {
		delete(raw, key)
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		raw[key] = encoded
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func readRawObject(path string) (map[string]json.RawMessage, error) {
	raw := make(map[string]json.RawMessage)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return raw, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}
