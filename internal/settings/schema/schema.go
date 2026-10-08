// Package schema generates JSON Schema (draft 2020-12) documents for conduit's
// config files from the Go types that load them. It is a small reflection
// walker rather than a general-purpose generator: it covers the shapes
// encoding/json handles in those types and fails loudly on anything it cannot
// describe, so a new custom marshaler cannot silently produce a wrong schema.
package schema

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Dialect is the JSON Schema draft the generated documents declare.
const Dialect = "https://json-schema.org/draft/2020-12/schema"

// Annotation decorates one schema node. Schema, when set, replaces the
// generated node entirely (Description/Enum/Default are then layered on top).
type Annotation struct {
	Description string
	Enum        []any
	Default     any
	Schema      map[string]any
}

// Annotations maps a key to an Annotation. A "Type.Field" key (Go type and Go
// field names) decorates a struct field; a bare "Type" key decorates every use
// of a named type and is how types with custom (un)marshalers are described.
type Annotations map[string]Annotation

// Meta is the document-level metadata of a root schema.
type Meta struct {
	ID          string
	Title       string
	Description string
}

var (
	rawMessageType    = reflect.TypeFor[json.RawMessage]()
	timeType          = reflect.TypeFor[time.Time]()
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	jsonUnmarshalType = reflect.TypeFor[json.Unmarshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

type generator struct {
	ann   Annotations
	defs  map[string]map[string]any
	owner map[string]reflect.Type // def name -> type, to detect name collisions
	names map[reflect.Type]string
}

// Generate renders the schema for root as indented JSON with a trailing
// newline. Output is deterministic: encoding/json sorts map keys.
func Generate(root reflect.Type, ann Annotations, meta Meta) ([]byte, error) {
	g := &generator{
		ann:   ann,
		defs:  map[string]map[string]any{},
		owner: map[string]reflect.Type{},
		names: map[reflect.Type]string{},
	}
	for root.Kind() == reflect.Pointer {
		root = root.Elem()
	}
	if root.Kind() != reflect.Struct {
		return nil, fmt.Errorf("schema: root %s is not a struct", root)
	}
	doc, err := g.structBody(root)
	if err != nil {
		return nil, err
	}
	doc["$schema"] = Dialect
	if meta.ID != "" {
		doc["$id"] = meta.ID
	}
	if meta.Title != "" {
		doc["title"] = meta.Title
	}
	if meta.Description != "" {
		doc["description"] = meta.Description
	}
	if _, ok := doc["properties"]; !ok {
		doc["properties"] = map[string]any{}
	}
	props := doc["properties"].(map[string]any)
	if _, ok := props["$schema"]; !ok {
		props["$schema"] = map[string]any{
			"type":        "string",
			"description": "URL of the JSON Schema this file follows (editor hint; ignored at load).",
		}
	}
	// Keys the config does not model (Claude Code settings, future fields)
	// must not be flagged.
	doc["additionalProperties"] = true
	if len(g.defs) > 0 {
		doc["$defs"] = g.defs
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("schema: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// node returns the schema for t. Named structs become $defs entries.
func (g *generator) node(t reflect.Type) (map[string]any, error) {
	if a, ok := g.ann[typeKey(t)]; ok && a.Schema != nil {
		return layer(cloneMap(a.Schema), a), nil
	}
	switch {
	case t == rawMessageType:
		return map[string]any{}, nil
	case t == timeType:
		return map[string]any{"type": "string", "format": "date-time"}, nil
	}
	if t.Kind() == reflect.Pointer {
		return g.node(t.Elem())
	}
	if t.Kind() != reflect.Interface && implementsCustom(t) {
		return nil, fmt.Errorf("schema: type %s has a custom JSON/Text (un)marshaler; add a %q annotation with a Schema override", t, typeKey(t))
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer", "minimum": 0}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Interface:
		return map[string]any{}, nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Kind() == reflect.Slice {
			return map[string]any{"type": "string", "contentEncoding": "base64"}, nil
		}
		items, err := g.node(t.Elem())
		if err != nil {
			return nil, err
		}
		out := map[string]any{"type": "array"}
		if len(items) > 0 {
			out["items"] = items
		}
		return out, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("schema: map key of %s is not a string", t)
		}
		val, err := g.node(t.Elem())
		if err != nil {
			return nil, err
		}
		out := map[string]any{"type": "object"}
		if len(val) > 0 {
			out["additionalProperties"] = val
		}
		return out, nil
	case reflect.Struct:
		return g.structRef(t)
	}
	return nil, fmt.Errorf("schema: unsupported kind %s for %s", t.Kind(), t)
}

func (g *generator) structRef(t reflect.Type) (map[string]any, error) {
	if t.Name() == "" {
		return g.structBody(t)
	}
	name, seen := g.names[t]
	if !seen {
		name = t.Name()
		if prev, clash := g.owner[name]; clash && prev != t {
			name = pkgBase(t) + "." + name
		}
		g.names[t] = name
		g.owner[name] = t
		g.defs[name] = nil // reserve before walking so recursion terminates
		body, err := g.structBody(t)
		if err != nil {
			return nil, err
		}
		g.defs[name] = body
	}
	return map[string]any{"$ref": "#/$defs/" + name}, nil
}

type field struct {
	name   string
	owner  reflect.Type
	sf     reflect.StructField
	depth  int
	tagged bool
	quoted bool // json:",string"
}

// quotedNode handles the ",string" option: scalars are carried as JSON strings.
func quotedNode(t reflect.Type) (map[string]any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return map[string]any{"type": "string"}, nil
	}
	return nil, fmt.Errorf("schema: json \",string\" option on unsupported type %s", t)
}

func (g *generator) structBody(t reflect.Type) (map[string]any, error) {
	fields := resolveFields(t)

	props := map[string]any{}
	for _, f := range fields {
		var n map[string]any
		var err error
		if f.quoted {
			n, err = quotedNode(f.sf.Type)
		} else {
			n, err = g.node(f.sf.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", f.owner.Name(), f.sf.Name, err)
		}
		if a, ok := g.ann[f.owner.Name()+"."+f.sf.Name]; ok {
			if a.Schema != nil {
				n = cloneMap(a.Schema)
			}
			n = layer(n, a)
		}
		if nullable(f) {
			n = allowNull(n)
		}
		props[f.name] = n
	}
	out := map[string]any{"type": "object"}
	if a, ok := g.ann[typeKey(t)]; ok && a.Description != "" {
		out["description"] = a.Description
	}
	if len(props) > 0 {
		out["properties"] = props
	}
	return out, nil
}

// nullable reports whether encoding/json can write null for f: a nil slice,
// map or pointer in a field without omitempty (conduit's own saves do this).
func nullable(f field) bool {
	switch f.sf.Type.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer:
	default:
		return false
	}
	_, opts, _ := strings.Cut(f.sf.Tag.Get("json"), ",")
	return !slices.Contains(strings.Split(opts, ","), "omitempty")
}

// CaseInsensitive returns a string schema that accepts any casing of vals
// (and the empty string when "" is listed). JSON Schema regexes have no
// inline case flag, so each letter becomes a character class. The canonical
// spellings are kept as examples for editor completion.
func CaseInsensitive(vals ...string) map[string]any {
	alts := make([]string, len(vals))
	for i, v := range vals {
		var b strings.Builder
		for _, r := range v {
			lo, up := strings.ToLower(string(r)), strings.ToUpper(string(r))
			if lo != up {
				fmt.Fprintf(&b, "[%s%s]", lo, up)
			} else {
				b.WriteRune(r)
			}
		}
		alts[i] = b.String()
	}
	ex := make([]any, 0, len(vals))
	for _, v := range vals {
		if v != "" {
			ex = append(ex, v)
		}
	}
	return map[string]any{"type": "string", "pattern": "^(" + strings.Join(alts, "|") + ")$", "examples": ex}
}

func allowNull(n map[string]any) map[string]any {
	if t, ok := n["type"].(string); ok {
		n["type"] = []any{t, "null"}
		if enum, ok := n["enum"].([]any); ok && !slices.Contains(enum, nil) {
			n["enum"] = append(slices.Clone(enum), nil)
		}
		return n
	}
	out := map[string]any{"anyOf": []any{n, map[string]any{"type": "null"}}}
	if d, ok := n["description"]; ok {
		out["description"] = d
		delete(n, "description")
	}
	return out
}

// resolveFields returns t's JSON-visible fields after applying encoding/json's
// conflict rules: the shallowest field of a name wins, a tagged field beats
// untagged ones at the same depth, and an unresolved conflict drops the name.
func resolveFields(t reflect.Type) []field {
	var all []field
	collect(t, 0, map[reflect.Type]bool{t: true}, &all)
	byName := map[string][]field{}
	var order []string
	for _, f := range all {
		if _, ok := byName[f.name]; !ok {
			order = append(order, f.name)
		}
		byName[f.name] = append(byName[f.name], f)
	}
	var out []field
	for _, name := range order {
		cands := byName[name]
		minDepth := cands[0].depth
		for _, c := range cands {
			minDepth = min(minDepth, c.depth)
		}
		var top, tagged []field
		for _, c := range cands {
			if c.depth != minDepth {
				continue
			}
			top = append(top, c)
			if c.tagged {
				tagged = append(tagged, c)
			}
		}
		switch {
		case len(top) == 1:
			out = append(out, top[0])
		case len(tagged) == 1:
			out = append(out, tagged[0])
		}
	}
	return out
}

// collect gathers the JSON-visible fields of t, flattening embedded structs.
func collect(t reflect.Type, depth int, visiting map[reflect.Type]bool, out *[]field) {
	for i := range t.NumField() {
		sf := t.Field(i)
		tag, hasTag := sf.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if sf.Anonymous && name == "" {
			ft := sf.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				if !visiting[ft] {
					visiting[ft] = true
					collect(ft, depth+1, visiting, out)
					delete(visiting, ft)
				}
				continue
			}
		}
		if !sf.IsExported() {
			continue
		}
		tagged := hasTag && name != ""
		if !tagged {
			name = sf.Name
		}
		*out = append(*out, field{
			name: name, owner: t, sf: sf, depth: depth, tagged: tagged,
			quoted: slices.Contains(strings.Split(opts, ","), "string"),
		})
	}
}

func layer(n map[string]any, a Annotation) map[string]any {
	if a.Description != "" {
		n["description"] = a.Description
	}
	if len(a.Enum) > 0 {
		n["enum"] = a.Enum
	}
	if a.Default != nil {
		n["default"] = a.Default
	}
	return n
}

func implementsCustom(t reflect.Type) bool {
	for _, iface := range []reflect.Type{jsonMarshalerType, jsonUnmarshalType, textMarshalerType} {
		if t.Implements(iface) || reflect.PointerTo(t).Implements(iface) {
			return true
		}
	}
	return false
}

func typeKey(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

func pkgBase(t reflect.Type) string {
	p := t.PkgPath()
	return p[strings.LastIndex(p, "/")+1:]
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Strings converts a string slice to the []any form Annotation.Enum takes.
func Strings(vals ...string) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}
