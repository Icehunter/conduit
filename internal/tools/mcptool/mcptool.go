// Package mcptool provides a tool.Tool implementation that proxies calls to
// an MCP server tool through the conduit MCP manager.
package mcptool

import (
	"context"
	"encoding/json"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/tool"
)

// MCPTool wraps a single MCP server tool as a tool.Tool.
type MCPTool struct {
	manager       *mcp.Manager
	qualifiedName string // e.g. "github____list_issues"
	def           mcp.ToolDef
}

// New creates a MCPTool for one entry from Manager.AllTools().
func New(manager *mcp.Manager, nt mcp.NamedTool) *MCPTool {
	return &MCPTool{
		manager:       manager,
		qualifiedName: nt.QualifiedName,
		def:           nt.Def,
	}
}

func (t *MCPTool) Name() string        { return t.qualifiedName }
func (t *MCPTool) Description() string { return t.def.Description }

func (t *MCPTool) InputSchema() json.RawMessage {
	if t.def.InputSchema != nil {
		return t.def.InputSchema
	}
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *MCPTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *MCPTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *MCPTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	result, err := t.manager.CallTool(ctx, t.qualifiedName, input)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}

	// Flatten MCP ContentBlocks into one text block.
	text := ""
	for _, block := range result.Content {
		if block.Type == "text" {
			if text != "" {
				text += "\n"
			}
			text += block.Text
		}
	}

	if result.IsError {
		return tool.ErrorResult(text), nil
	}
	return tool.TextResult(text), nil
}

// RegisterAll adds every tool from all connected MCP servers to reg.
func RegisterAll(reg *tool.Registry, manager *mcp.Manager) {
	for _, nt := range manager.AllTools() {
		reg.Register(New(manager, nt))
	}
}
