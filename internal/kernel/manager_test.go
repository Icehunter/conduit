package kernel

import (
	"testing"
	"time"
)

func TestManager_GetSameKernelForSameSession(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	m := NewManager()
	defer m.DisposeSession("sess1")

	k1, err := m.Get("sess1", "python")
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	k2, err := m.Get("sess1", "python")
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	if k1 != k2 {
		t.Error("expected same Kernel pointer for same session+lang")
	}
}

func TestManager_GetDifferentKernelForDifferentSession(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	m := NewManager()
	defer m.DisposeSession("sessA")
	defer m.DisposeSession("sessB")

	kA, err := m.Get("sessA", "python")
	if err != nil {
		t.Fatalf("Get sessA: %v", err)
	}
	kB, err := m.Get("sessB", "python")
	if err != nil {
		t.Fatalf("Get sessB: %v", err)
	}
	if kA == kB {
		t.Error("expected different Kernel pointers for different sessions")
	}
}

func TestManager_Reap_ClosesIdle(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	m := NewManager()

	_, err := m.Get("sessReap", "python")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Inject a last-used time far in the past.
	m.mu.Lock()
	key := "sessReap:python"
	m.kernels[key].lastUsed = time.Now().Add(-(idleTimeout + time.Minute))
	m.mu.Unlock()

	m.Reap(time.Now())

	m.mu.Lock()
	_, stillPresent := m.kernels[key]
	m.mu.Unlock()
	if stillPresent {
		t.Error("expected idle kernel to be reaped")
	}
}

func TestManager_DisposeSession(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not available")
	}
	m := NewManager()

	_, err := m.Get("sessDispose", "python")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	m.DisposeSession("sessDispose")

	m.mu.Lock()
	_, stillPresent := m.kernels["sessDispose:python"]
	m.mu.Unlock()
	if stillPresent {
		t.Error("expected kernel to be removed after DisposeSession")
	}
}
