// Package mcp implements the MCP (Model Context Protocol) host.
// It supports stdio, SSE, and HTTP transports — the three transports
// used by real Claude Code (decoded/client.ts, types.ts).
package mcp

import "encoding/json"

// ServerConfig describes how to connect to one MCP server.
// The discriminant field is "type" (stdio/sse/http); stdio omits it for
// backwards-compat with older .mcp.json files.
type ServerConfig struct {
	// Type is "stdio" | "sse" | "http". Empty means "stdio".
	Type string `json:"type,omitempty"`

	// Stdio fields
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// SSE / HTTP fields
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Source is the config file path this entry was loaded from.
	// Not serialized — set at load time by LoadConfigs.
	Source string `json:"-"`

	// Scope is "user" | "local" | "project" | "plugin".
	// Not serialized — set at load time by LoadConfigs.
	Scope string `json:"-"`

	// PluginName is set when the server was defined by an installed plugin.
	// Not serialized.
	PluginName string `json:"-"`
}

// McpJSON is the shape of ~/.claude/mcp.json and .mcp.json.
type McpJSON struct {
	McpServers map[string]ServerConfig `json:"mcpServers"`
}

// ToolDef is the tool shape returned by tools/list.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ResourceDef is one resource entry from resources/list.
type ResourceDef struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceContent is one content item from resources/read.
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"` // base64
}

// CallResult is the shape returned by tools/call.
type CallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is a single block in a tools/call result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// image fields omitted — added when we need them
}

// jsonrpcRequest is a JSON-RPC 2.0 request.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse is a JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

// jsonrpcError is a JSON-RPC 2.0 error object.
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonrpcError) Error() string { return e.Message }

// ServerStatus tracks connection state.
type ServerStatus string

const (
	StatusPending    ServerStatus = "pending"
	StatusConnected  ServerStatus = "connected"
	StatusFailed     ServerStatus = "failed"
	StatusDisconnected ServerStatus = "disconnected"
)

// ConnectedServer holds a live connection to one MCP server.
type ConnectedServer struct {
	Name     string
	Config   ServerConfig
	Status   ServerStatus
	Disabled bool   // true when the server is in disabledMcpServers
	Tools    []ToolDef
	Error    string // set when Status == StatusFailed
	client   Client
}
