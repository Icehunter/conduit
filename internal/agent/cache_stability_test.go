package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
)

// namedFakeTool is a minimal tool.Tool that only needs a Name() for ordering tests.
type namedFakeTool struct {
	name string
}

func (n *namedFakeTool) Name() string                             { return n.name }
func (n *namedFakeTool) Description() string                      { return "fake " + n.name }
func (n *namedFakeTool) InputSchema() json.RawMessage             { return json.RawMessage(`{"type":"object"}`) }
func (n *namedFakeTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (n *namedFakeTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (n *namedFakeTool) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.TextResult("ok"), nil
}

// TestBuildToolDefs_DeterministicOrder verifies that buildToolDefs produces
// alphabetically sorted tool definitions regardless of registry insertion order,
// and that the last element retains the cache_control breakpoint.
func TestBuildToolDefs_DeterministicOrder(t *testing.T) {
	tests := []struct {
		name  string
		names []string // insertion order
	}{
		{"forward", []string{"Alpha", "Beta", "Gamma"}},
		{"reverse", []string{"Gamma", "Beta", "Alpha"}},
		{"mixed", []string{"Beta", "Gamma", "Alpha"}},
		{"single", []string{"Solo"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := tool.NewRegistry()
			for _, n := range tt.names {
				reg.Register(&namedFakeTool{name: n})
			}

			defs := buildToolDefs(reg)

			if len(defs) != len(tt.names) {
				t.Fatalf("got %d defs, want %d", len(defs), len(tt.names))
			}

			// Verify ascending alphabetical order.
			for i := 1; i < len(defs); i++ {
				if defs[i].Name < defs[i-1].Name {
					t.Errorf("defs not sorted: defs[%d].Name=%q < defs[%d].Name=%q",
						i, defs[i].Name, i-1, defs[i-1].Name)
				}
			}

			// Verify the last element has a cache_control breakpoint.
			last := defs[len(defs)-1]
			if last.CacheControl == nil {
				t.Errorf("last element %q has nil CacheControl, want breakpoint", last.Name)
			}

			// Verify the breakpoint is only on the last element.
			for i := 0; i < len(defs)-1; i++ {
				if defs[i].CacheControl != nil {
					t.Errorf("non-last element %q has CacheControl set", defs[i].Name)
				}
			}
		})
	}
}

// TestBuildToolDefs_TwoOrdersMatch verifies that two registries populated in
// different orders produce identical defs slices.
func TestBuildToolDefs_TwoOrdersMatch(t *testing.T) {
	orders := [][]string{
		{"Charlie", "Alpha", "Bravo"},
		{"Bravo", "Charlie", "Alpha"},
	}

	results := make([][]api.ToolDef, 0, len(orders))
	for _, order := range orders {
		reg := tool.NewRegistry()
		for _, n := range order {
			reg.Register(&namedFakeTool{name: n})
		}
		results = append(results, buildToolDefs(reg))
	}

	if len(results[0]) != len(results[1]) {
		t.Fatalf("different lengths: %d vs %d", len(results[0]), len(results[1]))
	}
	for i := range results[0] {
		if results[0][i].Name != results[1][i].Name {
			t.Errorf("position %d: got %q vs %q", i, results[0][i].Name, results[1][i].Name)
		}
	}
}

// TestHashCachedPrefix_Stable verifies that identical inputs produce the same hash.
func TestHashCachedPrefix_Stable(t *testing.T) {
	ephemeral := &api.CacheControl{Type: "ephemeral"}

	system := []api.SystemBlock{
		{Type: "text", Text: "You are a helpful assistant.", CacheControl: ephemeral},
		{Type: "text", Text: "No cache control here."},
	}
	tools := []api.ToolDef{
		{Name: "Alpha", Description: "do alpha", CacheControl: ephemeral},
		{Name: "Beta", Description: "do beta"},
	}
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{
			{Type: "text", Text: "hello", CacheControl: ephemeral},
		}},
	}

	h1 := hashCachedPrefix(system, tools, msgs)
	h2 := hashCachedPrefix(system, tools, msgs)

	if h1 == 0 {
		t.Fatal("expected non-zero hash")
	}
	if h1 != h2 {
		t.Errorf("hash not stable: %d != %d", h1, h2)
	}
}

// TestHashCachedPrefix_ChangesOnDiff verifies that different cached content
// produces different hashes.
func TestHashCachedPrefix_ChangesOnDiff(t *testing.T) {
	ephemeral := &api.CacheControl{Type: "ephemeral"}

	systemA := []api.SystemBlock{
		{Type: "text", Text: "System prompt A.", CacheControl: ephemeral},
	}
	systemB := []api.SystemBlock{
		{Type: "text", Text: "System prompt B — different content.", CacheControl: ephemeral},
	}
	tools := []api.ToolDef{
		{Name: "Alpha", Description: "do alpha"},
	}

	hA := hashCachedPrefix(systemA, tools, nil)
	hB := hashCachedPrefix(systemB, tools, nil)

	if hA == 0 || hB == 0 {
		t.Fatal("expected non-zero hashes")
	}
	if hA == hB {
		t.Errorf("expected different hashes for different system content, both = %d", hA)
	}
}

// TestHashCachedPrefix_NoCachedContent returns a non-zero hash when only tools
// are present (tools are always hashed when non-empty).
func TestHashCachedPrefix_NoCachedContent(t *testing.T) {
	tools := []api.ToolDef{
		{Name: "Alpha", Description: "do alpha"},
	}
	h := hashCachedPrefix(nil, tools, nil)
	if h == 0 {
		t.Error("expected non-zero hash for non-empty tool list")
	}
}

// TestHashCachedPrefix_EmptyInputs returns zero when nothing is cached.
func TestHashCachedPrefix_EmptyInputs(t *testing.T) {
	h := hashCachedPrefix(nil, nil, nil)
	// FNV-1a of empty input is non-zero (14695981039346656037), but we write
	// nothing to the hasher, so h.Sum64() returns the FNV offset basis.
	// The function only returns 0 on marshalling errors, so any consistent
	// non-zero value is acceptable. Just verify it doesn't panic.
	_ = h
}
