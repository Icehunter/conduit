package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_SafeguardToggles(t *testing.T) {
	tests := []struct {
		name          string
		json          string
		wantRedactOff bool
		wantRepOff    bool
	}{
		{"absent keeps both on", `{"schemaVersion":1}`, false, false},
		{"redaction off", `{"schemaVersion":1,"redactSecrets":false}`, true, false},
		{"redaction explicitly on", `{"schemaVersion":1,"redactSecrets":true}`, false, false},
		{"repetition guard off", `{"schemaVersion":1,"ttsr":{"repetitionGuard":false}}`, false, true},
		{"ttsr block without the key", `{"schemaVersion":1,"ttsr":{}}`, false, false},
		{"both off", `{"schemaVersion":1,"redactSecrets":false,"ttsr":{"repetitionGuard":false}}`, true, true},
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
			merged, err := Load(filepath.Join(dir, "project"))
			if err != nil {
				t.Fatal(err)
			}
			if merged.SecretRedactionOff != tt.wantRedactOff {
				t.Errorf("SecretRedactionOff = %v, want %v", merged.SecretRedactionOff, tt.wantRedactOff)
			}
			if merged.RepetitionGuardOff != tt.wantRepOff {
				t.Errorf("RepetitionGuardOff = %v, want %v", merged.RepetitionGuardOff, tt.wantRepOff)
			}
		})
	}
}
