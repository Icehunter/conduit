//go:build !windows

package bashtool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/tool"
)

// TestBash_TimeoutKillsChildren: a timeout kills the command's whole process
// group, not just the shell, so a child it started can neither outlive the
// call nor hold the call open.
func TestBash_TimeoutKillsChildren(t *testing.T) {
	// A child that inherits the output pipe keeps it open after the shell
	// is killed, so killing only the shell leaves Execute waiting for the
	// child to exit on its own.
	// bash exits at once but a background child keeps the output pipe; the
	// call must return after waitDelay, not when the child exits.
	t.Run("backgrounded child without wait", func(t *testing.T) {
		res := executeWithin(t, Input{Command: "sleep 30 & echo started", Timeout: 60000}, 10*time.Second)
		if res.IsError || !strings.Contains(res.Content[0].Text, "started") {
			t.Fatalf("want a normal result with output, got IsError=%v %q", res.IsError, res.Content[0].Text)
		}
	})

	t.Run("child holding the output pipe", func(t *testing.T) {
		res := executeWithin(t, Input{Command: "sleep 30 & wait", Timeout: 300}, 10*time.Second)
		if !res.IsError || !strings.Contains(res.Content[0].Text, "timed out") {
			t.Fatalf("want a timeout result, got IsError=%v %q", res.IsError, res.Content[0].Text)
		}
	})

	// A child with its output elsewhere lets Execute return either way, so
	// the test watches the child itself. It is the only writer of a FIFO,
	// and a reader of the FIFO sees EOF exactly when the child dies, so the
	// test needs no polling.
	t.Run("child detached from the output", func(t *testing.T) {
		fifo := filepath.Join(t.TempDir(), "child")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		childGone := make(chan error, 1)
		go func() {
			// Blocks until the child opens the write end.
			f, err := os.Open(fifo)
			if err != nil {
				childGone <- err
				return
			}
			_, err = io.Copy(io.Discard, f)
			_ = f.Close()
			childGone <- err
		}()

		res := executeWithin(t, Input{Command: "sleep 30 > '" + fifo + "' 2>&1 & wait", Timeout: 300}, 10*time.Second)
		if !res.IsError || !strings.Contains(res.Content[0].Text, "timed out") {
			t.Fatalf("want a timeout result, got IsError=%v %q", res.IsError, res.Content[0].Text)
		}
		// Under load the kill can land before the child opens the FIFO, which
		// would leave the reader waiting for a writer forever. Opening and
		// closing a write end ourselves unblocks it; EOF then still waits for
		// every other writer, i.e. for a child that did open it to die.
		// Non-blocking: ENXIO means no reader is left, i.e. it already saw EOF.
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		} else if !errors.Is(err, syscall.ENXIO) {
			t.Fatalf("open FIFO for write: %v", err)
		}
		select {
		case err := <-childGone:
			if err != nil {
				t.Fatalf("reading the child's FIFO: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the backgrounded sleep outlived the timeout: its process group was not killed")
		}
	})
}

// executeWithin runs the Bash tool and fails the test if it has not returned
// within limit.
func executeWithin(t *testing.T, in Input, limit time.Duration) tool.Result {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		res tool.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := New(nil).Execute(context.Background(), raw)
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatal(o.err)
		}
		return o.res
	case <-time.After(limit):
		t.Fatalf("Execute(%q) still running after %s", in.Command, limit)
		return tool.Result{}
	}
}
