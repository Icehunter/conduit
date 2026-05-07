package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeTool struct{ name string }

func (f fakeTool) Name() string                           { return f.name }
func (f fakeTool) Description() string                    { return "fake" }
func (f fakeTool) InputSchema() json.RawMessage           { return json.RawMessage(`{"type":"object"}`) }
func (f fakeTool) IsReadOnly(json.RawMessage) bool        { return true }
func (f fakeTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (f fakeTool) Execute(context.Context, json.RawMessage) (Result, error) {
	return TextResult("ok"), nil
}

func TestRegistry_RegisterLookup(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "a"})
	r.Register(fakeTool{name: "b"})
	got, ok := r.Lookup("a")
	if !ok {
		t.Fatal("Lookup a: not found")
	}
	if got.Name() != "a" {
		t.Errorf("Name = %q", got.Name())
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Error("missing tool should not be found")
	}
}

func TestRegistry_RegisterReplaces(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "a"})
	r.Register(fakeTool{name: "a"})
	if len(r.All()) != 1 {
		t.Errorf("All() = %d; want 1 (replace)", len(r.All()))
	}
}

func TestTextResult(t *testing.T) {
	res := TextResult("hi")
	if res.IsError {
		t.Error("IsError should be false")
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" || res.Content[0].Text != "hi" {
		t.Errorf("res = %+v", res)
	}
}

func TestErrorResult(t *testing.T) {
	res := ErrorResult("nope")
	if !res.IsError {
		t.Error("IsError should be true")
	}
	if len(res.Content) != 1 || res.Content[0].Text != "nope" {
		t.Errorf("res = %+v", res)
	}
}

func TestSubset_FiltersToNamedTools(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "FileReadTool"})
	r.Register(fakeTool{name: "BashTool"})
	r.Register(fakeTool{name: "GrepTool"})

	sub := r.Subset([]string{"FileReadTool", "GrepTool"})
	if _, ok := sub.Lookup("FileReadTool"); !ok {
		t.Error("FileReadTool should be in subset")
	}
	if _, ok := sub.Lookup("GrepTool"); !ok {
		t.Error("GrepTool should be in subset")
	}
	if _, ok := sub.Lookup("BashTool"); ok {
		t.Error("BashTool should NOT be in subset")
	}
}

func TestSubset_CaseInsensitive(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "FileReadTool"})

	sub := r.Subset([]string{"filereadtool"})
	if _, ok := sub.Lookup("FileReadTool"); !ok {
		t.Error("case-insensitive lookup should match FileReadTool")
	}
}

func TestSubset_EmptyNamesReturnsEmpty(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "a"})

	sub := r.Subset(nil)
	if len(sub.All()) != 0 {
		t.Errorf("empty Subset should return empty registry; got %d tools", len(sub.All()))
	}
}
