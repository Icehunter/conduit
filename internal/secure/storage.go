// Package secure abstracts platform-specific secret storage.
//
// On macOS we use the Keychain, on Linux libsecret (Secret Service), and on
// Windows the Credential Manager — all wrapped through a single Storage
// interface so call sites in internal/auth and internal/api don't depend on
// the platform layer at compile time.
//
// This file ships the interface and an in-memory implementation suitable
// for tests. The real OS-backed Storage is bound in OS-specific files
// (storage_keychain.go etc.) added in a follow-up commit so we don't
// require a network fetch of the keyring dependency in this milestone's
// initial check-in.
package secure

import (
	"errors"
	"sync"
)

// ErrNotFound is returned by Storage.Get when no value exists for the key.
var ErrNotFound = errors.New("secure: not found")

// Storage is a per-key blob store. Implementations must scrub secrets from
// any stringification path: Storage values are SECRETS by definition.
type Storage interface {
	// Get returns the secret for the given service+key pair. ErrNotFound
	// when no secret exists; other errors are platform problems.
	Get(service, key string) ([]byte, error)
	// Set creates or replaces the secret.
	Set(service, key string, value []byte) error
	// Delete removes the secret. Idempotent — deleting a missing key is
	// not an error.
	Delete(service, key string) error
}

// MemoryStorage is an in-process map-backed Storage useful for tests and
// for environments where no platform keychain is available (e.g. headless
// CI with no D-Bus). It does not persist across processes.
type MemoryStorage struct {
	mu sync.Mutex
	m  map[string][]byte
}

// NewMemoryStorage returns an empty in-memory Storage.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{m: map[string][]byte{}}
}

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

// String never reveals the contents of MemoryStorage.
func (*MemoryStorage) String() string { return "<secure.MemoryStorage>" }
