package kernel

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func pythonAvailable() bool {
	if _, err := exec.LookPath("python3"); err == nil {
		return true
	}
	_, err := exec.LookPath("python")
	return err == nil
}

func nodeAvailable() bool {
	_, err := exec.LookPath("node")
	return err == nil
}

func TestKernel_PythonStateRetention(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	k, err := New("python")
	if err != nil {
		t.Fatalf("New(python): %v", err)
	}
	defer k.Close()

	ctx := context.Background()

	// Call 1: set a variable.
	_, err = k.Execute(ctx, "x = 42")
	if err != nil {
		t.Fatalf("Execute set x: %v", err)
	}

	// Call 2: read the variable.
	out, err := k.Execute(ctx, "print(x)")
	if err != nil {
		t.Fatalf("Execute print x: %v", err)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("expected '42' in output, got %q", out)
	}
}

func TestKernel_NodeStateRetention(t *testing.T) {
	if !nodeAvailable() {
		t.Skip("node not available")
	}
	k, err := New("node")
	if err != nil {
		t.Fatalf("New(node): %v", err)
	}
	defer k.Close()

	ctx := context.Background()

	// Call 1: set a variable.
	_, err = k.Execute(ctx, "var stateVar = 'hello';")
	if err != nil {
		t.Fatalf("Execute set stateVar: %v", err)
	}

	// Call 2: read the variable.
	out, err := k.Execute(ctx, "console.log(stateVar);")
	if err != nil {
		t.Fatalf("Execute print stateVar: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected 'hello' in output, got %q", out)
	}
}

func TestKernel_TimeoutDoesNotKillKernel(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	k, err := New("python")
	if err != nil {
		t.Fatalf("New(python): %v", err)
	}
	defer k.Close()

	// Timeout call: sleep longer than the deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, execErr := k.Execute(ctx, "import time; time.sleep(10)")
	if execErr == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(execErr.Error(), "timed out") {
		t.Errorf("expected 'timed out' in error, got %v", execErr)
	}

	// Kernel should still be usable after timeout.
	// Allow a generous deadline so the dirty-recovery path has time to run.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	out, err := k.Execute(ctx2, "print('alive')")
	if err != nil {
		t.Fatalf("Execute after timeout: %v", err)
	}
	if !strings.Contains(out, "alive") {
		t.Errorf("expected 'alive' in output after timeout recovery, got %q", out)
	}
}

func TestKernel_RespawnAfterCrash(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	k, err := New("python")
	if err != nil {
		t.Fatalf("New(python): %v", err)
	}
	defer k.Close()

	ctx := context.Background()

	// Crash the interpreter by calling sys.exit().
	_, crashErr := k.Execute(ctx, "import sys; sys.exit(0)")
	// We expect an error because the process died mid-read.
	// (The process may exit before or after writing the sentinel depending on
	// timing, so we accept both an error and an empty success here.)
	_ = crashErr

	// Next call must succeed (fresh process after respawn).
	out, err := k.Execute(ctx, "print('respawned')")
	if err != nil {
		t.Fatalf("Execute after crash: %v", err)
	}
	if !strings.Contains(out, "respawned") {
		t.Errorf("expected 'respawned' in output, got %q", out)
	}
}
