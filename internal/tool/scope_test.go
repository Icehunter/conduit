package tool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestScope(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"root has none", context.Background(), ""},
		{"empty id is no scope", WithScope(context.Background(), ""), ""},
		{"sub-agent id", WithScope(context.Background(), "sub-1"), "sub-1"},
		{"nested overrides", WithScope(WithScope(context.Background(), "a"), "b"), "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScopeFromContext(tt.ctx); got != tt.want {
				t.Errorf("ScopeFromContext = %q, want %q", got, tt.want)
			}
		})
	}
	if got := ScopeFromContext(nil); got != "" { //nolint:staticcheck // nil ctx is the case under test
		t.Errorf("nil ctx scope = %q", got)
	}
}

type releaserTool struct {
	name     string
	released []string
}

func (r *releaserTool) Name() string                           { return r.name }
func (r *releaserTool) Description() string                    { return "" }
func (r *releaserTool) InputSchema() json.RawMessage           { return json.RawMessage(`{}`) }
func (r *releaserTool) IsReadOnly(json.RawMessage) bool        { return true }
func (r *releaserTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (r *releaserTool) Execute(context.Context, json.RawMessage) (Result, error) {
	return TextResult(""), nil
}
func (r *releaserTool) ReleaseScope(id string) { r.released = append(r.released, id) }

func TestRegistryReleaseScope(t *testing.T) {
	base := NewRegistry()
	rel := &releaserTool{name: "R"}
	base.Register(rel)
	base.Register(&fakePlain{name: "plain"})

	tests := []struct {
		name string
		reg  *Registry
		id   string
		want []string
	}{
		{"base releases", base, "sub-1", []string{"sub-1"}},
		{"view reaches the base tool", base.WithOverrides(&fakePlain{name: "other"}), "sub-2", []string{"sub-2"}},
		{"empty id is ignored", base, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel.released = nil
			tt.reg.ReleaseScope(tt.id)
			if len(rel.released) != len(tt.want) || (len(tt.want) == 1 && rel.released[0] != tt.want[0]) {
				t.Errorf("released = %v, want %v", rel.released, tt.want)
			}
		})
	}
}

type fakePlain struct{ name string }

func (f *fakePlain) Name() string                           { return f.name }
func (f *fakePlain) Description() string                    { return "" }
func (f *fakePlain) InputSchema() json.RawMessage           { return json.RawMessage(`{}`) }
func (f *fakePlain) IsReadOnly(json.RawMessage) bool        { return true }
func (f *fakePlain) IsConcurrencySafe(json.RawMessage) bool { return true }
func (f *fakePlain) Execute(context.Context, json.RawMessage) (Result, error) {
	return TextResult(""), nil
}
