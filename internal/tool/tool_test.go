package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
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

// ownedTool is a fakeTool a Reconcile caller claims; fakeTool entries stand in
// for tools registered by someone else.
type ownedTool struct{ fakeTool }

func isOwned(t Tool) bool {
	_, ok := t.(ownedTool)
	return ok
}

func owned(name string) Tool { return ownedTool{fakeTool{name: name}} }

func TestRegistry_Unregister(t *testing.T) {
	tests := []struct {
		name   string
		remove string
		want   bool
		left   []string
	}{
		{"registered tool", "a", true, []string{"b"}},
		{"missing tool", "zzz", false, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			r.Register(fakeTool{name: "a"})
			r.Register(fakeTool{name: "b"})
			if got := r.Unregister(tt.remove); got != tt.want {
				t.Errorf("Unregister(%q) = %v, want %v", tt.remove, got, tt.want)
			}
			if got := sortedNames(r); !equalNames(got, tt.left) {
				t.Errorf("tools = %v, want %v", got, tt.left)
			}
		})
	}
}

func TestRegistry_Reconcile(t *testing.T) {
	tests := []struct {
		name  string
		start []Tool
		want  []Tool
		names []string
		owner map[string]bool // name -> registered entry must be an owned tool
	}{
		{
			name:  "adds new owned tools",
			start: []Tool{fakeTool{name: "Bash"}},
			want:  []Tool{owned("mcp__a__x"), owned("mcp__a__y")},
			names: []string{"Bash", "mcp__a__x", "mcp__a__y"},
		},
		{
			name:  "removes owned tools no longer wanted",
			start: []Tool{fakeTool{name: "Bash"}, owned("mcp__a__x"), owned("mcp__b__z")},
			want:  []Tool{owned("mcp__a__x")},
			names: []string{"Bash", "mcp__a__x"},
		},
		{
			name:  "empty want clears every owned tool",
			start: []Tool{fakeTool{name: "Bash"}, owned("mcp__a__x")},
			want:  nil,
			names: []string{"Bash"},
		},
		{
			name:  "never displaces a tool it does not own",
			start: []Tool{fakeTool{name: "Bash"}},
			want:  []Tool{owned("Bash")},
			names: []string{"Bash"},
			owner: map[string]bool{"Bash": false},
		},
		{
			name:  "replaces an owned tool of the same name",
			start: []Tool{fakeTool{name: "Bash"}, owned("mcp__a__x")},
			want:  []Tool{owned("mcp__a__x")},
			names: []string{"Bash", "mcp__a__x"},
			owner: map[string]bool{"mcp__a__x": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			for _, tl := range tt.start {
				r.Register(tl)
			}
			r.Reconcile(isOwned, tt.want)
			if got := sortedNames(r); !equalNames(got, tt.names) {
				t.Errorf("tools = %v, want %v", got, tt.names)
			}
			for name, wantOwned := range tt.owner {
				got, ok := r.Lookup(name)
				if !ok {
					t.Fatalf("%s missing", name)
				}
				if isOwned(got) != wantOwned {
					t.Errorf("%s owned = %v, want %v", name, isOwned(got), wantOwned)
				}
			}
		})
	}
}

// Tools are registered and removed at runtime (MCP servers connect in the
// background) while the agent loop and ToolSearch read the registry. Without
// the lock this is a fatal concurrent map write; under -race it also catches
// unguarded reads.
func TestRegistry_ConcurrentMutationAndReads(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "Bash"})
	const workers = 8
	const rounds = 200
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range rounds {
				name := fmt.Sprintf("mcp__s%d__t%d", w, i%5)
				switch i % 4 {
				case 0:
					r.Register(owned(name))
				case 1:
					r.Unregister(name)
				case 2:
					r.Reconcile(isOwned, []Tool{owned(name)})
				default:
					r.Register(fakeTool{name: name + "_plain"})
				}
			}
		})
		wg.Go(func() {
			for range rounds {
				_, _ = r.Lookup("Bash")
				_ = r.All()
				_ = r.WithOverrides(fakeTool{name: "Read"})
				_ = r.Subset([]string{"Bash"})
			}
		})
	}
	wg.Wait()
	if _, ok := r.Lookup("Bash"); !ok {
		t.Fatal("Bash was dropped by concurrent MCP churn")
	}
}

func sortedNames(r *Registry) []string {
	all := r.All()
	names := make([]string, 0, len(all))
	for _, t := range all {
		names = append(names, t.Name())
	}
	sort.Strings(names)
	return names
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// boundTool stands in for ToolSearch: it reads the registry it is bound to.
type boundTool struct {
	fakeTool
	reg *Registry
}

func (b boundTool) BindRegistry(r *Registry) Tool { return boundTool{fakeTool: b.fakeTool, reg: r} }

// Sub-agents and teammates get registries made by WithOverrides and Subset.
// They used to be copies taken at spawn, so an MCP server that connected
// afterwards never reached a long-lived teammate, while the ToolSearch it
// inherited searched the parent's live registry and offered tools the child
// could not call.
func TestRegistryViews(t *testing.T) {
	newBase := func() *Registry {
		r := NewRegistry()
		r.Register(fakeTool{name: "Bash"})
		r.Register(fakeTool{name: "Read"})
		r.Register(boundTool{fakeTool: fakeTool{name: "ToolSearch"}})
		return r
	}
	tests := []struct {
		name     string
		view     func(base *Registry) *Registry
		later    func(base, view *Registry) // changes made after the view exists
		want     []string
		wantBase []string
		bound    bool // the view holds a ToolSearch bound to itself
	}{
		{
			name:     "overrides view sees tools the base gains later",
			view:     func(b *Registry) *Registry { return b.WithOverrides(fakeTool{name: "EnterPlanMode"}) },
			later:    func(b, _ *Registry) { b.Register(fakeTool{name: "mcp__docs__search"}) },
			want:     []string{"Bash", "EnterPlanMode", "Read", "ToolSearch", "mcp__docs__search"},
			wantBase: []string{"Bash", "Read", "ToolSearch", "mcp__docs__search"},
			bound:    true,
		},
		{
			name:     "overrides view loses tools the base drops",
			view:     func(b *Registry) *Registry { return b.WithOverrides() },
			later:    func(b, _ *Registry) { b.Unregister("Read") },
			want:     []string{"Bash", "ToolSearch"},
			wantBase: []string{"Bash", "ToolSearch"},
			bound:    true,
		},
		{
			name: "subset view shows only allowlisted tools, including late ones",
			view: func(b *Registry) *Registry { return b.Subset([]string{"bash", "ToolSearch", "mcp__docs__search"}) },
			later: func(b, _ *Registry) {
				b.Register(fakeTool{name: "mcp__docs__search"})
				b.Register(fakeTool{name: "mcp__docs__delete"})
			},
			want:     []string{"Bash", "ToolSearch", "mcp__docs__search"},
			wantBase: []string{"Bash", "Read", "ToolSearch", "mcp__docs__delete", "mcp__docs__search"},
			bound:    true,
		},
		{
			name:     "subset without ToolSearch has none",
			view:     func(b *Registry) *Registry { return b.Subset([]string{"Read"}) },
			want:     []string{"Read"},
			wantBase: []string{"Bash", "Read", "ToolSearch"},
		},
		{
			name: "chained views keep the allowlist and add extras",
			view: func(b *Registry) *Registry {
				return b.Subset([]string{"Bash", "ToolSearch"}).WithOverrides(fakeTool{name: "SkillManage"})
			},
			later:    func(b, _ *Registry) { b.Register(fakeTool{name: "mcp__docs__search"}) },
			want:     []string{"Bash", "SkillManage", "ToolSearch"},
			wantBase: []string{"Bash", "Read", "ToolSearch", "mcp__docs__search"},
			bound:    true,
		},
		{
			name: "a view's own changes stay out of the base",
			view: func(b *Registry) *Registry { return b.WithOverrides(fakeTool{name: "Read"}) },
			later: func(_, v *Registry) {
				v.Register(fakeTool{name: "ViewOnly"})
				v.Unregister("Read") // drops the override; the base's Read shows through
			},
			want:     []string{"Bash", "Read", "ToolSearch", "ViewOnly"},
			wantBase: []string{"Bash", "Read", "ToolSearch"},
			bound:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := newBase()
			v := tt.view(base)
			if tt.later != nil {
				tt.later(base, v)
			}
			if got := sortedNames(v); !equalNames(got, tt.want) {
				t.Errorf("view tools = %v, want %v", got, tt.want)
			}
			for _, name := range tt.want {
				if _, ok := v.Lookup(name); !ok {
					t.Errorf("Lookup(%q) failed on a tool All lists", name)
				}
			}
			if got := sortedNames(base); !equalNames(got, tt.wantBase) {
				t.Errorf("base tools = %v, want %v", got, tt.wantBase)
			}
			ts, ok := v.Lookup("ToolSearch")
			if ok != tt.bound {
				t.Fatalf("view has ToolSearch = %v, want %v", ok, tt.bound)
			}
			if ok {
				if b, isBound := ts.(boundTool); !isBound || b.reg != v {
					t.Errorf("the view's ToolSearch reads %p, want the view %p", b.reg, v)
				}
			}
		})
	}
}
