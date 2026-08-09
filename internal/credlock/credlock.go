// Package credlock provides a cross-process advisory lock for credential
// refresh, shared by the Claude, Codex and Copilot auth paths.
package credlock

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// LockTimeout bounds how long one process waits for another to finish a
// token exchange. Long enough for a slow round-trip, short enough that a stuck
// peer degrades to "refresh anyway" rather than hanging the agent.
const LockTimeout = 45 * time.Second

// lockPoll is how often the wait re-checks. Token exchange is a network
// round-trip, so sub-second polling is plenty.
const lockPoll = 50 * time.Millisecond

// Acquire takes a cross-process advisory lock covering credential refresh for
// one account. key is any stable identifier — an email, a credential name.
//
// In-process coordination (a mutex, singleflight) cannot see other conduit
// processes. The OAuth token endpoints rotate the refresh token on every
// exchange and reject one they have already consumed, so two conduit windows
// refreshing the same account revoke each other. This is the piece that stops
// that.
//
// The returned release func is always safe to call. An error means the lock
// could not be taken; callers should proceed anyway rather than fail the
// request, since refreshing without the lock is still better than not
// authenticating at all.
func Acquire(ctx context.Context, key string) (func(), error) {
	path, err := lockPath(key)
	if err != nil {
		return func() {}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}, fmt.Errorf("credlock: create lock dir: %w", err)
	}

	fl := flock.New(path)
	waitCtx, cancel := context.WithTimeout(ctx, LockTimeout)
	defer cancel()

	locked, err := fl.TryLockContext(waitCtx, lockPoll)
	if err != nil {
		return func() {}, fmt.Errorf("credlock: wait for refresh lock: %w", err)
	}
	if !locked {
		return func() {}, fmt.Errorf("credlock: refresh lock busy after %s", LockTimeout)
	}
	return func() { _ = fl.Unlock() }, nil
}

// refreshLockPath names the lock file for an account. The email is hashed
// rather than used directly so the filename carries no address and stays valid
// on every filesystem.
func lockPath(key string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("credlock: locate home dir: %w", err)
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(home, ".conduit", "locks", fmt.Sprintf("refresh-%x.lock", sum[:8])), nil
}
