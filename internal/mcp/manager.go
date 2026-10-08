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

	// connLocks serializes connect, reconnect and disconnect of one server,
	// so two of them can't both open a client and leave one running with
	// nothing referencing it. Keyed by server name; guarded by mu.
	connLocks map[string]*sync.Mutex

	// secureStore is the secure store backing per-server OAuth tokens.
	// When non-nil, connects inject Authorization: Bearer <token> on
	// HTTP/SSE/WS servers; a 401 sets StatusNeedsAuth instead of
	// StatusFailed either way. nil disables the OAuth path.
	secureStore secure.Storage

	// onChange runs after every change to the server set or a server's state
	// (see SetOnChange). Guarded by mu; called without it.
	onChange func()

	// cwd is the directory StartConnectAll loaded configs for.
	cwd string

	// initStarted is when StartConnectAll ran (zero before), anchoring
	// WaitForInit's budget.
	initStarted time.Time

	// changed is closed and replaced on every change to the server set or a
	// server's state, waking WaitForInit to look again. Guarded by mu.
	changed chan struct{}

	// closed is set by Close. A connect that finishes afterwards closes its
	// client instead of keeping it.
	closed bool

	// dial opens a server's transport client; nil means openClient. Tests
	// substitute fakes.
	dial dialFunc
}

// dialFunc opens the transport client for cfg. expand resolves ${VAR}
// references in config values; bearer, when set, is sent as the
// Authorization header on remote transports.
type dialFunc func(ctx context.Context, cfg ServerConfig, bearer string, expand func(string) string) (Client, error)

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{
		servers:   make(map[string]*ConnectedServer),
		connLocks: make(map[string]*sync.Mutex),
		changed:   make(chan struct{}),
	}
}

// NewManagerWithServers returns a Manager pre-populated with the given
// connected servers, keyed by name. Used by tests that exercise
// server-selection logic (e.g. picking among multiple local MCP tool
// providers) without spinning up real MCP connections.
func NewManagerWithServers(servers map[string]*ConnectedServer) *Manager {
	m := NewManager()
	for name, srv := range servers {
		m.servers[name] = srv
	}
	return m
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

// SetOnChange registers fn to run after every change to the server set or to
// a server's state: a connect starting or finishing, a disconnect or removal,
// an approval decision. fn runs synchronously on the goroutine that made the
// change, after the state lock is released but possibly while that server's
// connect lock is held, so it may read the manager (Servers, AllTools) but
// must not connect, reconnect or disconnect servers itself. Changes made on
// different goroutines call fn concurrently. nil removes the hook.
func (m *Manager) SetOnChange(fn func()) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

func (m *Manager) notify() {
	m.mu.RLock()
	fn := m.onChange
	m.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// changedLocked wakes WaitForInit after a state change. The caller holds mu
// for writing.
func (m *Manager) changedLocked() {
	if m.changed != nil {
		close(m.changed)
	}
	m.changed = make(chan struct{})
}

// Cwd returns the directory StartConnectAll loaded configs for (the session's
// working directory), or "" before it runs. Reconnects pass it so servers
// configured for this project stay in scope.
func (m *Manager) Cwd() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cwd
}

// InitWaitBudget bounds how long a prompt waits for the startup connect (see
// WaitForInit). It sits well above a healthy server's start (npx and uvx take
// a few seconds) and below ConnectTimeout, so a server wedged mid-handshake
// can't make the first prompt look dead while it times out.
const InitWaitBudget = 10 * time.Second

// StartConnectAll loads configs for cwd and connects every server in the
// background. It returns once each server's starting state is recorded:
// disabled, awaiting or denied project approval, or pending while its
// handshake runs. Outcomes arrive through SetOnChange as connects finish, and
// WaitForInit waits for them. Errors from individual servers are captured in
// the server's Error field; only a config load failure is returned.
//
// trusted must be true when the workspace has been approved by the user;
// plugin MCP servers are skipped when trusted is false (matching hooks gating).
func (m *Manager) StartConnectAll(ctx context.Context, cwd string, trusted bool) error {
	_, err := m.startConnectAll(ctx, cwd, trusted)
	return err
}

// ConnectAll is StartConnectAll followed by waiting for every server's
// connect to finish.
func (m *Manager) ConnectAll(ctx context.Context, cwd string, trusted bool) error {
	done, err := m.startConnectAll(ctx, cwd, trusted)
	if err != nil {
		return err
	}
	<-done
	return nil
}

func (m *Manager) startConnectAll(ctx context.Context, cwd string, trusted bool) (<-chan struct{}, error) {
	configs, err := LoadConfigs(cwd, trusted)
	if err != nil {
		return nil, fmt.Errorf("mcp: load configs: %w", err)
	}

	// The gates only read settings, so they settle before returning: the
	// startup approval prompt needs PendingApprovals complete up front. They
	// run in parallel because each can parse all of ~/.claude.json.
	initial := make(map[string]*ConnectedServer, len(configs))
	toDial := make(map[string]ServerConfig, len(configs))
	var gateMu sync.Mutex
	var gates sync.WaitGroup
	for name, cfg := range configs {
		gates.Go(func() {
			srv := gated(name, cfg, cwd, trusted)
			gateMu.Lock()
			defer gateMu.Unlock()
			if srv == nil {
				srv = &ConnectedServer{Name: name, Config: cfg, Status: StatusPending}
				toDial[name] = cfg
			}
			initial[name] = srv
		})
	}
	gates.Wait()

	done := make(chan struct{})
	var stale []Client
	m.mu.Lock()
	m.cwd = cwd
	m.initStarted = time.Now()
	for name, srv := range initial {
		if old := m.servers[name]; old != nil && old.client != nil {
			stale = append(stale, old.client)
		}
		m.servers[name] = srv
	}
	m.changedLocked()
	m.mu.Unlock()
	closeClients(stale)
	m.notify()

	var wg sync.WaitGroup
	for name, cfg := range toDial {
		wg.Go(func() {
			lock := m.connLock(name)
			lock.Lock()
			defer lock.Unlock()
			m.handshake(ctx, name, cfg)
		})
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	return done, nil
}

// WaitForInit waits while any server is connecting: the startup connects, and
// any started since, such as a server approved at the startup prompt. It waits
// at most budget measured from StartConnectAll rather than from this call, so
// a prompt's waiters share one deadline and prompts sent after it don't wait at
// all. It returns nil once nothing is connecting or the budget runs out
// (servers still connecting deliver their tools through SetOnChange when they
// finish), ctx's error if ctx ends first, and nil at once if StartConnectAll
// never ran.
func (m *Manager) WaitForInit(ctx context.Context, budget time.Duration) error {
	for {
		m.mu.RLock()
		started, changed := m.initStarted, m.changed
		connecting := false
		for _, srv := range m.servers {
			if srv.Status == StatusPending {
				connecting = true
				break
			}
		}
		m.mu.RUnlock()
		if started.IsZero() || !connecting {
			return nil
		}
		remaining := time.Until(started.Add(budget))
		if remaining <= 0 {
			return nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
			return nil
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("mcp: wait for startup connect: %w", ctx.Err())
		}
	}
}

// deniedNote explains why a project server the user denied stays offline.
const deniedNote = "denied at the project-server approval prompt (disabledMcpjsonServers)"

// gated returns the state a server settles in without connecting: disabled
// for this project, or a project server (.mcp.json) the user hasn't approved,
// has denied, or that sits in a workspace not yet trusted. It returns nil when
// the server should connect.
func gated(name string, cfg ServerConfig, cwd string, trusted bool) *ConnectedServer {
	// Disabled state is stored in ~/.conduit/conduit.json →
	// projects[cwd].disabledMcpServers, with Claude as an import fallback.
	if IsDisabled(name, cwd) {
		return &ConnectedServer{Name: name, Config: cfg, Status: StatusDisconnected, Disabled: true}
	}
	// Project-scope security gate: a server loaded from a .mcp.json
	// (committed to a repo) shouldn't auto-execute on first checkout.
	// User must approve via the startup dialog or explicit settings entry,
	// and nothing from .mcp.json starts before the workspace is trusted;
	// approving it at the prompt is the user's consent for that server.
	if cfg.Scope == "project" {
		switch state := mcpjsonApprovalState(name, cwd); {
		case state == mcpjsonDenied:
			return &ConnectedServer{Name: name, Config: cfg, Status: StatusDisconnected, Error: deniedNote}
		case state == mcpjsonUndecided, !trusted:
			return &ConnectedServer{Name: name, Config: cfg, Status: StatusNeedsApproval}
		}
	}
	return nil
}

// connLock returns the lock serializing connects of the named server.
func (m *Manager) connLock(name string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connLocks == nil {
		m.connLocks = make(map[string]*sync.Mutex)
	}
	l := m.connLocks[name]
	if l == nil {
		l = new(sync.Mutex)
		m.connLocks[name] = l
	}
	return l
}

// connectLocked connects one server and records the outcome. The caller
// holds the server's connect lock, so it never overlaps another connect,
// reconnect or disconnect of that server.
func (m *Manager) connectLocked(ctx context.Context, name string, cfg ServerConfig, cwd string, trusted bool) {
	if srv := gated(name, cfg, cwd, trusted); srv != nil {
		m.store(name, srv)
		return
	}
	// Pending first: this closes the client of any earlier connect, and the
	// panel and the tool registry see the server reconnecting meanwhile.
	m.store(name, &ConnectedServer{Name: name, Config: cfg, Status: StatusPending})
	m.handshake(ctx, name, cfg)
}

// handshake opens the client of a server recorded as pending, runs initialize
// and tools/list, and records the outcome. The caller holds the server's
// connect lock.
func (m *Manager) handshake(ctx context.Context, name string, cfg ServerConfig) {
	srv := &ConnectedServer{
		Name:   name,
		Config: cfg,
		Status: StatusPending,
	}

	// Bounds the handshake below so one wedged server (spawns but never
	// responds) can't hang the whole ConnectAll fan-out — see ConnectTimeout.
	// Not the stdio subprocess's lifetime: exec.CommandContext kills the child
	// when its context is Done, and this deferred cancel() fires as soon as
	// handshake returns. The subprocess gets the long-lived parent ctx.
	handshakeCtx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()

	// Config values can reference environment variables. Track any that don't
	// resolve so an unset ${TOKEN} is reported as such instead of surfacing as
	// an opaque exec failure or 401 — see expandEnvVars.
	var missingVars []string
	seenMissing := map[string]bool{}
	expand := func(s string) string {
		expanded, missing := expandEnvVars(s)
		for _, name := range missing {
			if !seenMissing[name] {
				seenMissing[name] = true
				missingVars = append(missingVars, name)
			}
		}
		return expanded
	}
	// fail decorates a failure message with the unresolved variables, which
	// are the likeliest cause of it.
	fail := func(format string, a ...any) string {
		msg := fmt.Sprintf(format, a...)
		if len(missingVars) > 0 {
			msg += " — " + missingVarsNote(missingVars)
		}
		return msg
	}

	dial := m.dial
	if dial == nil {
		dial = openClient
	}
	// Look up a persisted OAuth bearer for HTTP/SSE/WS servers so the
	// initial connect carries an Authorization header. Stdio servers
	// never use it.
	client, err := dial(ctx, cfg, m.loadBearer(name), expand)
	if err != nil {
		srv.Status = StatusFailed
		srv.Error = fail("%v", err)
		m.store(name, srv)
		return
	}

	t := strings.ToLower(cfg.Type)
	remote := t == "http" || t == "sse" || t == "ws" || t == "websocket"

	instructions, err := client.Initialize(handshakeCtx)
	if err != nil {
		_ = client.Close()
		if errors.Is(err, ErrUnauthorized) && remote {
			srv.Status = StatusNeedsAuth
			srv.Error = "OAuth required — run /mcp auth " + name
		} else {
			srv.Status = StatusFailed
			srv.Error = fail("initialize: %v", err)
		}
		m.store(name, srv)
		return
	}
	srv.Instructions = instructions

	tools, err := client.ListTools(handshakeCtx)
	if err != nil {
		_ = client.Close()
		if errors.Is(err, ErrUnauthorized) && remote {
			srv.Status = StatusNeedsAuth
			srv.Error = "OAuth required — run /mcp auth " + name
		} else {
			srv.Status = StatusFailed
			srv.Error = fail("tools/list: %v", err)
		}
		m.store(name, srv)
		return
	}

	srv.Status = StatusConnected
	srv.Tools = tools
	srv.client = client
	if len(missingVars) > 0 {
		// Connected, but something in the config didn't resolve: the server
		// may still misbehave (empty arg, unauthenticated header), so say so.
		srv.Error = missingVarsNote(missingVars)
	}
	m.store(name, srv)
}

// openClient is the default dialFunc: it builds the client for cfg's
// transport. Only stdio starts anything here; remote transports connect on
// their first request.
func openClient(ctx context.Context, cfg ServerConfig, bearer string, expand func(string) string) (Client, error) {
	switch strings.ToLower(cfg.Type) {
	case "", "stdio":
		cmd := expand(cfg.Command)
		args := make([]string, len(cfg.Args))
		for i, a := range cfg.Args {
			args[i] = expand(a)
		}
		envMap := make(map[string]string, len(cfg.Env))
		for k, v := range cfg.Env {
			envMap[k] = expand(v)
		}
		return NewStdioClient(ctx, cmd, args, envMap)
	case "sse", "http":
		hdrs := remoteHeaders(cfg, bearer, expand)
		return NewHTTPClient(expand(cfg.URL), hdrs), nil
	case "ws", "websocket":
		hdrs := remoteHeaders(cfg, bearer, expand)
		return NewWebSocketClient(expand(cfg.URL), hdrs), nil
	default:
		return nil, fmt.Errorf("unsupported transport type %q", cfg.Type)
	}
}

// remoteHeaders expands cfg's configured headers and adds the OAuth bearer.
func remoteHeaders(cfg ServerConfig, bearer string, expand func(string) string) map[string]string {
	hdrs := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		hdrs[k] = expand(v)
	}
	if bearer != "" {
		hdrs["Authorization"] = "Bearer " + bearer
	}
	return hdrs
}

// missingVarsNote renders unresolved config variable names for display.
func missingVarsNote(names []string) string {
	return "missing environment variables: " + strings.Join(names, ", ")
}

// store records srv as name's state and notifies the change hook. The client
// of the state it replaces is closed unless srv carries it over; once Close
// has run, srv's own client is closed too instead of kept.
func (m *Manager) store(name string, srv *ConnectedServer) {
	var stale []Client
	m.mu.Lock()
	if old := m.servers[name]; old != nil && old.client != nil && old.client != srv.client {
		stale = append(stale, old.client)
	}
	if m.closed && srv.client != nil {
		stale = append(stale, srv.client)
		srv.client = nil
		srv.Status = StatusDisconnected
	}
	m.servers[name] = srv
	m.changedLocked()
	m.mu.Unlock()
	closeClients(stale)
	m.notify()
}

// remove deletes name's entry, closes its client and notifies. The caller
// holds the server's connect lock.
func (m *Manager) remove(name string) {
	m.mu.Lock()
	srv, ok := m.servers[name]
	delete(m.servers, name)
	if ok {
		m.changedLocked()
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if srv.client != nil {
		closeClients([]Client{srv.client})
	}
	m.notify()
}

// has reports whether the manager holds an entry for name, in any state.
func (m *Manager) has(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.servers[name]
	return ok
}

// closeClients closes each client. Best-effort: a server that fails to shut
// down cleanly has been dropped from the session either way.
func closeClients(clients []Client) {
	for _, c := range clients {
		_ = c.Close()
	}
}

// SyncPluginServers re-reads configs and reconciles plugin-scoped MCP servers:
// new plugin servers are connected, removed plugin servers are disconnected.
// Non-plugin servers (user/project/local scope) are left untouched.
// Called after /plugin install or /plugin uninstall.
//
// trusted must be true when the workspace has been approved by the user;
// plugin MCP servers are skipped when trusted is false.
func (m *Manager) SyncPluginServers(ctx context.Context, cwd string, trusted bool) {
	configs, err := LoadConfigs(cwd, trusted)
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
	for _, srv := range m.Servers() {
		if srv.Config.Scope != "plugin" {
			continue
		}
		if _, keep := desired[srv.Name]; !keep {
			m.DisconnectServer(srv.Name)
		}
	}

	// Connect plugin servers not yet in the manager. The check is made under
	// the server's connect lock: syncs run concurrently (installing several
	// plugins at once runs one per plugin), and another may connect the
	// server while this one waits for the lock.
	var wg sync.WaitGroup
	for name, cfg := range desired {
		wg.Go(func() {
			lock := m.connLock(name)
			lock.Lock()
			defer lock.Unlock()
			if m.has(name) {
				return
			}
			m.connectLocked(ctx, name, cfg, cwd, trusted)
		})
	}
	wg.Wait()
}

// DisconnectServer closes and removes a server from the live connection map.
// Used when a server is removed from config at runtime. A connect of the same
// server in flight finishes first, so its client is closed rather than left
// running.
func (m *Manager) DisconnectServer(name string) {
	lock := m.connLock(name)
	lock.Lock()
	defer lock.Unlock()
	m.remove(name)
}

// Reconnect closes and re-connects a single named server.
// It re-reads the config so any edits to MCP config/state take effect: a
// server that is now disabled or gated settles in that state, and one no
// longer configured is removed and reported as not found.
//
// trusted must be true when the workspace has been approved by the user;
// plugin MCP servers are skipped when trusted is false.
func (m *Manager) Reconnect(ctx context.Context, name, cwd string, trusted bool) error {
	configs, err := LoadConfigs(cwd, trusted)
	if err != nil {
		return fmt.Errorf("mcp: reconnect %q: %w", name, err)
	}
	lock := m.connLock(name)
	lock.Lock()
	defer lock.Unlock()
	cfg, ok := configs[name]
	if !ok {
		m.remove(name)
		return fmt.Errorf("mcp: server %q not found in config", name)
	}
	m.connectLocked(ctx, name, cfg, cwd, trusted)
	return nil
}

// ApprovalDecided records the user's answer to the project-server approval
// prompt for name without connecting anything, so PendingApprovals stops
// offering it right away: an approved server becomes pending (Reconnect it to
// connect) and a denied one disconnected. A server that isn't awaiting
// approval is left alone. Only the state lock is taken, never a connect lock,
// so it returns promptly even while other servers connect.
func (m *Manager) ApprovalDecided(name string, approved bool) {
	m.mu.Lock()
	srv, ok := m.servers[name]
	if !ok || srv.Status != StatusNeedsApproval {
		m.mu.Unlock()
		return
	}
	next := &ConnectedServer{Name: name, Config: srv.Config, Status: StatusPending}
	if !approved {
		next.Status = StatusDisconnected
		next.Error = deniedNote
	}
	m.servers[name] = next
	m.changedLocked()
	m.mu.Unlock()
	m.notify()
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

// Server returns the named server's current state.
func (m *Manager) Server(name string) (*ConnectedServer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	srv, ok := m.servers[name]
	return srv, ok
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
//
// The RLock is released before the RPC so that a long-running tool call (which
// can take tens of seconds) does not block DisconnectServer or SyncPluginServers,
// both of which need a write lock.
func (m *Manager) CallTool(ctx context.Context, qualifiedName string, input []byte) (CallResult, error) {
	// Snapshot the matching client pointer under the RLock, then release.
	m.mu.RLock()
	var matchedClient Client
	var matchedToolName string
	for srvName, srv := range m.servers {
		if srv.Status != StatusConnected {
			continue
		}
		prefix := ToolNamePrefix(srvName)
		if !strings.HasPrefix(qualifiedName, prefix) {
			continue
		}
		matchedClient = srv.client
		matchedToolName = strings.TrimPrefix(qualifiedName, prefix)
		break
	}
	m.mu.RUnlock()

	if matchedClient == nil {
		return CallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("mcp: tool %q not found", qualifiedName)}}}, nil
	}
	return matchedClient.CallTool(ctx, matchedToolName, input)
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

// ConnectTimeout is the maximum time to wait for one MCP server to spawn/dial,
// complete its initialize handshake, and list tools. Without this, a server
// that hangs on startup (e.g. a stdio command that never writes a response)
// would block ConnectAll's WaitGroup forever, since the ctx it's called with
// has no deadline of its own — hanging conduit's whole startup before the TUI
// ever appears.
const ConnectTimeout = 15 * time.Second

// CloseTimeout is the maximum time to wait for MCP servers to shut down.
const CloseTimeout = 5 * time.Second

// Close shuts down all server connections with a timeout. Connects still in
// flight close their client when they finish.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	clients := make([]Client, 0, len(m.servers))
	for _, srv := range m.servers {
		if srv.client != nil {
			clients = append(clients, srv.client)
		}
	}
	m.mu.Unlock()

	if len(clients) == 0 {
		return
	}

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, client := range clients {
			wg.Add(1)
			go func(c Client) {
				defer wg.Done()
				_ = c.Close()
			}(client)
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(CloseTimeout):
		// Timeout: some MCP servers didn't shut down cleanly.
		// Continue anyway to avoid blocking the user.
	}
}

// NamedTool is a tool from an MCP server with its qualified name.
type NamedTool struct {
	ServerName    string
	Prefix        string
	Def           ToolDef
	QualifiedName string
}
