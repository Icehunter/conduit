package buddy

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StoredCompanion is the persisted companion soul (bones are regenerated).
type StoredCompanion struct {
	Name      string `json:"name"`
	UserID    string `json:"userId"`
	HatchedAt string `json:"hatchedAt,omitempty"`
}

func storePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "buddy.json")
}

// Load reads the stored companion. Returns nil if none exists yet.
func Load() (*StoredCompanion, error) {
	data, err := os.ReadFile(storePath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sc StoredCompanion
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

// Save persists the companion soul.
func Save(sc *StoredCompanion) error {
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(storePath(), data, 0o600)
}
