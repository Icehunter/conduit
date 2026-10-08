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

func TestConfigTool_RejectsCapabilityGrantingKeys(t *testing.T) {
	tests := []struct{ name, input string }{
		{"defaultMode", `{"setting":"permissions.defaultMode","value":"bypassPermissions"}`},
		{"allow", `{"setting":"permissions.allow","value":["Bash"]}`},
		{"deny", `{"setting":"permissions.deny","value":[]}`},
		{"env", `{"setting":"env","value":{"X":"1"}}`},
		{"hooks", `{"setting":"hooks","value":{}}`},
		{"mcpServers", `{"setting":"mcpServers","value":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct, path := setup(t)
			res, err := ct.Execute(context.Background(), json.RawMessage(tc.input))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !res.IsError {
				t.Errorf("expected error result, got %v", res.Content)
			}
			if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "bypassPermissions") {
				t.Errorf("settings file was modified: %s", data)
			}
		})
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

func TestConfigTool_DoesNotOverwriteInvalidJSON(t *testing.T) {
	ct, path := setup(t)
	before := []byte(`{"model":`)
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ct.Execute(context.Background(), json.RawMessage(`{"setting":"model","value":"new-model"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for invalid existing settings JSON")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("invalid JSON file was overwritten: %q", after)
	}
}
