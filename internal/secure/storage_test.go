package secure

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMemoryStorage_RoundTrip(t *testing.T) {
	s := NewMemoryStorage()

	if _, err := s.Get("svc", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on missing: err = %v; want ErrNotFound", err)
	}

	if err := s.Set("svc", "k", []byte("super-secret")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get("svc", "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("super-secret")) {
		t.Errorf("Get = %q", got)
	}

	if err := s.Delete("svc", "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("svc", "k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: err = %v; want ErrNotFound", err)
	}
}

// TestMemoryStorage_Isolation: distinct services don't see each other's keys.
func TestMemoryStorage_Isolation(t *testing.T) {
	s := NewMemoryStorage()
	_ = s.Set("svc-a", "k", []byte("A"))
	_ = s.Set("svc-b", "k", []byte("B"))

	if v, _ := s.Get("svc-a", "k"); !bytes.Equal(v, []byte("A")) {
		t.Errorf("svc-a/k = %q; want A", v)
	}
	if v, _ := s.Get("svc-b", "k"); !bytes.Equal(v, []byte("B")) {
		t.Errorf("svc-b/k = %q; want B", v)
	}
}

// TestMemoryStorage_ReturnsCopy: the returned slice cannot be mutated to
// affect the stored value. Critical so callers can't accidentally clobber
// secrets via aliasing.
func TestMemoryStorage_ReturnsCopy(t *testing.T) {
	s := NewMemoryStorage()
	_ = s.Set("svc", "k", []byte("hello"))
	v, _ := s.Get("svc", "k")
	v[0] = 'X'
	again, _ := s.Get("svc", "k")
	if !bytes.Equal(again, []byte("hello")) {
		t.Errorf("storage mutated through returned slice: got %q", again)
	}
}

// TestMemoryStorage_StringRedacted: stringifying must not leak contents.
func TestMemoryStorage_StringRedacted(t *testing.T) {
	s := NewMemoryStorage()
	_ = s.Set("svc", "k", []byte("plaintext-secret"))
	str := fmt.Sprintf("%v", s)
	if str == "" {
		t.Fatal("empty string")
	}
	if strings.Contains(str, "plaintext-secret") {
		t.Errorf("stringification leaked secret: %s", str)
	}
}
