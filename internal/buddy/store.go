package buddy

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StoredCompanion is the persisted companion soul (bones are regenerated).
type StoredCompanion struct {
	Name         string `json:"name"`
	UserID       string `json:"userId"`
	HatchedAt    string `json:"hatchedAt,omitempty"`
	ForcedRarity string `json:"forcedRarity,omitempty"` // persisted rarity override
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
	path := storePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	fields, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	var typed map[string]json.RawMessage
	if err := json.Unmarshal(fields, &typed); err != nil {
		return err
	}
	for k, v := range typed {
		raw[k] = v
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
