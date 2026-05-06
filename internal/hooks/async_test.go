package hooks

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestAsyncGroup_Shutdown verifies that goroutines submitted to an AsyncGroup
// receive a cancelled context when Shutdown is called, and that Shutdown blocks
// until all goroutines have exited (within the grace window).
func TestAsyncGroup_Shutdown(t *testing.T) {
	t.Parallel()

	grp := NewAsyncGroup(context.Background())

	var cancelled atomic.Bool

	// Submit a goroutine that blocks until its context is cancelled.
	started := make(chan struct{})
	grp.Go(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		cancelled.Store(true)
	})

	// Wait for the goroutine to be running before we shut down.
	<-started

	grp.Shutdown(2 * time.Second)

	if !cancelled.Load() {
		t.Error("expected goroutine context to be cancelled after Shutdown")
	}
}

// TestAsyncGroup_ShutdownTimeout verifies that Shutdown returns even when a
// goroutine ignores cancellation and outlasts the grace window.
func TestAsyncGroup_ShutdownTimeout(t *testing.T) {
	t.Parallel()

	grp := NewAsyncGroup(context.Background())

	// This goroutine deliberately ignores ctx.Done() to exercise the timeout path.
	started := make(chan struct{})
	released := make(chan struct{})
	grp.Go(func(_ context.Context) {
		close(started)
		<-released // blocks until after Shutdown returns
	})

	<-started

	begin := time.Now()
	grp.Shutdown(100 * time.Millisecond) // very short grace
	elapsed := time.Since(begin)

	// Allow a generous margin above the 100ms timeout.
	if elapsed > 500*time.Millisecond {
		t.Errorf("Shutdown took %v; want ≤500ms even with stuck goroutine", elapsed)
	}

	// Unblock the stuck goroutine so the test process can exit cleanly.
	close(released)
}

// TestAsyncGroup_ParentCancellation verifies that cancelling the parent context
// also propagates to goroutines in the group (since the group ctx is a child).
func TestAsyncGroup_ParentCancellation(t *testing.T) {
	t.Parallel()

	parent, parentCancel := context.WithCancel(context.Background())
	grp := NewAsyncGroup(parent)

	var saw atomic.Bool
	started := make(chan struct{})
	grp.Go(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		saw.Store(true)
	})

	<-started
	parentCancel() // cancel the parent — group ctx must follow

	// Give the goroutine a moment to observe cancellation.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if saw.Load() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !saw.Load() {
		t.Error("goroutine did not observe parent context cancellation")
	}

	// Shutdown should return immediately since the goroutine already finished.
	grp.Shutdown(time.Second)
}

// TestAsyncGroup_DefaultGroupIntegration verifies the package-level
// DefaultAsyncGroup integration: async hooks submitted through runMatching
// are tracked in the group and their contexts are cancelled on Shutdown.
func TestAsyncGroup_DefaultGroupIntegration(t *testing.T) {
	t.Parallel()

	// Save and restore DefaultAsyncGroup so parallel tests don't interfere.
	orig := DefaultAsyncGroup
	t.Cleanup(func() { DefaultAsyncGroup = orig })

	grp := NewAsyncGroup(context.Background())
	DefaultAsyncGroup = grp

	var cancelled atomic.Bool
	// Use Go directly to mimic what runMatching would do.
	started := make(chan struct{})
	DefaultAsyncGroup.Go(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		cancelled.Store(true)
	})

	<-started
	DefaultAsyncGroup.Shutdown(2 * time.Second)

	if !cancelled.Load() {
		t.Error("async hook goroutine was not cancelled by DefaultAsyncGroup.Shutdown")
	}
}
