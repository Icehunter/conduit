package ccrtool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/ccr"
)

// newTestStore creates a CCR store backed by a temp dir.
func newTestStore(t *testing.T) *ccr.Store {
	t.Helper()
	return ccr.NewStore(t.TempDir())
}

func TestCCRRetrieveValidHandle(t *testing.T) {
	s := newTestStore(t)
	content := "first line\nsecond line\nthird line"
	handle, err := s.Put(content)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"handle": handle})
	result, err := executeWithStore(context.Background(), s, raw)
	if err != nil {
		t.Fatalf("Execute: unexpected go error: %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute: tool error: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "first line") {
		t.Fatalf("result missing expected content; got: %q", result.Content[0].Text)
	}
}

func TestCCRRetrieveInvalidHandle(t *testing.T) {
	s := newTestStore(t)
	raw, _ := json.Marshal(map[string]any{"handle": "notahandle"})
	result, err := executeWithStore(context.Background(), s, raw)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid handle, got success")
	}
}

func TestCCRRetrievePattern(t *testing.T) {
	s := newTestStore(t)
	content := "apple\nbanana\napricot\ncherry"
	handle, err := s.Put(content)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"handle": handle, "pattern": "ap"})
	result, err := executeWithStore(context.Background(), s, raw)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", result.Content[0].Text)
	}
	got := result.Content[0].Text
	if !strings.Contains(got, "apple") || !strings.Contains(got, "apricot") {
		t.Fatalf("pattern filter missed expected lines; got: %q", got)
	}
	if strings.Contains(got, "banana") || strings.Contains(got, "cherry") {
		t.Fatalf("pattern filter included unexpected lines; got: %q", got)
	}
}

func TestCCRRetrieveSlice(t *testing.T) {
	s := newTestStore(t)
	content := "L0\nL1\nL2\nL3\nL4"
	handle, err := s.Put(content)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"handle": handle, "offset": 1, "limit": 2})
	result, err := executeWithStore(context.Background(), s, raw)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", result.Content[0].Text)
	}
	got := result.Content[0].Text
	if !strings.Contains(got, "L1") || !strings.Contains(got, "L2") {
		t.Fatalf("slice missing expected lines; got: %q", got)
	}
	if strings.Contains(got, "L0") || strings.Contains(got, "L3") {
		t.Fatalf("slice contains unexpected lines; got: %q", got)
	}
}
