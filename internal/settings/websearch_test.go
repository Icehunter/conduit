package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_WebSearchKeyless(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"absent is auto", `{"schemaVersion":1}`, "auto"},
		{"always", `{"schemaVersion":1,"webSearch":{"keyless":"always"}}`, "always"},
		{"never, case and space tolerant", `{"schemaVersion":1,"webSearch":{"keyless":" Never "}}`, "never"},
		{"unknown value is auto", `{"schemaVersion":1,"webSearch":{"keyless":"yes"}}`, "auto"},
		{"empty block is auto", `{"schemaVersion":1,"webSearch":{}}`, "auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
			if err := os.MkdirAll(filepath.Join(dir, ".conduit"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".conduit", "conduit.json"), []byte(tt.json), 0o600); err != nil {
				t.Fatal(err)
			}
			merged, err := Load("")
			if err != nil {
				t.Fatal(err)
			}
			if merged.WebSearchKeyless != tt.want {
				t.Errorf("WebSearchKeyless = %q, want %q", merged.WebSearchKeyless, tt.want)
			}
		})
	}
}
