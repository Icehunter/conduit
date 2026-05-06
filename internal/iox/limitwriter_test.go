package iox

import (
	"bytes"
	"errors"
	"testing"
)

func TestLimitWriter_UnderLimit(t *testing.T) {
	var buf bytes.Buffer
	lw := &LimitWriter{W: &buf, Limit: 10}
	n, err := lw.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Errorf("Write() = %d, %v; want 5, nil", n, err)
	}
	if buf.String() != "hello" {
		t.Errorf("buf = %q; want %q", buf.String(), "hello")
	}
}

func TestLimitWriter_ExactLimit(t *testing.T) {
	var buf bytes.Buffer
	lw := &LimitWriter{W: &buf, Limit: 5}
	n, err := lw.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Errorf("Write() = %d, %v; want 5, nil", n, err)
	}
}

func TestLimitWriter_OverLimit_Partial(t *testing.T) {
	var buf bytes.Buffer
	lw := &LimitWriter{W: &buf, Limit: 3}
	n, err := lw.Write([]byte("hello"))
	if !errors.Is(err, ErrLimitReached) {
		t.Errorf("err = %v; want ErrLimitReached", err)
	}
	if n != 3 {
		t.Errorf("n = %d; want 3", n)
	}
	if buf.String() != "hel" {
		t.Errorf("buf = %q; want %q", buf.String(), "hel")
	}
}

func TestLimitWriter_AlreadyOverLimit(t *testing.T) {
	var buf bytes.Buffer
	lw := &LimitWriter{W: &buf, Limit: 3}
	_, _ = lw.Write([]byte("hello"))
	// Second write must also fail.
	n, err := lw.Write([]byte("world"))
	if !errors.Is(err, ErrLimitReached) || n != 0 {
		t.Errorf("second Write() = %d, %v; want 0, ErrLimitReached", n, err)
	}
}

func TestAtomicLimitWriter_UnderLimit(t *testing.T) {
	var buf bytes.Buffer
	alw := &AtomicLimitWriter{W: &buf, Limit: 10}
	n, err := alw.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Errorf("Write() = %d, %v; want 5, nil", n, err)
	}
}

func TestAtomicLimitWriter_Overflow_CallsOnOverflow(t *testing.T) {
	var buf bytes.Buffer
	called := 0
	alw := &AtomicLimitWriter{
		W:          &buf,
		Limit:      3,
		OnOverflow: func() { called++ },
	}
	_, _ = alw.Write([]byte("hello"))
	// OnOverflow must have been called exactly once.
	if called != 1 {
		t.Errorf("OnOverflow called %d times; want 1", called)
	}
	// Subsequent writes don't re-trigger OnOverflow.
	_, _ = alw.Write([]byte("world"))
	if called != 1 {
		t.Errorf("OnOverflow called %d times after second write; want 1", called)
	}
}

func TestAtomicLimitWriter_Overflow_FlagSet(t *testing.T) {
	var buf bytes.Buffer
	alw := &AtomicLimitWriter{W: &buf, Limit: 3}
	_, err := alw.Write([]byte("hello"))
	if !errors.Is(err, ErrLimitReached) {
		t.Errorf("err = %v; want ErrLimitReached", err)
	}
	if !alw.Overflow.Load() {
		t.Error("Overflow flag not set after limit exceeded")
	}
}
