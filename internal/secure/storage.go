// Package secure abstracts platform-specific secret storage.
//
// Uses github.com/zalando/go-keyring which provides:
//   - macOS: Keychain Services
//   - Linux: libsecret / Secret Service (D-Bus)
//   - Windows: Windows Credential Manager
//
// Service name: "conduit-credentials" — separate from CC's entries on all platforms.
// File fallback (~/.claude/.conduit-credentials.json) used when the platform
// keychain is unavailable (headless CI, no libsecret, etc.).
package secure

import (
	"errors"
	"fmt"
	"sync"

	gokeyring "github.com/zalando/go-keyring"
)

// ErrNotFound is returned by Storage.Get when no value exists for the key.
var ErrNotFound = errors.New("secure: not found")

// Storage is a per-key blob store.
type Storage interface {
	Get(service, key string) ([]byte, error)
	Set(service, key string, value []byte) error
	Delete(service, key string) error
}

// conduitService is the keychain service name for all conduit credentials.
// Deliberately different from CC's "Claude Code-credentials" on macOS.
const conduitService = "conduit-credentials"

// keyringStorage wraps go-keyring with the Storage interface.
type keyringStorage struct{}

func (k *keyringStorage) Get(_, key string) ([]byte, error) {
	val, err := gokeyring.Get(conduitService, key)
	if err != nil {
		return nil, ErrNotFound
	}
	return []byte(val), nil
}

func (k *keyringStorage) Set(_, key string, value []byte) error {
	if err := gokeyring.Set(conduitService, key, string(value)); err != nil {
		return fmt.Errorf("secure: keyring set %s: %w", key, err)
	}
	return nil
}

func (k *keyringStorage) Delete(_, key string) error {
	_ = gokeyring.Delete(conduitService, key)
	return nil
}

// NewDefault returns the platform keychain with a file fallback.
// The file fallback handles headless CI and systems without a secret service.
func NewDefault() Storage {
	return &fallbackStorage{
		primary:  &keyringStorage{},
		fallback: newFileStorage(),
	}
}

// fallbackStorage tries primary first; on error falls back to secondary.
// Set writes to primary only (not secondary) — the secondary is read-only
// fallback for environments where the primary is unavailable at read time.
type fallbackStorage struct {
	primary  Storage
	fallback Storage
}

func (f *fallbackStorage) Get(service, key string) ([]byte, error) {
	v, err := f.primary.Get(service, key)
	if err == nil {
		return v, nil
	}
	return f.fallback.Get(service, key)
}

func (f *fallbackStorage) Set(service, key string, value []byte) error {
	if err := f.primary.Set(service, key, value); err != nil {
		// Primary unavailable (e.g. no libsecret on Linux) — fall back to file.
		return f.fallback.Set(service, key, value)
	}
	return nil
}

func (f *fallbackStorage) Delete(service, key string) error {
	_ = f.primary.Delete(service, key)
	_ = f.fallback.Delete(service, key)
	return nil
}

// MemoryStorage is an in-process map-backed Storage for tests.
type MemoryStorage struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemoryStorage() *MemoryStorage { return &MemoryStorage{m: map[string][]byte{}} }

func memKey(service, key string) string { return service + "\x00" + key }

func (s *MemoryStorage) Get(service, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[memKey(service, key)]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (s *MemoryStorage) Set(service, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dup := make([]byte, len(value))
	copy(dup, value)
	s.m[memKey(service, key)] = dup
	return nil
}

func (s *MemoryStorage) Delete(service, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, memKey(service, key))
	return nil
}

func (*MemoryStorage) String() string { return "<secure.MemoryStorage>" }
