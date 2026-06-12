// Package ccr implements a content-addressed, TTL-backed retrieve store
// (Compress-Cache-Retrieve). It deduplicates stored content by SHA-256
// prefix and lets callers retrieve the full text, a line slice, or a
// filtered view by handle string ("ccr:<key>").
//
// Files are stored under ConduitDir()/ccr/ and expire after 7 days.
package ccr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// Retention matches truncate.Retention: stored files live for 7 days.
const Retention = 7 * 24 * time.Hour

// handlePrefix is prepended to every handle returned by Put.
const handlePrefix = "ccr:"

// keyLen is the number of hex characters used as the content key
// (= 8 bytes of SHA-256 prefix encoded as 16 hex chars).
const keyLen = 16

// Store is a content-addressed CCR store backed by a directory.
type Store struct {
	dir string
}

// FileInfo describes a file in the store.
type FileInfo struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
}

// DefaultStore returns a Store rooted at ConduitDir()/ccr/.
func DefaultStore() *Store {
	return &Store{dir: filepath.Join(settings.ConduitDir(), "ccr")}
}

// NewStore returns a Store backed by the given directory.
// Intended for testing; production code should use DefaultStore.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Put stores content under its SHA-256-derived key and returns a handle
// like "ccr:<key>". If identical content was already stored the existing
// file is left on disk; the same handle is returned (idempotent).
func (s *Store) Put(content string) (string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("ccr: mkdir: %w", err)
	}

	key := contentKey(content)
	path := s.filePath(key)

	// If the file already exists we only need to touch its mtime so the
	// retention clock resets — the content is identical by construction.
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now) // best-effort; idempotency is the goal
		return handlePrefix + key, nil
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("ccr: write: %w", err)
	}
	return handlePrefix + key, nil
}

// Get retrieves the full stored content for handle.
// Returns an error if the handle format is invalid or the file is missing.
func (s *Store) Get(handle string) (string, error) {
	key, err := parseHandle(handle)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(s.filePath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("ccr: get %q: not found", handle)
		}
		return "", fmt.Errorf("ccr: get %q: %w", handle, err)
	}
	return string(data), nil
}

// Slice returns lines [offset, offset+limit) of the stored content.
// offset is 0-based. limit=0 means all lines from offset.
func (s *Store) Slice(handle string, offset, limit int) (string, error) {
	text, err := s.Get(handle)
	if err != nil {
		return "", err
	}
	lines := strings.Split(text, "\n")

	if offset < 0 {
		offset = 0
	}
	if offset >= len(lines) {
		return "", nil
	}
	lines = lines[offset:]

	if limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n"), nil
}

// Cleanup removes files older than Retention from the store directory.
func (s *Store) Cleanup() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("ccr: cleanup readdir: %w", err)
	}

	cutoff := time.Now().Add(-Retention)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "ccr_") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(s.dir, entry.Name())) // best-effort
		}
	}
	return nil
}

// ListFiles returns stored files sorted newest-first.
func (s *Store) ListFiles() ([]FileInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ccr: listfiles readdir: %w", err)
	}

	var files []FileInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "ccr_") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, FileInfo{
			Path:    filepath.Join(s.dir, entry.Name()),
			Name:    entry.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})
	return files, nil
}

// Stats returns the count and total byte size of stored files.
func (s *Store) Stats() (int, int64, error) {
	files, err := s.ListFiles()
	if err != nil {
		return 0, 0, err
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return len(files), total, nil
}

// contentKey returns the first keyLen hex chars of SHA-256(content).
func contentKey(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:keyLen]
}

// filePath returns the on-disk path for a given key.
func (s *Store) filePath(key string) string {
	return filepath.Join(s.dir, "ccr_"+key+".txt")
}

// parseHandle validates the "ccr:<key>" format and returns the key.
func parseHandle(handle string) (string, error) {
	if !strings.HasPrefix(handle, handlePrefix) {
		return "", fmt.Errorf("ccr: invalid handle %q: must start with %q", handle, handlePrefix)
	}
	key := handle[len(handlePrefix):]
	if len(key) != keyLen {
		return "", fmt.Errorf("ccr: invalid handle %q: key must be %d hex chars", handle, keyLen)
	}
	// Validate hex.
	if _, err := hex.DecodeString(key); err != nil {
		return "", fmt.Errorf("ccr: invalid handle %q: key is not hex: %w", handle, err)
	}
	return key, nil
}
