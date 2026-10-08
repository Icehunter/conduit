package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const schemaURL = "https://raw.githubusercontent.com/icehunter/conduit/main/schemas/conduit.schema.json"

// TestSchemaKeySurvivesSaves checks that the editor-hint "$schema" key is
// ignored when loading and kept by both the raw-key and typed save paths.
func TestSchemaKeySurvivesSaves(t *testing.T) {
	tests := []struct {
		name string
		save func(t *testing.T)
	}{
		{"raw key", func(t *testing.T) {
			if err := SaveConduitRawKey("someKey", "v"); err != nil {
				t.Fatal(err)
			}
		}},
		{"typed model save", func(t *testing.T) {
			if err := SaveConduitModel("claude-opus-5-5"); err != nil {
				t.Fatal(err)
			}
		}},
		{"typed theme save", func(t *testing.T) {
			if err := SaveConduitTheme("dark"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CONDUIT_CONFIG_DIR", dir)
			path := filepath.Join(dir, "conduit.json")
			if err := os.WriteFile(path, []byte(`{"$schema":"`+schemaURL+`","model":"m"}`), 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := LoadConduitConfig()
			if err != nil {
				t.Fatalf("load with $schema: %v", err)
			}
			if cfg.Model != "m" {
				t.Errorf("Model = %q, want m", cfg.Model)
			}

			tt.save(t)

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			var got string
			_ = json.Unmarshal(raw["$schema"], &got)
			if got != schemaURL {
				t.Errorf("$schema after save = %q, want %q\n%s", got, schemaURL, data)
			}
		})
	}
}

func TestSettingsLoadIgnoresSchemaKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"$schema":"x","model":"m"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := readFile(path)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if s.Model != "m" {
		t.Errorf("Model = %q, want m", s.Model)
	}
}
