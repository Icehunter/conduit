package websearchtool

import (
	"encoding/json"
	"testing"
)

func TestWebSearch_StaticMetadata(t *testing.T) {
	tt := New(nil) // nil client — only tests metadata
	if tt.Name() != "WebSearch" {
		t.Errorf("Name = %q", tt.Name())
	}
	if !tt.IsReadOnly(nil) {
		t.Error("IsReadOnly should be true")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe should be true")
	}
	var schema map[string]any
	if err := json.Unmarshal(tt.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["query"]; !ok {
		t.Error("schema missing 'query' property")
	}
}

func TestWebSearch_InputSchema_RequiresQuery(t *testing.T) {
	tt := New(nil)
	var schema map[string]any
	_ = json.Unmarshal(tt.InputSchema(), &schema)
	required, _ := schema["required"].([]any)
	found := false
	for _, r := range required {
		if r == "query" {
			found = true
		}
	}
	if !found {
		t.Error("'query' should be in required")
	}
}

func TestWebSearch_InvalidJSON(t *testing.T) {
	tt := New(nil)
	res, err := tt.Execute(nil, json.RawMessage(`{bad`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should IsError=true")
	}
}

func TestWebSearch_EmptyQuery(t *testing.T) {
	tt := New(nil)
	b, _ := json.Marshal(map[string]any{"query": "  "})
	res, err := tt.Execute(nil, b)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("empty query should IsError=true")
	}
}
