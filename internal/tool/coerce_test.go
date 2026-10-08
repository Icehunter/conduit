package tool

import (
	"encoding/json"
	"reflect"
	"testing"
)

const coerceSchema = `{
	"type": "object",
	"properties": {
		"count":   {"type": "integer"},
		"ratio":   {"type": "number"},
		"flag":    {"type": "boolean"},
		"name":    {"type": "string"},
		"tags":    {"type": "array", "items": {"type": "string"}},
		"ids":     {"type": "array", "items": {"type": "integer"}},
		"opts":    {"type": "object", "properties": {"depth": {"type": "integer"}, "on": {"type": "boolean"}}},
		"maybe":   {"type": ["integer", "null"]},
		"either":  {"anyOf": [{"type": "string"}, {"type": "integer"}]},
		"free":    {"description": "no type"},
		"items":   {"type": "array", "items": {"type": "object", "properties": {"n": {"type": "integer"}}}}
	}
}`

func TestCoerceInput(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		wantC bool
	}{
		{"integer string", `{"count":"42"}`, `{"count":42}`, true},
		{"integer string padded", `{"count":" 7 "}`, `{"count":7}`, true},
		{"integral float string", `{"count":"3.0"}`, `{"count":3}`, true},
		{"fractional string stays for integer", `{"count":"3.5"}`, `{"count":"3.5"}`, false},
		{"non numeric stays", `{"count":"many"}`, `{"count":"many"}`, false},
		{"empty string stays", `{"count":""}`, `{"count":""}`, false},
		{"inf stays", `{"ratio":"Inf"}`, `{"ratio":"Inf"}`, false},
		{"nan stays", `{"ratio":"NaN"}`, `{"ratio":"NaN"}`, false},
		{"integer rejects exponent", `{"count":"1e3"}`, `{"count":"1e3"}`, false},
		{"integer rejects hex float", `{"count":"0x1p4"}`, `{"count":"0x1p4"}`, false},
		{"integer rejects hex", `{"count":"0x10"}`, `{"count":"0x10"}`, false},
		{"integer rejects underscores", `{"count":"1_000"}`, `{"count":"1_000"}`, false},
		{"integer keeps a huge value as text", `{"count":"1234567890123456789"}`, `{"count":"1234567890123456789"}`, false},
		{"integer at the exact limit", `{"count":"9007199254740992"}`, `{"count":9007199254740992}`, true},
		{"integer one past the limit", `{"count":"9007199254740993"}`, `{"count":"9007199254740993"}`, false},
		{"negative integer", `{"count":"-12"}`, `{"count":-12}`, true},
		{"number accepts exponent", `{"ratio":"1e3"}`, `{"ratio":1000}`, true},
		{"number rejects hex float", `{"ratio":"0x1p4"}`, `{"ratio":"0x1p4"}`, false},
		{"number rejects underscores", `{"ratio":"1_0.5"}`, `{"ratio":"1_0.5"}`, false},
		{"number string", `{"ratio":"0.25"}`, `{"ratio":0.25}`, true},
		{"number from integer string", `{"ratio":"2"}`, `{"ratio":2}`, true},
		{"bool true", `{"flag":"true"}`, `{"flag":true}`, true},
		{"bool False mixed case", `{"flag":"False"}`, `{"flag":false}`, true},
		{"bool yes is not coerced", `{"flag":"yes"}`, `{"flag":"yes"}`, false},
		{"bool 1 is not coerced", `{"flag":"1"}`, `{"flag":"1"}`, false},
		{"string field keeps numeric text", `{"name":"42"}`, `{"name":"42"}`, false},
		{"string field keeps true", `{"name":"true"}`, `{"name":"true"}`, false},
		{"already typed values untouched", `{"count":5,"flag":true,"ratio":1.5}`, `{"count":5,"flag":true,"ratio":1.5}`, false},
		{"json array string", `{"tags":"[\"a\",\"b\"]"}`, `{"tags":["a","b"]}`, true},
		{"json array of ints in string", `{"ids":"[1, 2, 3]"}`, `{"ids":[1,2,3]}`, true},
		{"array string elements coerced", `{"ids":["1","2"]}`, `{"ids":[1,2]}`, true},
		{"bare string wrapped", `{"tags":"solo"}`, `{"tags":["solo"]}`, true},
		{"broken json array wrapped as one string", `{"tags":"[oops"}`, `{"tags":["[oops"]}`, true},
		{"bare number wrapped and coerced", `{"ids":"9"}`, `{"ids":[9]}`, true},
		{"null left alone for array", `{"tags":null}`, `{"tags":null}`, false},
		{"object string", `{"opts":"{\"depth\":\"2\",\"on\":\"true\"}"}`, `{"opts":{"depth":2,"on":true}}`, true},
		{"nested object field coerced", `{"opts":{"depth":"4"}}`, `{"opts":{"depth":4}}`, true},
		{"array of objects coerced", `{"items":[{"n":"1"},{"n":2}]}`, `{"items":[{"n":1},{"n":2}]}`, true},
		{"nullable null string", `{"maybe":"null"}`, `{"maybe":null}`, true},
		{"nullable int string", `{"maybe":"8"}`, `{"maybe":8}`, true},
		{"union keeps string", `{"either":"12"}`, `{"either":"12"}`, false},
		{"untyped property untouched", `{"free":"42"}`, `{"free":"42"}`, false},
		{"unknown property untouched", `{"zzz":"42"}`, `{"zzz":"42"}`, false},
		{"empty input", `{}`, `{}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in, want map[string]any
			if err := json.Unmarshal([]byte(tt.in), &in); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
				t.Fatal(err)
			}
			orig := mustClone(t, in)
			got, changed := CoerceInput(json.RawMessage(coerceSchema), in)
			if changed != tt.wantC {
				t.Errorf("changed = %v, want %v", changed, tt.wantC)
			}
			if !reflect.DeepEqual(got, want) {
				gj, _ := json.Marshal(got)
				t.Errorf("got %s, want %s", gj, tt.want)
			}
			if !reflect.DeepEqual(in, orig) {
				t.Errorf("input was mutated: %v -> %v", orig, in)
			}
		})
	}
}

func TestCoerceInput_BadSchema(t *testing.T) {
	in := map[string]any{"count": "42"}
	for _, schema := range []string{``, `not json`, `[]`, `{}`} {
		got, changed := CoerceInput(json.RawMessage(schema), in)
		if changed || !reflect.DeepEqual(got, in) {
			t.Errorf("schema %q: got %v changed=%v", schema, got, changed)
		}
	}
}

func mustClone(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(m)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// FuzzCoerceInput checks that arbitrary arguments never panic the coercer,
// never mutate their input, and that coercing twice changes nothing more.
func FuzzCoerceInput(f *testing.F) {
	for _, s := range []string{`{"count":"42"}`, `{"tags":"[1"}`, `{"opts":"{\"depth\":\"x\"}"}`, `{"ids":[["1"]]}`, `{"flag":"TRUE","items":"[{\"n\":\"1\"}]"}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var in map[string]any
		if json.Unmarshal([]byte(raw), &in) != nil {
			return
		}
		orig := mustClone(t, in)
		out, _ := CoerceInput(json.RawMessage(coerceSchema), in)
		if !reflect.DeepEqual(in, orig) {
			t.Fatalf("input mutated")
		}
		again, changed := CoerceInput(json.RawMessage(coerceSchema), out)
		if changed {
			t.Fatalf("second pass changed %v -> %v", out, again)
		}
	})
}
