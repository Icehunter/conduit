// Package mcptool provides a tool.Tool implementation that proxies calls to
// an MCP server tool through the conduit MCP manager.
package mcptool

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/truncate"
)

// MCPTool wraps a single MCP server tool as a tool.Tool.
type MCPTool struct {
	manager       *mcp.Manager
	qualifiedName string // e.g. "github____list_issues"
	def           mcp.ToolDef
	alwaysOn      bool // when true: non-deferrable, description is truncated to first sentence
}

// New creates a MCPTool for one entry from Manager.AllTools().
func New(manager *mcp.Manager, nt mcp.NamedTool) *MCPTool {
	return &MCPTool{
		manager:       manager,
		qualifiedName: nt.QualifiedName,
		def:           nt.Def,
	}
}

// NewAlwaysOn creates a MCPTool that is never deferred. Its description is
// truncated to the first sentence in the schema sent to the model; the full
// description remains accessible via ToolSearch (which calls Description()
// directly on the registered tool).
func NewAlwaysOn(manager *mcp.Manager, nt mcp.NamedTool) *MCPTool {
	t := New(manager, nt)
	t.alwaysOn = true
	return t
}

func (t *MCPTool) Name() string { return t.qualifiedName }

// Description returns the full tool description. ToolSearch uses this so the
// model can read the complete description when it selects the tool. For the
// API schema, alwaysOn tools use a truncated first-sentence version (see
// buildAPIDescription).
func (t *MCPTool) Description() string { return t.def.Description }

// buildAPIDescription returns the description to include in the API schema.
// For always-on tools it returns only the first sentence; for deferred tools
// the full description is included (the schema is only sent when requested).
func (t *MCPTool) buildAPIDescription() string {
	if !t.alwaysOn {
		return t.def.Description
	}
	// Truncate to the first sentence (period, question mark, or exclamation).
	desc := t.def.Description
	for i, r := range desc {
		if r == '.' || r == '?' || r == '!' {
			return strings.TrimSpace(desc[:i+1])
		}
	}
	// No sentence terminator — fall back to the first line.
	if idx := strings.IndexByte(desc, '\n'); idx > 0 {
		return strings.TrimSpace(desc[:idx])
	}
	return desc
}

func (t *MCPTool) InputSchema() json.RawMessage {
	if t.def.InputSchema != nil {
		return t.def.InputSchema
	}
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *MCPTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *MCPTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

// Deferrable implements tool.DeferrableChecker. MCP tools are deferrable by
// default so their schemas are excluded from every API request when ToolSearch
// is registered. Always-on tools return false and are always included.
func (t *MCPTool) Deferrable() bool { return !t.alwaysOn }

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

	// Apply truncate-to-disk for large MCP tool outputs.
	maxLines, maxBytes := truncate.Limits()
	tr, _ := truncate.Apply(text, truncate.Options{
		MaxLines: maxLines,
		MaxBytes: maxBytes,
	})
	text = tr.Content

	if result.IsError {
		return tool.ErrorResult(text), nil
	}
	return tool.TextResult(text), nil
}

// RegisterAll adds every tool from all connected MCP servers to reg.
// All registered tools are deferrable by default (see MCPTool.Deferrable).
func RegisterAll(reg *tool.Registry, manager *mcp.Manager) {
	for _, nt := range manager.AllTools() {
		reg.Register(New(manager, nt))
	}
}

// APIDescription implements the apiDescriber interface checked by buildToolDefs.
// For always-on tools it returns the first-sentence truncation; for deferrable
// tools it returns the full description (sent only when the model requests it
// via ToolSearch select:).
func (t *MCPTool) APIDescription() string { return t.buildAPIDescription() }
