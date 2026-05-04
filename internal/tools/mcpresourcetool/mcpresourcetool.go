// Package mcpresourcetool implements ListMcpResources and ReadMcpResource tools.
//
// These tools expose MCP server resources (non-tool data like files, URLs,
// database queries) to the agent. Port of src/tools/ListMcpResourcesTool/ and
// src/tools/ReadMcpResourceTool/.
package mcpresourcetool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/tool"
)

// ListMcpResources lists available resources from all connected MCP servers.
type ListMcpResources struct {
	Manager *mcp.Manager
}

func (t *ListMcpResources) Name() string        { return "ListMcpResources" }
func (t *ListMcpResources) Description() string { return listDescription }
func (t *ListMcpResources) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"server_name": {
			"type": "string",
			"description": "Optional: filter by MCP server name."
		}
	},
	"additionalProperties": false
}`)
}
func (t *ListMcpResources) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *ListMcpResources) IsConcurrencySafe(_ json.RawMessage) bool { return true }

type listInput struct {
	ServerName string `json:"server_name,omitempty"`
}

func (t *ListMcpResources) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if t.Manager == nil {
		return tool.TextResult("No MCP manager available."), nil
	}
	var inp listInput
	_ = json.Unmarshal(raw, &inp)

	servers := t.Manager.Servers()
	if len(servers) == 0 {
		return tool.TextResult("No MCP servers connected."), nil
	}

	var sb strings.Builder
	found := 0
	for _, srv := range servers {
		if inp.ServerName != "" && srv.Name != inp.ServerName {
			continue
		}
		if srv.Status != mcp.StatusConnected {
			continue
		}
		resources, err := t.Manager.ListResources(ctx, srv.Name)
		if err != nil {
			fmt.Fprintf(&sb, "[%s] error: %v\n", srv.Name, err)
			continue
		}
		for _, r := range resources {
			fmt.Fprintf(&sb, "[%s] %s", srv.Name, r.URI)
			if r.Name != "" {
				sb.WriteString(" — " + r.Name)
			}
			if r.Description != "" {
				sb.WriteString(": " + r.Description)
			}
			sb.WriteByte('\n')
			found++
		}
	}
	if found == 0 {
		return tool.TextResult("No resources found."), nil
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// ReadMcpResource reads the contents of one MCP resource by URI.
type ReadMcpResource struct {
	Manager *mcp.Manager
}

func (t *ReadMcpResource) Name() string        { return "ReadMcpResource" }
func (t *ReadMcpResource) Description() string { return readDescription }
func (t *ReadMcpResource) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"server_name": {
			"type": "string",
			"description": "The MCP server name hosting the resource."
		},
		"uri": {
			"type": "string",
			"description": "The resource URI (from ListMcpResources)."
		}
	},
	"required": ["server_name", "uri"],
	"additionalProperties": false
}`)
}
func (t *ReadMcpResource) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *ReadMcpResource) IsConcurrencySafe(_ json.RawMessage) bool { return true }

type readInput struct {
	ServerName string `json:"server_name"`
	URI        string `json:"uri"`
}

func (t *ReadMcpResource) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if t.Manager == nil {
		return tool.ErrorResult("No MCP manager available."), nil
	}
	var inp readInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return tool.ErrorResult("invalid input: " + err.Error()), nil
	}
	if inp.ServerName == "" || inp.URI == "" {
		return tool.ErrorResult("server_name and uri are required"), nil
	}

	contents, err := t.Manager.ReadResource(ctx, inp.ServerName, inp.URI)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("read resource: %v", err)), nil
	}
	if len(contents) == 0 {
		return tool.TextResult("Resource is empty."), nil
	}

	var sb strings.Builder
	for _, c := range contents {
		if c.Text != "" {
			sb.WriteString(c.Text)
		} else if c.Blob != "" {
			fmt.Fprintf(&sb, "[binary data, base64, %d bytes]", len(c.Blob)*3/4)
		}
	}
	return tool.TextResult(sb.String()), nil
}

const listDescription = `List available resources from connected MCP servers. Resources are non-tool data items (files, URLs, database queries) that the model can read. Use ReadMcpResource to access the content.`

const readDescription = `Read the contents of one MCP resource by URI. Use ListMcpResources first to discover available URIs.`
