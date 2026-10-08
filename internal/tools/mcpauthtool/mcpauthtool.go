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
	"os"
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

// IsAuthTool reports whether t is an authenticate pseudo-tool made by New.
func IsAuthTool(t tool.Tool) bool {
	_, ok := t.(*authTool)
	return ok
}

func (t *authTool) Name() string {
	return mcp.ToolNamePrefix(t.serverName) + "authenticate"
}

func (t *authTool) Description() string {
	return fmt.Sprintf(
		"The %q MCP server is installed but requires OAuth authentication. "+
			"Call this tool to start the flow — the user's browser will open "+
			"to the authorization page, and once they approve, the server is "+
			"reconnected and its tools are registered (load them with ToolSearch).",
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
	return t.reconnect(), nil
}

// reconnect connects the server with the tokens just saved and reports how
// that went. Failures come back as text rather than errors so the model can
// ask the user to retry: the tokens are saved, so a later reconnect from /mcp
// picks them up.
func (t *authTool) reconnect() tool.Result {
	// The manager's own cwd, not the tool call's: configs are scoped to the
	// session's directory, and an empty or different one drops the
	// project-scoped (.mcp.json) servers, which then fail as "not found".
	cwd := t.manager.Cwd()
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// Background, not the tool call's ctx: a stdio server's process lives as
	// long as the context it was started with.
	// trusted=true: tool execution only reaches here when the workspace is already trusted.
	if err := t.manager.Reconnect(context.Background(), t.serverName, cwd, true); err != nil {
		return tool.TextResult(fmt.Sprintf(
			"OAuth complete and tokens saved, but reconnecting failed (%v). Ask the user to reconnect %s from /mcp.",
			err, t.serverName,
		))
	}
	srv, ok := t.manager.Server(t.serverName)
	if !ok || srv.Status != mcp.StatusConnected {
		state := "not connected"
		if ok {
			state = string(srv.Status)
			if srv.Error != "" {
				state += ": " + srv.Error
			}
		}
		return tool.TextResult(fmt.Sprintf(
			"OAuth complete and tokens saved, but the %s MCP server is %s. Ask the user to retry from /mcp.",
			t.serverName, state,
		))
	}
	return tool.TextResult(fmt.Sprintf(
		"OAuth complete. The %s MCP server is connected and its %d tools are registered under %s; load them with ToolSearch.",
		t.serverName, len(srv.Tools), mcp.ToolNamePrefix(t.serverName),
	))
}
