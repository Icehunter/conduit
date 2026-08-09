package secure

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// FileStorage persists secrets to a JSON file with mode 0600 in a directory
// owned by the current user (mode 0700). It is a stop-gap for environments
// that lack a real OS keychain — adequate for development and CI but NOT
// recommended for production credential storage on shared machines.
//
// The on-disk format is a flat JSON object keyed by service\x00key. Reads
// and writes hold an in-memory cache to avoid spurious file IO under heavy
// access.
type FileStorage struct {
	path string

	mu     sync.Mutex
	loaded bool
	// loadedMod/loadedSize fingerprint the file as of the last read, so a write
	// by another process is picked up instead of being masked by the cache.
	loadedMod  time.Time
	loadedSize int64
	cache      map[string]string // base64-encoded values
}

// NewFileStorage returns a FileStorage rooted at the given file path.
// The parent directory is created with mode 0700 on first write.
//
// On read it performs a permissive permission check matching the TS
// reference's _x_ helper (decoded/1390.js:66): the file must exist and
// must not be group/world readable or writable, otherwise we refuse to
// load — this prevents another local user from planting a token.
func NewFileStorage(path string) *FileStorage {
	return &FileStorage{path: path, cache: map[string]string{}}
}

// newFileStorage returns a FileStorage at ~/.claude/.conduit-credentials.json.
// Used as the fallback when the platform keychain is unavailable.
func newFileStorage() *FileStorage {
	home, err := os.UserHomeDir()
	if err != nil {
		return NewFileStorage("")
	}
	return NewFileStorage(filepath.Join(home, ".claude", ".conduit-credentials.json"))
}

func (s *FileStorage) loadLocked() error {
	// The cache is keyed to the file's mtime and size rather than merely to
	// "have we loaded once". Another conduit process refreshing a token writes
	// this same file, and a cache pinned for the process lifetime would make
	// that write permanently invisible — so this process would keep presenting
	// a refresh token the peer has already consumed and revoke the session.
	info, statErr := os.Stat(s.path)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			s.loaded = true
			return nil // empty file is fine
		}
		return fmt.Errorf("secure: stat %s: %w", s.path, statErr)
	}
	if s.loaded && info.ModTime().Equal(s.loadedMod) && info.Size() == s.loadedSize {
		return nil
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.loaded = true
			return nil // raced with a delete; empty is fine
		}
		return fmt.Errorf("secure: read %s: %w", s.path, err)
	}
	// Permission check: refuse to load if the file is group/world-writable
	// or world-readable. Mirrors decoded/1390.js:66-83.
	// Skipped on Windows where Unix permission bits are not meaningful.
	if runtime.GOOS != "windows" {
		if info, err := os.Lstat(s.path); err == nil {
			mode := info.Mode().Perm()
			if mode&0o077 != 0 {
				return fmt.Errorf("secure: refusing to load credentials at %s: file has group/world-readable or writable bits (mode %o); run `chmod 600 %s`", s.path, mode, s.path)
			}
		}
	}
	var cache map[string]string
	if err := json.Unmarshal(raw, &cache); err != nil {
		return fmt.Errorf("secure: parse %s: %w", s.path, err)
	}
	if cache == nil {
		cache = map[string]string{}
	}
	s.cache = cache
	s.loaded = true
	s.loadedMod = info.ModTime()
	s.loadedSize = info.Size()
	return nil
}

func (s *FileStorage) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secure: mkdir %s: %w", dir, err)
	}
	buf, err := json.Marshal(s.cache)
	if err != nil {
		return fmt.Errorf("secure: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("secure: write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secure: rename: %w", err)
	}
	// Re-fingerprint so our own write doesn't look like someone else's and
	// force a pointless re-read on the next Get.
	if info, err := os.Stat(s.path); err == nil {
		s.loaded = true
		s.loadedMod = info.ModTime()
		s.loadedSize = info.Size()
	}
	return nil
}

// Get implements Storage.
func (s *FileStorage) Get(service, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	raw, ok := s.cache[memKey(service, key)]
	if !ok {
		return nil, ErrNotFound
	}
	return []byte(raw), nil
}

// Set implements Storage.
func (s *FileStorage) Set(service, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	s.cache[memKey(service, key)] = string(value)
	return s.saveLocked()
}

// Delete implements Storage.
func (s *FileStorage) Delete(service, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	delete(s.cache, memKey(service, key))
	return s.saveLocked()
}

// String never reveals the file contents.
func (*FileStorage) String() string { return "<secure.FileStorage>" }
