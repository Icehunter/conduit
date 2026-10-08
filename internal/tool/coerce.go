package tool

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// CoerceInput repairs model-emitted tool arguments against the tool's JSON
// Schema: "42" for an integer, "true" for a boolean, a JSON-encoded string for
// an array or object, and a bare value where an array is expected (wrapped in
// a one-element array).
//
// Coercion is conservative. A value is touched only when it fails the type the
// schema expects and the repair is unambiguous; everything else, including
// fields the schema does not describe, is returned as is. input is never
// mutated: when something changed, the returned map is a fresh copy and the
// bool is true.
func CoerceInput(schema json.RawMessage, input map[string]any) (map[string]any, bool) {
	if len(input) == 0 || len(schema) == 0 {
		return input, false
	}
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return input, false
	}
	out, changed := coerceValue(input, s)
	if !changed {
		return input, false
	}
	m, _ := out.(map[string]any)
	return m, true
}

// coerceValue returns v repaired against schema s and whether it changed.
func coerceValue(v any, s map[string]any) (any, bool) {
	if len(s) == 0 {
		return v, false
	}
	if !matchesSchemaType(v, s) {
		if repaired, ok := repairValue(v, s); ok {
			// A parsed container may itself hold strings that need repair.
			nested, _ := coerceValue(repaired, s)
			return nested, true
		}
		return v, false
	}
	switch tv := v.(type) {
	case []any:
		items, _ := s["items"].(map[string]any)
		if items == nil {
			return v, false
		}
		var out []any
		for i, el := range tv {
			c, ok := coerceValue(el, items)
			if ok && out == nil {
				out = append([]any(nil), tv...)
			}
			if ok {
				out[i] = c
			}
		}
		if out == nil {
			return v, false
		}
		return out, true
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if props == nil {
			return v, false
		}
		var out map[string]any
		for k, el := range tv {
			ps, _ := props[k].(map[string]any)
			if ps == nil {
				continue
			}
			c, ok := coerceValue(el, ps)
			if !ok {
				continue
			}
			if out == nil {
				out = make(map[string]any, len(tv))
				for k2, v2 := range tv {
					out[k2] = v2
				}
			}
			out[k] = c
		}
		if out == nil {
			return v, false
		}
		return out, true
	}
	return v, false
}

// repairValue attempts a single-step repair of v, which does not satisfy s.
func repairValue(v any, s map[string]any) (any, bool) {
	str, isStr := v.(string)
	if isStr {
		t := strings.TrimSpace(str)
		if schemaAccepts(s, "null") && strings.EqualFold(t, "null") {
			return nil, true
		}
		if schemaAccepts(s, "integer") {
			if f, ok := parseIntegerString(t); ok {
				return f, true
			}
		}
		if schemaAccepts(s, "number") {
			if f, ok := parseNumberString(t); ok {
				return f, true
			}
		}
		if schemaAccepts(s, "boolean") {
			switch strings.ToLower(t) {
			case "true":
				return true, true
			case "false":
				return false, true
			}
		}
		if schemaAccepts(s, "array") && strings.HasPrefix(t, "[") {
			var arr []any
			if json.Unmarshal([]byte(t), &arr) == nil {
				return arr, true
			}
		}
		if schemaAccepts(s, "object") && strings.HasPrefix(t, "{") {
			var obj map[string]any
			if json.Unmarshal([]byte(t), &obj) == nil {
				return obj, true
			}
		}
	}
	// A bare value where an array is expected. nil is left alone: the tool's
	// own default handling decides between "omit" and "empty list".
	if v != nil && schemaAccepts(s, "array") {
		if _, isArr := v.([]any); !isArr {
			return []any{v}, true
		}
	}
	return v, false
}

// matchesSchemaType reports whether v already has a type s permits. A schema
// that names no type (and no union branches) permits anything.
func matchesSchemaType(v any, s map[string]any) bool {
	if !schemaDeclaresType(s) {
		return true
	}
	switch tv := v.(type) {
	case nil:
		return schemaAccepts(s, "null") || nullable(s)
	case bool:
		return schemaAccepts(s, "boolean")
	case string:
		return schemaAccepts(s, "string")
	case float64:
		return schemaAccepts(s, "number") || (schemaAccepts(s, "integer") && tv == math.Trunc(tv))
	case int, int64:
		return schemaAccepts(s, "number") || schemaAccepts(s, "integer")
	case []any:
		return schemaAccepts(s, "array")
	case map[string]any:
		return schemaAccepts(s, "object")
	}
	return true
}

func nullable(s map[string]any) bool {
	n, _ := s["nullable"].(bool)
	return n
}

// schemaDeclaresType reports whether s (or a union branch) constrains type.
func schemaDeclaresType(s map[string]any) bool {
	if _, ok := s["type"]; ok {
		return true
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if branches, ok := s[key].([]any); ok && len(branches) > 0 {
			return true
		}
	}
	return false
}

// schemaAccepts reports whether s permits JSON type kind through "type" or
// any anyOf/oneOf/allOf branch.
func schemaAccepts(s map[string]any, kind string) bool {
	switch t := s["type"].(type) {
	case string:
		if t == kind {
			return true
		}
	case []any:
		for _, e := range t {
			if e == kind {
				return true
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		branches, _ := s[key].([]any)
		for _, b := range branches {
			if bm, ok := b.(map[string]any); ok && schemaAccepts(bm, kind) {
				return true
			}
		}
	}
	return false
}

// maxExactInt is the largest magnitude a float64 holds exactly. A decoded JSON
// number past it would silently change value, so such strings stay strings.
const maxExactInt = 1 << 53

var (
	intStringRe    = regexp.MustCompile(`^[+-]?[0-9]+(?:\.0+)?$`)
	numberStringRe = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
)

// parseIntegerString reads a plain decimal integer ("42", "-7", "3.0"). Hex,
// exponent and underscore forms, and values beyond float64's exact range, are
// not integers here.
func parseIntegerString(t string) (float64, bool) {
	if !intStringRe.MatchString(t) {
		return 0, false
	}
	whole, _, _ := strings.Cut(t, ".")
	v, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || v > maxExactInt || v < -maxExactInt {
		return 0, false
	}
	return float64(v), true
}

// parseNumberString reads a decimal number, with an optional exponent. Hex
// floats, "Inf", "NaN" and underscores are rejected.
func parseNumberString(t string) (float64, bool) {
	if !numberStringRe.MatchString(t) {
		return 0, false
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
