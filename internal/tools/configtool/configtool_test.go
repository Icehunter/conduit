package configtool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) (ct *ConfigTool, path string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "settings.json")
	ct = &ConfigTool{SettingsPath: path}
	return
}

func TestConfigTool_GetMissingReturnsNull(t *testing.T) {
	ct, _ := setup(t)
	res, err := ct.Execute(context.Background(), json.RawMessage(`{"setting":"model"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "null") {
		t.Errorf("expected null for missing setting; got: %s", res.Content[0].Text)
	}
}

func TestConfigTool_SetAndGet(t *testing.T) {
	ct, _ := setup(t)

	// Set model.
	res, err := ct.Execute(context.Background(), json.RawMessage(`{"setting":"model","value":"claude-opus-4-7"}`))
	if err != nil {
		t.Fatalf("Execute set: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %s", res.Content[0].Text)
	}

	// Get model back.
	res, err = ct.Execute(context.Background(), json.RawMessage(`{"setting":"model"}`))
	if err != nil {
		t.Fatalf("Execute get: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, "claude-opus-4-7") {
		t.Errorf("get returned: %s", res.Content[0].Text)
	}
}

func TestConfigTool_SetPermissionsDefaultMode(t *testing.T) {
	ct, path := setup(t)

	_, err := ct.Execute(context.Background(), json.RawMessage(`{"setting":"permissions.defaultMode","value":"plan"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Read the file directly to verify structure.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	perms, ok := raw["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions key missing; file: %s", data)
	}
	if perms["defaultMode"] != "plan" {
		t.Errorf("defaultMode = %v; want plan", perms["defaultMode"])
	}
}

func TestConfigTool_UnsupportedSetting(t *testing.T) {
	ct, _ := setup(t)
	res, err := ct.Execute(context.Background(), json.RawMessage(`{"setting":"unknown.key","value":"x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for unsupported setting")
	}
}

func TestConfigTool_InvalidInput(t *testing.T) {
	ct, _ := setup(t)
	res, err := ct.Execute(context.Background(), json.RawMessage(`not-json`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for invalid JSON")
	}
}

func TestConfigTool_IsReadOnly(t *testing.T) {
	ct := &ConfigTool{}
	if !ct.IsReadOnly(json.RawMessage(`{"setting":"model"}`)) {
		t.Error("GET should be read-only")
	}
	if ct.IsReadOnly(json.RawMessage(`{"setting":"model","value":"x"}`)) {
		t.Error("SET should not be read-only")
	}
}

func TestConfigTool_Metadata(t *testing.T) {
	ct := &ConfigTool{}
	if ct.Name() != "Config" {
		t.Errorf("Name = %q", ct.Name())
	}
	if !ct.IsConcurrencySafe(nil) {
		t.Error("should be concurrency safe")
	}
	var schema map[string]any
	if err := json.Unmarshal(ct.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid JSON: %v", err)
	}
}

func TestConfigTool_PreviousValueReturned(t *testing.T) {
	ct, path := setup(t)

	// Pre-seed with a value.
	_ = os.WriteFile(path, []byte(`{"model":"old-model"}`), 0o644)

	res, err := ct.Execute(context.Background(), json.RawMessage(`{"setting":"model","value":"new-model"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "old-model") {
		t.Errorf("previousValue not in response: %s", res.Content[0].Text)
	}
}
