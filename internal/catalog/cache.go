package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// CachePath returns the path where the catalog is persisted on disk.
// The directory is created on first write; reads return an error if absent.
func CachePath(conduitDir string) string {
	return filepath.Join(conduitDir, "catalog.json")
}

// LoadCache reads the catalog from disk. Returns the builtin snapshot when
// the cache file is absent or unreadable without propagating the error (callers
// fall back to builtin automatically). Returns an error only for malformed JSON.
func LoadCache(conduitDir string) (*Catalog, error) {
	data, err := os.ReadFile(CachePath(conduitDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	c.Source = "cache"
	return &c, nil
}

// SaveCache writes the catalog to disk atomically via a temp file rename.
func SaveCache(conduitDir string, c *Catalog) error {
	if err := os.MkdirAll(conduitDir, 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	dst := CachePath(conduitDir)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Load returns the best available catalog in priority order:
//  1. Disk cache (if present and not stale relative to defaultTTL)
//  2. Built-in snapshot
//
// It never returns nil.
func Load(conduitDir string) *Catalog {
	c, err := LoadCache(conduitDir)
	if err == nil && c != nil && !c.IsStale(DefaultTTL) {
		return c
	}
	return Builtin()
}

// marshalIndentBuf is a test-helper to compare JSON round-trips.
func marshalIndentBuf(c *Catalog) ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

// catalogFromJSON is a convenience helper used in tests.
func catalogFromJSON(data []byte) (*Catalog, error) {
	var c Catalog
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}
