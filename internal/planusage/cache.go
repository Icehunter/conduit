package planusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const cacheFile = "plan_usage_cache.json"

// CacheEntry is the on-disk representation of a successful fetch plus any
// active backoff deadline.
type CacheEntry struct {
	Info         Info      `json:"info"`
	CachedAt     time.Time `json:"cached_at"`
	BackoffUntil time.Time `json:"backoff_until,omitempty"`
}

// LoadCache reads the cache file from dir. Returns a zero-value CacheEntry
// (not an error) when the file is missing or corrupt — callers should treat a
// zero CachedAt as "no cache".
func LoadCache(dir string) (CacheEntry, error) {
	data, err := os.ReadFile(filepath.Join(dir, cacheFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CacheEntry{}, nil
		}
		return CacheEntry{}, fmt.Errorf("planusage: read cache: %w", err)
	}
	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return CacheEntry{}, fmt.Errorf("planusage: decode cache: %w", err)
	}
	return entry, nil
}

// SaveCache writes entry to dir atomically (temp file + rename).
func SaveCache(dir string, entry CacheEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("planusage: encode cache: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("planusage: mkdir cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".plan_usage_cache_*.json")
	if err != nil {
		return fmt.Errorf("planusage: create temp cache: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("planusage: write temp cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("planusage: close temp cache: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, cacheFile)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("planusage: rename cache: %w", err)
	}
	return nil
}
