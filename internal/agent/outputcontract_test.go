package agent

import (
	"encoding/json"
	"testing"
)

const personSchema = `{
  "type": "object",
  "required": ["name", "age"],
  "properties": {"name": {"type": "string"}, "age": {"type": "integer"}},
  "additionalProperties": false
}`

func TestCompileOutputSchema(t *testing.T) {
	if sch, err := compileOutputSchema(nil); err != nil || sch != nil {
		t.Errorf("empty schema: got (%v, %v), want (nil, nil)", sch, err)
	}
	if _, err := compileOutputSchema(json.RawMessage(`{"type":"object"}`)); err != nil {
		t.Errorf("valid schema rejected: %v", err)
	}
	// A malformed schema must fail before a sub-agent is ever spawned.
	if _, err := compileOutputSchema(json.RawMessage(`{"type":`)); err == nil {
		t.Error("malformed JSON accepted as a schema")
	}
	if _, err := compileOutputSchema(json.RawMessage(`{"type": 12}`)); err == nil {
		t.Error("structurally invalid schema accepted")
	}
}

func TestExtractOutputJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"bare object", `{"a":1}`, `{"a":1}`, true},
		{"bare array", `[1,2]`, `[1,2]`, true},
		{"surrounding whitespace", "\n  {\"a\":1}\n ", `{"a":1}`, true},
		{"fenced with tag", "```json\n{\"a\":1}\n```", `{"a":1}`, true},
		{"fenced without tag", "```\n{\"a\":1}\n```", `{"a":1}`, true},
		{"prose before and after", `Here you go: {"a":1} — hope that helps!`, `{"a":1}`, true},
		{"prose plus fence", "Sure:\n```json\n{\"a\":1}\n```\nDone.", `{"a":1}`, true},
		{"nested braces", `text {"a":{"b":2}} tail`, `{"a":{"b":2}}`, true},
		{"no json at all", `I could not do it.`, ``, false},
		{"empty", ``, ``, false},
		{"unbalanced", `{"a":1`, ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractOutputJSON(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tt.ok, got)
			}
			if ok && string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateOutput(t *testing.T) {
	sch, err := compileOutputSchema(json.RawMessage(personSchema))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := validateOutput(sch, json.RawMessage(`{"name":"ada","age":36}`)); err != nil {
		t.Errorf("valid instance rejected: %v", err)
	}
	for _, bad := range []string{
		`{"name":"ada"}`,                // missing required
		`{"name":"ada","age":"36"}`,     // wrong type
		`{"name":"ada","age":36,"x":1}`, // additionalProperties false
		`[]`,                            // wrong root type
	} {
		if err := validateOutput(sch, json.RawMessage(bad)); err == nil {
			t.Errorf("invalid instance accepted: %s", bad)
		}
	}
	// A nil schema means no contract, so anything passes.
	if err := validateOutput(nil, json.RawMessage(`whatever`)); err != nil {
		t.Errorf("nil schema should not validate: %v", err)
	}
}

func TestOutputContractPromptMentionsTheSchema(t *testing.T) {
	p := outputContractPrompt(json.RawMessage(personSchema))
	for _, want := range []string{"Output contract", "\"name\"", "\"age\"", "single JSON value"} {
		if !contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
