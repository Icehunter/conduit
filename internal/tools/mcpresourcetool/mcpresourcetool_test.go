package mcpresourcetool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestListMcpResources_NoManager(t *testing.T) {
	tool := &ListMcpResources{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
}

func TestReadMcpResource_NoManager(t *testing.T) {
	tool := &ReadMcpResource{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"server_name":"test","uri":"test://x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error when no manager")
	}
}

func TestReadMcpResource_MissingInput(t *testing.T) {
	tool := &ReadMcpResource{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for missing server_name/uri")
	}
}

func TestListMcpResources_Metadata(t *testing.T) {
	tool := &ListMcpResources{}
	if tool.Name() != "ListMcpResources" {
		t.Errorf("Name = %q", tool.Name())
	}
	if !tool.IsReadOnly(nil) {
		t.Error("should be read-only")
	}
	if !tool.IsConcurrencySafe(nil) {
		t.Error("should be concurrency safe")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid JSON: %v", err)
	}
}

func TestReadMcpResource_Metadata(t *testing.T) {
	tool := &ReadMcpResource{}
	if tool.Name() != "ReadMcpResource" {
		t.Errorf("Name = %q", tool.Name())
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid JSON: %v", err)
	}
}
