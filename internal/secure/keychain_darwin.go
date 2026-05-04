//go:build darwin

package secure

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// macOS keychain storage — mirrors Claude Code's macOsKeychainStorage.ts.
// Uses the `security` CLI to read/write generic password items.
//
// Service name: "conduit-credentials"  (CC uses "Claude Code-credentials")
// Account:      current username
//
// Data is stored hex-encoded (same as CC) to avoid shell-escaping issues.
const keychainService = "conduit-credentials"

type keychainStorage struct {
	mu    sync.Mutex
	cache map[string][]byte // in-memory cache: memKey → value
}

func newKeychainStorage() *keychainStorage {
	return &keychainStorage{cache: map[string][]byte{}}
}

func username() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	out, err := exec.Command("id", "-un").Output()
	if err != nil {
		return "conduit-user"
	}
	return strings.TrimSpace(string(out))
}

// legacyKey is the single-account keychain account name — no suffix, matching
// the convention CC uses for its own single-entry format.
const legacyKey = "oauth-tokens"

// keychainAccount builds the keychain account name for a given storage key.
// Single-account legacy key gets just the username; scoped keys append the key.
func keychainAccount(key string) string {
	u := username()
	if key == legacyKey {
		return u
	}
	return u + ":" + key
}

func (s *keychainStorage) Get(service, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mk := memKey(service, key)
	if v, ok := s.cache[mk]; ok {
		return v, nil
	}

	acct := keychainAccount(key)
	svc := keychainServiceName(service)
	out, err := exec.Command("security", "find-generic-password",
		"-a", acct, "-s", svc, "-w").Output()
	if err != nil {
		return nil, ErrNotFound
	}
	hexStr := strings.TrimSpace(string(out))
	data, err := hex.DecodeString(hexStr)
	if err != nil {
		// Stored as plain text (old format) — return as-is.
		data = []byte(hexStr)
	}
	s.cache[mk] = data
	return data, nil
}

func (s *keychainStorage) Set(service, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	acct := keychainAccount(key)
	svc := keychainServiceName(service)
	hexVal := hex.EncodeToString(value)

	// Delete existing entry first (add-or-update pattern).
	_ = exec.Command("security", "delete-generic-password",
		"-a", acct, "-s", svc).Run()

	cmd := exec.Command("security", "add-generic-password",
		"-a", acct, "-s", svc, "-w", hexVal)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain: write %s/%s: %v — %s", service, key, err, strings.TrimSpace(string(out)))
	}

	mk := memKey(service, key)
	s.cache[mk] = value
	return nil
}

func (s *keychainStorage) Delete(service, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	acct := keychainAccount(key)
	svc := keychainServiceName(service)
	_ = exec.Command("security", "delete-generic-password",
		"-a", acct, "-s", svc).Run()
	delete(s.cache, memKey(service, key))
	return nil
}

// keychainServiceName maps any internal service identifier to the macOS
// keychain service name. All conduit entries use "conduit-credentials" so
// they're separate from CC's "Claude Code-credentials" entries.
func keychainServiceName(_ string) string {
	return keychainService
}

// NewDefault on macOS returns a keychain storage with file fallback.
func NewDefault() Storage {
	return &fallbackStorage{
		primary:  newKeychainStorage(),
		fallback: newLinuxFileStorage(),
	}
}

// fallbackStorage tries primary; on any error falls back to secondary.
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
	err := f.primary.Set(service, key, value)
	if err != nil {
		// Best-effort file fallback.
		_ = f.fallback.Set(service, key, value)
	}
	return err
}

func (f *fallbackStorage) Delete(service, key string) error {
	_ = f.primary.Delete(service, key)
	_ = f.fallback.Delete(service, key)
	return nil
}
