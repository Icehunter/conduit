package settings

import (
	"os"
	"path/filepath"
	"testing"

	internalmodel "github.com/icehunter/conduit/internal/model"
)

func intPtr(v int) *int { return &v }

func TestEffectiveCompactionTriggerCap(t *testing.T) {
	tests := []struct {
		name string
		m    *Merged
		want int
	}{
		{"nil merged", nil, internalmodel.DefaultAutoCompactTriggerCap},
		{"unset", &Merged{}, internalmodel.DefaultAutoCompactTriggerCap},
		{"configured", &Merged{CompactionMaxTriggerTokens: intPtr(100_000)}, 100_000},
		{"zero is uncapped", &Merged{CompactionMaxTriggerTokens: intPtr(0)}, 0},
		{"negative is uncapped", &Merged{CompactionMaxTriggerTokens: intPtr(-5)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.EffectiveCompactionTriggerCap(); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLoad_CompactionMaxTriggerTokens(t *testing.T) {
	tests := []struct {
		name string
		json string
		want int
	}{
		{"absent", `{"schemaVersion":1}`, internalmodel.DefaultAutoCompactTriggerCap},
		{"set", `{"schemaVersion":1,"compaction":{"maxTriggerTokens":120000}}`, 120_000},
		{"explicit zero", `{"schemaVersion":1,"compaction":{"maxTriggerTokens":0}}`, 0},
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
			if got := merged.EffectiveCompactionTriggerCap(); got != tt.want {
				t.Errorf("cap = %d, want %d", got, tt.want)
			}
		})
	}
}
