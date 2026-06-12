// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.

package rtk

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCrushJSON_ArrayOfObjects(t *testing.T) {
	// Build 10 objects; the JSON will be > 1024 bytes when objects have enough fields.
	arr := make([]map[string]any, 10)
	for i := range 10 {
		arr[i] = map[string]any{
			"id":          i + 1,
			"name":        fmt.Sprintf("item-number-%d-with-long-name", i),
			"status":      "active",
			"created_at":  "2024-01-01T00:00:00Z",
			"updated_at":  "2024-06-01T12:00:00Z",
			"description": fmt.Sprintf("This is item %d with a longer description to ensure we have enough bytes", i),
		}
	}
	b, _ := json.Marshal(arr)
	input := string(b)

	if len(input) < minCrushBytes {
		t.Skipf("test data too small (%d bytes); increase object size", len(input))
	}

	out, crushed := crushJSON(input)
	if !crushed {
		t.Errorf("expected crushed=true for 10-object array; got false\nout=%s", out)
	}
	if !strings.Contains(out, "[10 objects") {
		t.Errorf("expected '[10 objects' header; got: %s", out)
	}
	// Key names should be present (sorted).
	for _, key := range []string{"created_at", "description", "id", "name", "status", "updated_at"} {
		if !strings.Contains(out, key) {
			t.Errorf("expected key %q in output; got: %s", key, out)
		}
	}
	// Should include sample objects.
	if !strings.Contains(out, "sample[0..") {
		t.Errorf("expected sample header; got: %s", out)
	}
}

func TestCrushJSON_ArraySmall(t *testing.T) {
	// 2 objects — below the 4-element threshold.
	arr := []map[string]any{
		{"id": 1, "name": "a"},
		{"id": 2, "name": "b"},
	}
	b, _ := json.Marshal(arr)
	input := string(b)

	// Pad to be >= minCrushBytes.
	for len(input) < minCrushBytes {
		input += " "
	}

	_, crushed := crushJSON(input)
	if crushed {
		t.Error("expected crushed=false for 2-element array (below threshold)")
	}
}

func TestCrushJSON_NestedObject(t *testing.T) {
	// Object with a key containing 15 items and a long string.
	// Use enough content to exceed minCrushBytes (1024).
	items := make([]any, 15)
	for i := range 15 {
		items[i] = fmt.Sprintf("item-with-a-longer-name-%d", i)
	}
	longStr := strings.Repeat("x", 600) // well over 200 chars
	obj := map[string]any{
		"name":        "test",
		"items":       items,
		"content":     longStr,
		"description": strings.Repeat("description text ", 20),
		"count":       15,
	}
	b, _ := json.Marshal(obj)
	input := string(b)

	if len(input) < minCrushBytes {
		t.Skipf("test data too small (%d bytes); increase object size", len(input))
	}

	out, crushed := crushJSON(input)
	if !crushed {
		t.Errorf("expected crushed=true for object with large array; got false\nout=%s", out)
	}
	if !strings.Contains(out, "…(15 items)") {
		t.Errorf("expected '…(15 items)' for collapsed array; got: %s", out)
	}
	if !strings.Contains(out, "…(600 chars)") {
		t.Errorf("expected '…(600 chars)' for long string; got: %s", out)
	}
}

func TestCrushJSON_NonJSON(t *testing.T) {
	input := strings.Repeat("this is not json at all!!!", 50) // >= 1024 bytes
	if len(input) < minCrushBytes {
		t.Fatal("padding insufficient")
	}
	out, crushed := crushJSON(input)
	if crushed {
		t.Error("expected crushed=false for non-JSON input")
	}
	if out != input {
		t.Error("expected output to equal input for non-JSON")
	}
}

func TestCrushJSON_SmallJSON(t *testing.T) {
	// Valid JSON but below the 1024-byte threshold.
	input := `{"key": "value", "num": 42}`
	if len(input) >= minCrushBytes {
		t.Skip("input accidentally too large")
	}
	out, crushed := crushJSON(input)
	if crushed {
		t.Error("expected crushed=false for small JSON")
	}
	if out != input {
		t.Errorf("expected output=input for small JSON; got %q", out)
	}
}

func TestCrushJSON_MixedArray(t *testing.T) {
	// Top-level array where not all elements are objects — Case 1 must not fire.
	mixed := []any{
		map[string]any{"id": 1},
		"just a string",
		42,
		map[string]any{"id": 4},
	}
	b, _ := json.Marshal(mixed)
	input := string(b)
	// Pad to >= 1024 bytes.
	for len(input) < minCrushBytes {
		input += " "
	}

	_, crushed := crushJSON(input)
	if crushed {
		t.Error("expected crushed=false for mixed-type array")
	}
}

func FuzzCrushJSON(f *testing.F) {
	f.Add(`{"key": "value"}`)
	f.Add(`[{"id":1},{"id":2}]`)
	f.Add(`null`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, s string) {
		out, _ := crushJSON(s)
		if out == "" && s != "" {
			t.Error("crushJSON returned empty for non-empty input")
		}
	})
}
