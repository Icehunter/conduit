// Package mcpauthtool provides a per-server pseudo-tool that triggers the
// OAuth flow for an MCP server in the StatusNeedsAuth state. Mirrors
// src/tools/McpAuthTool/McpAuthTool.ts (createMcpAuthTool): the tool is
// registered with the name "mcp__<serverName>__authenticate" so it shows
// up in the model's tool list under the same prefix as the server's
// real tools (which are absent until auth completes).
//
// On call, the tool kicks off PerformOAuthFlow synchronously with a
// generous timeout, persists the token bundle, and asks the manager to
// reconnect the server. Returns a short status string suitable for the
// LLM to relay to the user.
package mcpauthtool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/tool"
)

// authTool is the per-server pseudo-tool.
type authTool struct {
	manager    *mcp.Manager
	serverName string
	serverURL  string
}

// New creates a pseudo-tool for one server. The manager must have a
// SecureStore configured — otherwise the tool can't persist the bundle
// and Execute returns an error.
func New(manager *mcp.Manager, serverName, serverURL string) tool.Tool {
	return &authTool{
		manager:    manager,
		serverName: serverName,
		serverURL:  serverURL,
	}
}

func (t *authTool) Name() string {
	return mcp.ToolNamePrefix(t.serverName) + "authenticate"
}

func (t *authTool) Description() string {
	return fmt.Sprintf(
		"The %q MCP server is installed but requires OAuth authentication. "+
			"Call this tool to start the flow — the user's browser will open "+
			"to the authorization page, and once they approve, the server's "+
			"real tools become available automatically.",
		t.serverName,
	)
}

func (t *authTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *authTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *authTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *authTool) Execute(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	store := t.manager.SecureStore()
	if store == nil {
		return tool.ErrorResult("MCP OAuth: no secure storage configured"), nil
	}

	// 5-minute window covers the slowest user — interactive auth in a
	// browser, including SSO redirects.
	flowCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	tokens, err := mcp.PerformOAuthFlow(flowCtx, t.serverName, t.serverURL, nil, nil)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("MCP OAuth flow failed: %v", err)), nil
	}
	if err := mcp.SaveServerToken(store, t.serverName, tokens); err != nil {
		return tool.ErrorResult(fmt.Sprintf("MCP OAuth: persist tokens: %v", err)), nil
	}

	// Reconnect now that we have a bearer. Errors here are surfaced to the
	// LLM so it can ask the user to retry — the tokens are already saved,
	// so a future /mcp reconnect will pick them up.
	cwd := "" // Manager.Reconnect treats "" as user/local scope — correct for HTTP/SSE servers.
	if err := t.manager.Reconnect(context.Background(), t.serverName, cwd); err != nil {
		return tool.TextResult(fmt.Sprintf(
			"OAuth complete and tokens saved. Reconnect failed (%v) — try /mcp reconnect %s.",
			err, t.serverName,
		)), nil
	}
	return tool.TextResult(fmt.Sprintf(
		"OAuth complete. The %s MCP server is now authenticated and its tools are available.",
		t.serverName,
	)), nil
}

// RegisterPending walks manager.PendingNeedsAuth() and registers a
// pseudo-tool for each. Idempotent — calling twice for the same server
// just overwrites the previous registration via tool.Registry semantics.
// Caller passes a name→URL mapping for HTTP/SSE servers.
func RegisterPending(reg *tool.Registry, manager *mcp.Manager, urls map[string]string) {
	if reg == nil || manager == nil {
		return
	}
	for _, name := range manager.PendingNeedsAuth() {
		url, ok := urls[name]
		if !ok || url == "" {
			continue
		}
		reg.Register(New(manager, name, url))
	}
}
