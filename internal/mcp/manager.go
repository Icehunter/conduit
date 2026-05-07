package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/secure"
)

// Manager connects to all configured MCP servers and keeps them alive.
// It is the single source of truth for which servers are connected and
// what tools they expose. Mirrors MCPConnectionManager.tsx.
type Manager struct {
	mu      sync.RWMutex
	servers map[string]*ConnectedServer // keyed by server name

	// secureStore is the secure store backing per-server OAuth tokens.
	// When non-nil, connectWithCwd injects Authorization: Bearer <token>
	// on HTTP/SSE/WS connects and branches on ErrUnauthorized to set
	// StatusNeedsAuth instead of StatusFailed. nil disables OAuth path.
	secureStore secure.Storage
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{servers: make(map[string]*ConnectedServer)}
}

// SetSecureStore wires a secure.Storage so the manager can load persisted
// OAuth bearer tokens for HTTP/SSE/WS servers. Callers that don't need
// MCP OAuth can leave this unset.
func (m *Manager) SetSecureStore(s secure.Storage) {
	m.mu.Lock()
	m.secureStore = s
	m.mu.Unlock()
}

// SecureStore returns the configured secure store (nil if unset). Used
// by McpAuthTool to persist newly-obtained tokens.
func (m *Manager) SecureStore() secure.Storage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.secureStore
}

// ConnectAll loads configs for cwd and connects to every server in parallel.
// Errors from individual servers are captured in the server's Error field;
// ConnectAll itself only returns an error if config loading fails.
func (m *Manager) ConnectAll(ctx context.Context, cwd string) error {
	configs, err := LoadConfigs(cwd)
	if err != nil {
		return fmt.Errorf("mcp: load configs: %w", err)
	}

	var wg sync.WaitGroup
	for name, cfg := range configs {
		wg.Add(1)
		go func(name string, cfg ServerConfig) {
			defer wg.Done()
			m.connectWithCwd(ctx, name, cfg, cwd)
		}(name, cfg)
	}
	wg.Wait()
	return nil
}

// connectWithCwd establishes a connection to one MCP server.
// If the server is in disabledMcpServers for cwd it is stored as disabled without connecting.
func (m *Manager) connectWithCwd(ctx context.Context, name string, cfg ServerConfig, cwd string) {
	srv := &ConnectedServer{
		Name:   name,
		Config: cfg,
		Status: StatusPending,
	}

	// Check disabled state (stored in ~/.conduit/conduit.json →
	// projects[cwd].disabledMcpServers, with Claude as an import fallback).
	if IsDisabled(name, cwd) {
		srv.Status = StatusDisconnected
		srv.Disabled = true
		m.store(name, srv)
		return
	}

	// Project-scope security gate: a server loaded from a .mcp.json
	// (committed to a repo) shouldn't auto-execute on first checkout.
	// User must approve via the startup dialog or explicit settings entry.
	if cfg.Scope == "project" && !isMcpjsonApproved(name, cwd) {
		srv.Status = StatusNeedsApproval
		m.store(name, srv)
		return
	}

	var client Client
	var err error

	// Look up a persisted OAuth bearer for HTTP/SSE/WS servers so the
	// initial connect carries an Authorization header. Stdio servers
	// never use this path.
	bearer := m.loadBearer(name)

	t := strings.ToLower(cfg.Type)
	switch t {
	case "", "stdio":
		cmd := expandEnv(cfg.Command)
		args := make([]string, len(cfg.Args))
		for i, a := range cfg.Args {
			args[i] = expandEnv(a)
		}
		envMap := make(map[string]string, len(cfg.Env))
		for k, v := range cfg.Env {
			envMap[k] = expandEnv(v)
		}
		client, err = NewStdioClient(ctx, cmd, args, envMap)
	case "sse", "http":
		hdrs := make(map[string]string, len(cfg.Headers))
		for k, v := range cfg.Headers {
			hdrs[k] = expandEnv(v)
		}
		if bearer != "" {
			hdrs["Authorization"] = "Bearer " + bearer
		}
		client = NewHTTPClient(expandEnv(cfg.URL), hdrs)
	case "ws", "websocket":
		hdrs := make(map[string]string, len(cfg.Headers))
		for k, v := range cfg.Headers {
			hdrs[k] = expandEnv(v)
		}
		if bearer != "" {
			hdrs["Authorization"] = "Bearer " + bearer
		}
		client = NewWebSocketClient(expandEnv(cfg.URL), hdrs)
	default:
		srv.Status = StatusFailed
		srv.Error = fmt.Sprintf("unsupported transport type %q", cfg.Type)
		m.store(name, srv)
		return
	}

	if err != nil {
		srv.Status = StatusFailed
		srv.Error = err.Error()
		m.store(name, srv)
		return
	}

	instructions, err := client.Initialize(ctx)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) && (t == "http" || t == "sse" || t == "ws" || t == "websocket") {
			srv.Status = StatusNeedsAuth
			srv.Error = "OAuth required — run /mcp auth " + name
			_ = client.Close()
			m.store(name, srv)
			return
		}
		srv.Status = StatusFailed
		srv.Error = fmt.Sprintf("initialize: %v", err)
		_ = client.Close()
		m.store(name, srv)
		return
	}
	srv.Instructions = instructions

	tools, err := client.ListTools(ctx)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) && (t == "http" || t == "sse" || t == "ws" || t == "websocket") {
			srv.Status = StatusNeedsAuth
			srv.Error = "OAuth required — run /mcp auth " + name
			_ = client.Close()
			m.store(name, srv)
			return
		}
		srv.Status = StatusFailed
		srv.Error = fmt.Sprintf("tools/list: %v", err)
		_ = client.Close()
		m.store(name, srv)
		return
	}

	srv.Status = StatusConnected
	srv.Tools = tools
	srv.client = client
	m.store(name, srv)
}

func (m *Manager) store(name string, srv *ConnectedServer) {
	m.mu.Lock()
	m.servers[name] = srv
	m.mu.Unlock()
}

// SyncPluginServers re-reads configs and reconciles plugin-scoped MCP servers:
// new plugin servers are connected, removed plugin servers are disconnected.
// Non-plugin servers (user/project/local scope) are left untouched.
// Called after /plugin install or /plugin uninstall.
func (m *Manager) SyncPluginServers(ctx context.Context, cwd string) {
	configs, err := LoadConfigs(cwd)
	if err != nil {
		return
	}

	// Determine which plugin server names should exist now.
	desired := map[string]ServerConfig{}
	for name, cfg := range configs {
		if cfg.Scope == "plugin" {
			desired[name] = cfg
		}
	}

	// Disconnect plugin servers no longer in config.
	m.mu.Lock()
	var toRemove []string
	for name, srv := range m.servers {
		if srv.Config.Scope == "plugin" {
			if _, keep := desired[name]; !keep {
				if srv.client != nil {
					_ = srv.client.Close()
				}
				toRemove = append(toRemove, name)
			}
		}
	}
	for _, name := range toRemove {
		delete(m.servers, name)
	}
	m.mu.Unlock()

	// Connect plugin servers not yet in the manager.
	var wg sync.WaitGroup
	for name, cfg := range desired {
		m.mu.RLock()
		_, exists := m.servers[name]
		m.mu.RUnlock()
		if !exists {
			wg.Add(1)
			go func(n string, c ServerConfig) {
				defer wg.Done()
				m.connectWithCwd(ctx, n, c, cwd)
			}(name, cfg)
		}
	}
	wg.Wait()
}

// DisconnectServer closes and removes a server from the live connection map.
// Used when disabling a server at runtime.
func (m *Manager) DisconnectServer(name string) {
	m.mu.Lock()
	srv, ok := m.servers[name]
	if ok && srv.client != nil {
		_ = srv.client.Close()
	}
	delete(m.servers, name)
	m.mu.Unlock()
}

// Reconnect closes and re-connects a single named server.
// It re-reads the config so any edits to MCP config/state take effect.
func (m *Manager) Reconnect(ctx context.Context, name, cwd string) error {
	// Close existing connection if any.
	m.mu.Lock()
	if srv, ok := m.servers[name]; ok && srv.client != nil {
		_ = srv.client.Close()
	}
	delete(m.servers, name)
	m.mu.Unlock()

	configs, err := LoadConfigs(cwd)
	if err != nil {
		return err
	}
	cfg, ok := configs[name]
	if !ok {
		return fmt.Errorf("mcp: server %q not found in config", name)
	}
	m.connectWithCwd(ctx, name, cfg, cwd)
	return nil
}

// loadBearer returns the persisted OAuth access token for serverName, or
// "" if no token is stored / no secure store is wired. Refreshes the
// token in place when it's within a 60s safety window of expiry. Errors
// are swallowed — the worst case is a 401 which the connect path handles.
func (m *Manager) loadBearer(serverName string) string {
	m.mu.RLock()
	ss := m.secureStore
	m.mu.RUnlock()
	if ss == nil {
		return ""
	}
	tokens, err := LoadServerToken(ss, serverName)
	if err != nil {
		return ""
	}
	if tokens.RefreshToken != "" && tokens.TokenEndpoint != "" && tokens.Client.ClientID != "" {
		// 60s safety window — refresh before the server starts rejecting.
		if !tokens.ExpiresAt.IsZero() && tokens.ExpiresAt.Before(time.Now().Add(60*time.Second)) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fresh, ferr := RefreshToken(ctx, tokens.TokenEndpoint, tokens.RefreshToken, tokens.Client.ClientID)
			if ferr == nil && fresh.AccessToken != "" {
				fresh.Client = tokens.Client
				if fresh.RefreshToken == "" {
					fresh.RefreshToken = tokens.RefreshToken
				}
				_ = SaveServerToken(ss, serverName, fresh)
				return fresh.AccessToken
			}
		}
	}
	return tokens.AccessToken
}

// PendingNeedsAuth returns the names of HTTP/SSE/WS MCP servers that
// returned 401 on connect — the caller (TUI) surfaces these via the
// /mcp panel and the McpAuthTool pseudo-tool.
func (m *Manager) PendingNeedsAuth() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for name, srv := range m.servers {
		if srv.Status == StatusNeedsAuth {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// PendingApprovals returns the names of project-scope MCP servers that
// are awaiting user approval (StatusNeedsApproval), in deterministic
// order. Used by the TUI to drive the approval dialog on startup.
func (m *Manager) PendingApprovals() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for name, srv := range m.servers {
		if srv.Status == StatusNeedsApproval {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Servers returns a snapshot of all server states.
func (m *Manager) Servers() []*ConnectedServer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ConnectedServer, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, s)
	}
	return out
}

// ServerInstructions returns a map of serverName → instructions for all
// connected servers that provided instructions in their initialize response.
// These are injected into the system prompt as additional context.
func (m *Manager) ServerInstructions() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string)
	for name, srv := range m.servers {
		if srv.Status == StatusConnected && srv.Instructions != "" {
			out[name] = srv.Instructions
		}
	}
	return out
}

// ToolNamePrefix returns the Claude-compatible MCP tool prefix for a server.
func ToolNamePrefix(serverName string) string {
	return "mcp__" + strings.TrimSuffix(NormalizeServerName(serverName), "__") + "__"
}

// AllTools returns all tools across all connected servers, with names
// prefixed by "mcp__<serverName>__" to avoid collisions.
func (m *Manager) AllTools() []NamedTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var tools []NamedTool
	for srvName, srv := range m.servers {
		if srv.Status != StatusConnected {
			continue
		}
		prefix := ToolNamePrefix(srvName)
		for _, t := range srv.Tools {
			tools = append(tools, NamedTool{
				ServerName:    srvName,
				Prefix:        prefix,
				Def:           t,
				QualifiedName: prefix + t.Name,
			})
		}
	}
	return tools
}

// CallTool dispatches a tool call to the appropriate server.
// qualifiedName is "mcp__<serverName>__<toolName>" as returned by AllTools.
func (m *Manager) CallTool(ctx context.Context, qualifiedName string, input []byte) (CallResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for srvName, srv := range m.servers {
		if srv.Status != StatusConnected {
			continue
		}
		prefix := ToolNamePrefix(srvName)
		if !strings.HasPrefix(qualifiedName, prefix) {
			continue
		}
		toolName := strings.TrimPrefix(qualifiedName, prefix)
		return srv.client.CallTool(ctx, toolName, input)
	}
	return CallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("mcp: tool %q not found", qualifiedName)}}}, nil
}

// ListResources fetches resources from the named server.
func (m *Manager) ListResources(ctx context.Context, serverName string) ([]ResourceDef, error) {
	m.mu.RLock()
	srv, ok := m.servers[serverName]
	m.mu.RUnlock()
	if !ok || srv.client == nil {
		return nil, fmt.Errorf("mcp: server %q not found", serverName)
	}
	return srv.client.ListResources(ctx)
}

// ReadResource reads a resource from the named server.
func (m *Manager) ReadResource(ctx context.Context, serverName, uri string) ([]ResourceContent, error) {
	m.mu.RLock()
	srv, ok := m.servers[serverName]
	m.mu.RUnlock()
	if !ok || srv.client == nil {
		return nil, fmt.Errorf("mcp: server %q not found", serverName)
	}
	return srv.client.ReadResource(ctx, uri)
}

// Close shuts down all server connections.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, srv := range m.servers {
		if srv.client != nil {
			_ = srv.client.Close()
		}
	}
}

// NamedTool is a tool from an MCP server with its qualified name.
type NamedTool struct {
	ServerName    string
	Prefix        string
	Def           ToolDef
	QualifiedName string
}
