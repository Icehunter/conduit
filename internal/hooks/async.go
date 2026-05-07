package hooks

import (
	"context"
	"sync"
	"time"
)

// AsyncGroup tracks async hook goroutines so they can be cancelled and drained
// at session shutdown. Create one per session via NewAsyncGroup, then assign it
// to DefaultAsyncGroup before running any hooks.
type AsyncGroup struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// SubAgentRunner is an optional function injected by the agent loop that
	// can execute a sub-agent prompt. When non-nil, prompt/agent hooks invoke
	// it to evaluate hook prompts via the LLM.
	SubAgentRunner func(ctx context.Context, prompt string) (string, error)
}

// NewAsyncGroup creates an AsyncGroup whose goroutines run under a child of
// parent. Cancelling parent also cancels the group.
func NewAsyncGroup(parent context.Context) *AsyncGroup {
	ctx, cancel := context.WithCancel(parent)
	return &AsyncGroup{ctx: ctx, cancel: cancel}
}

// Go runs f in a background goroutine tracked by the group. f receives the
// group's context, which is cancelled by Shutdown.
func (g *AsyncGroup) Go(f func(ctx context.Context)) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		f(g.ctx)
	}()
}

// Shutdown cancels the group's context and waits up to d for all goroutines to
// finish. Goroutines still running after d are abandoned (not killed).
func (g *AsyncGroup) Shutdown(d time.Duration) {
	g.cancel()
	done := make(chan struct{})
	go func() { g.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// DefaultAsyncGroup is the session-scoped group used by runMatching for async
// hooks. Prefer SetDefaultAsyncGroup / GetDefaultAsyncGroup for concurrent
// access. Direct assignment is supported for the single-session startup path
// in agent/loop.go (which assigns exactly once before any hooks run).
// When nil, async hooks fall back to untracked goroutines (original behaviour).
var DefaultAsyncGroup *AsyncGroup //nolint:gochecknoglobals

var defaultAsyncGroupMu sync.Mutex

// SetDefaultAsyncGroup safely sets DefaultAsyncGroup. Use this when multiple
// sessions could run concurrently.
func SetDefaultAsyncGroup(g *AsyncGroup) {
	defaultAsyncGroupMu.Lock()
	defer defaultAsyncGroupMu.Unlock()
	DefaultAsyncGroup = g
}

// GetDefaultAsyncGroup safely reads DefaultAsyncGroup. Use this when multiple
// sessions could run concurrently.
func GetDefaultAsyncGroup() *AsyncGroup {
	defaultAsyncGroupMu.Lock()
	defer defaultAsyncGroupMu.Unlock()
	return DefaultAsyncGroup
}
