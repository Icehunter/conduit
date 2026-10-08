package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type inner struct {
	A string `json:"a"`
}

type embedded struct {
	FromEmbedded int `json:"fromEmbedded"`
}

type node struct {
	Name     string  `json:"name"`
	Children []*node `json:"children,omitempty"`
}

type walkerSample struct {
	embedded
	Plain     string `json:"plain"`
	NoTag     bool
	Skipped   string                                `json:"-"`
	hidden    string                                //nolint:unused // exercises unexported-field skipping
	Omit      int                                   `json:"omit,omitempty"`
	Ptr       *inner                                `json:"ptr,omitempty"`
	Slice     []string                              `json:"slice,omitempty"`
	Map       map[string]float64                    `json:"map,omitempty"`
	AnyMap    map[string]any                        `json:"anyMap,omitempty"`
	Raw       json.RawMessage                       `json:"raw,omitempty"`
	Iface     any                                   `json:"iface,omitempty"`
	When      time.Time                             `json:"when,omitempty"`
	Nested    map[string]map[string]json.RawMessage `json:"nested,omitempty"`
	Tree      node                                  `json:"tree,omitempty"`
	NullSlice []string                              `json:"nullSlice"`
	Unsigned  uint16                                `json:"unsigned,omitempty"`
}

type customMarshaler struct{}

func (customMarshaler) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }

type withCustom struct {
	C customMarshaler `json:"c"`
}

func gen(t *testing.T, root reflect.Type, ann Annotations) map[string]any {
	t.Helper()
	b, err := Generate(root, ann, Meta{ID: "https://example.test/s.json", Title: "T"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	return doc
}

func TestWalker(t *testing.T) {
	doc := gen(t, reflect.TypeFor[walkerSample](), Annotations{
		"walkerSample.Plain": {Description: "plain field", Enum: Strings("x", "y"), Default: "x"},
	})
	props := doc["properties"].(map[string]any)
	defs := doc["$defs"].(map[string]any)

	tests := []struct {
		name string
		prop string
		want string // JSON of the expected property schema
	}{
		{"annotated string", "plain", `{"default":"x","description":"plain field","enum":["x","y"],"type":"string"}`},
		{"untagged uses Go name", "NoTag", `{"type":"boolean"}`},
		{"embedded flattened", "fromEmbedded", `{"type":"integer"}`},
		{"omitempty int", "omit", `{"type":"integer"}`},
		{"pointer is plain ref", "ptr", `{"$ref":"#/$defs/inner"}`},
		{"slice", "slice", `{"items":{"type":"string"},"type":"array"}`},
		{"map", "map", `{"additionalProperties":{"type":"number"},"type":"object"}`},
		{"any map is open object", "anyMap", `{"type":"object"}`},
		{"raw message", "raw", `{}`},
		{"interface", "iface", `{}`},
		{"time", "when", `{"format":"date-time","type":"string"}`},
		{"nested raw maps", "nested", `{"additionalProperties":{"type":"object"},"type":"object"}`},
		{"recursive ref", "tree", `{"$ref":"#/$defs/node"}`},
		{"nullable without omitempty", "nullSlice", `{"items":{"type":"string"},"type":["array","null"]}`},
		{"unsigned", "unsigned", `{"minimum":0,"type":"integer"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := props[tt.prop]
			if !ok {
				t.Fatalf("property %q missing; have %v", tt.prop, keys(props))
			}
			gb, _ := json.Marshal(got)
			if string(gb) != tt.want {
				t.Errorf("%s = %s, want %s", tt.prop, gb, tt.want)
			}
		})
	}

	for _, absent := range []string{"Skipped", "hidden", "-", "embedded", "Plain"} {
		if _, ok := props[absent]; ok {
			t.Errorf("property %q should not be present", absent)
		}
	}

	// Recursion: node's children items reference node itself.
	nodeDef := defs["node"].(map[string]any)["properties"].(map[string]any)
	cb, _ := json.Marshal(nodeDef["children"])
	if want := `{"items":{"$ref":"#/$defs/node"},"type":"array"}`; string(cb) != want {
		t.Errorf("children = %s, want %s", cb, want)
	}

	if doc["additionalProperties"] != true {
		t.Error("root must keep additionalProperties true")
	}
	if _, ok := props["$schema"]; !ok {
		t.Error("root must allow a $schema property")
	}
}

func TestGenerateDeterministicAndFormatted(t *testing.T) {
	a, err := Generate(reflect.TypeFor[walkerSample](), nil, Meta{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate(reflect.TypeFor[walkerSample](), nil, Meta{})
	if string(a) != string(b) {
		t.Error("output differs between runs")
	}
	if !strings.HasSuffix(string(a), "}\n") || !strings.Contains(string(a), "\n  \"") {
		t.Errorf("want 2-space indent and trailing newline, got %q...", string(a[:40]))
	}
}

func TestCustomMarshalerNeedsOverride(t *testing.T) {
	if _, err := Generate(reflect.TypeFor[withCustom](), nil, Meta{}); err == nil {
		t.Fatal("expected error for type with custom marshaler and no override")
	}
	doc := gen(t, reflect.TypeFor[withCustom](), Annotations{
		"customMarshaler": {Schema: map[string]any{"type": "string"}, Description: "custom"},
	})
	got, _ := json.Marshal(doc["properties"].(map[string]any)["c"])
	if want := `{"description":"custom","type":"string"}`; string(got) != want {
		t.Errorf("c = %s, want %s", got, want)
	}
}

func TestRootMustBeStruct(t *testing.T) {
	if _, err := Generate(reflect.TypeFor[string](), nil, Meta{}); err == nil {
		t.Fatal("expected error for non-struct root")
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

type DepthA struct {
	Shared string `json:"shared"`
	OnlyA  string `json:"onlyA"`
}
type DepthB struct {
	Shared int `json:"shared"`
}
type PlainName struct{ Name string }
type TaggedName struct {
	Other int `json:"Name"`
}

// embed builds a struct embedding each of the given struct types, plus extra
// fields. reflect.StructOf keeps go vet's duplicate-tag check out of the way
// of the deliberate conflicts.
func embed(extra []reflect.StructField, embedded ...reflect.Type) reflect.Type {
	var fs []reflect.StructField
	for _, e := range embedded {
		fs = append(fs, reflect.StructField{Name: e.Name(), Type: e, Anonymous: true})
	}
	return reflect.StructOf(append(fs, extra...))
}

type quotedRoot struct {
	N int      `json:"n,string"`
	B *bool    `json:"b,omitempty,string"`
	S []string `json:"s,string"`
}

func TestFieldConflictRules(t *testing.T) {
	root := embed([]reflect.StructField{
		{Name: "Top", Type: reflect.TypeFor[int](), Tag: `json:"onlyA"`},
	}, reflect.TypeFor[DepthA](), reflect.TypeFor[DepthB]())
	props := gen(t, root, nil)["properties"].(map[string]any)
	if _, ok := props["shared"]; ok {
		t.Error("ambiguous same-depth conflict must be dropped")
	}
	if got, _ := json.Marshal(props["onlyA"]); string(got) != `{"type":"integer"}` {
		t.Errorf("shallowest field must win, onlyA = %s", got)
	}
}

func TestTaggedBeatsUntaggedAtSameDepth(t *testing.T) {
	root := embed(nil, reflect.TypeFor[PlainName](), reflect.TypeFor[TaggedName]())
	props := gen(t, root, nil)["properties"].(map[string]any)
	if got, _ := json.Marshal(props["Name"]); string(got) != `{"type":"integer"}` {
		t.Errorf("tagged field should win, got %s", got)
	}
}

func TestStringOption(t *testing.T) {
	doc := gen(t, reflect.TypeFor[struct {
		N int   `json:"n,string"`
		B *bool `json:"b,omitempty,string"`
	}](), nil)
	props := doc["properties"].(map[string]any)
	for _, k := range []string{"n", "b"} {
		if got, _ := json.Marshal(props[k]); string(got) != `{"type":"string"}` {
			t.Errorf("%s = %s, want string", k, got)
		}
	}
	if _, err := Generate(reflect.TypeFor[quotedRoot](), nil, Meta{}); err == nil {
		t.Error("expected error for ,string on a slice")
	}
}

func TestAllowNullAddsNullToEnum(t *testing.T) {
	type root struct {
		P *string `json:"p"`
	}
	doc := gen(t, reflect.TypeFor[root](), Annotations{"root.P": {Enum: Strings("a", "b")}})
	got, _ := json.Marshal(doc["properties"].(map[string]any)["p"])
	if want := `{"enum":["a","b",null],"type":["string","null"]}`; string(got) != want {
		t.Errorf("p = %s, want %s", got, want)
	}
}

func TestCaseInsensitive(t *testing.T) {
	n := CaseInsensitive("", "on", "auto")
	if n["pattern"] != "^(|[oO][nN]|[aA][uU][tT][oO])$" {
		t.Errorf("pattern = %v", n["pattern"])
	}
}
