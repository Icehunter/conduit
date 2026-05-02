// Package toolsearchtool implements the ToolSearch tool.
// Mirrors src/tools/ToolSearchTool/. Allows the model to discover tools in
// the registry by name or keyword when the full tool list is deferred.
package toolsearchtool

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/icehunter/conduit/internal/tool"
)

// Tool implements ToolSearch.
type Tool struct {
	registry *tool.Registry
}

func New(reg *tool.Registry) *Tool { return &Tool{registry: reg} }
func (*Tool) Name() string          { return "ToolSearch" }
func (*Tool) Description() string {
	return `Search the tool registry by name or keyword.

Use "select:<name>" to load a specific tool by exact name.
Use keywords to find tools by description or name.

Returns matching tool names and their descriptions.`
}
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query":       {"type": "string", "description": "Search query or \"select:<name>\""},
			"max_results": {"type": "number", "description": "Maximum results (default 5)"}
		},
		"required": ["query"]
	}`)
}
func (*Tool) IsReadOnly(json.RawMessage) bool       { return true }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *Tool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult("invalid input"), nil
	}
	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}

	tools := t.registry.All()
	query := strings.TrimSpace(in.Query)
	var matches []map[string]string

	// "select:<name>" — direct lookup.
	if strings.HasPrefix(query, "select:") {
		names := strings.Split(strings.TrimPrefix(query, "select:"), ",")
		for _, name := range names {
			name = strings.TrimSpace(name)
			for _, tl := range tools {
				if strings.EqualFold(tl.Name(), name) {
					matches = append(matches, map[string]string{
						"name":        tl.Name(),
						"description": tl.Description(),
					})
					break
				}
			}
		}
	} else {
		// Keyword search — match name or first sentence of description.
		queryLower := strings.ToLower(query)
		for _, tl := range tools {
			desc := tl.Description()
			// Use just the first line for matching.
			if idx := strings.IndexByte(desc, '\n'); idx > 0 {
				desc = desc[:idx]
			}
			if strings.Contains(strings.ToLower(tl.Name()), queryLower) ||
				strings.Contains(strings.ToLower(desc), queryLower) {
				matches = append(matches, map[string]string{
					"name":        tl.Name(),
					"description": desc,
				})
				if len(matches) >= maxResults {
					break
				}
			}
		}
	}

	out, _ := json.Marshal(map[string]any{
		"matches":              matches,
		"query":                in.Query,
		"total_deferred_tools": 0,
	})
	return tool.TextResult(string(out)), nil
}
