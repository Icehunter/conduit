// Package mcptest runs an in-process MCP server over the HTTP transport, and
// isolates MCP configuration, for tests that drive a real mcp.Manager.
package mcptest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
)

// Server is a minimal MCP server speaking JSON-RPC over HTTP POST. It answers
// initialize, tools/list and tools/call; what it lists, and whether it
// demands OAuth, can change between connects.
type Server struct {
	*httptest.Server

	mu           sync.Mutex
	tools        []mcp.ToolDef
	instructions string
	unauthorized bool
	hold         chan struct{} // non-nil: initialize waits for it to close
}

// NewServer starts a server listing tools. It is closed when the test ends.
func NewServer(t testing.TB, tools ...mcp.ToolDef) *Server {
	t.Helper()
	s := &Server{tools: tools}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// SetTools replaces the tools listed from the next tools/list on.
func (s *Server) SetTools(tools ...mcp.ToolDef) {
	s.mu.Lock()
	s.tools = tools
	s.mu.Unlock()
}

// SetInstructions sets the instructions returned by initialize.
func (s *Server) SetInstructions(text string) {
	s.mu.Lock()
	s.instructions = text
	s.mu.Unlock()
}

// HoldInitialize makes initialize requests wait until release is called, so a
// test can tell whether its caller blocks on the handshake. Release is also
// run when the test ends.
func (s *Server) HoldInitialize(t testing.TB) (release func()) {
	t.Helper()
	hold := make(chan struct{})
	s.mu.Lock()
	s.hold = hold
	s.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	return release
}

// SetUnauthorized makes every request fail with 401 until reset, as a
// server awaiting OAuth does.
func (s *Server) SetUnauthorized(on bool) {
	s.mu.Lock()
	s.unauthorized = on
	s.mu.Unlock()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	tools := append([]mcp.ToolDef(nil), s.tools...)
	instructions := s.instructions
	unauthorized := s.unauthorized
	hold := s.hold
	s.mu.Unlock()
	if unauthorized {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Method == "initialize" && hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "instructions": instructions}
	case "tools/list":
		result = map[string]any{"tools": tools}
	case "tools/call":
		result = map[string]any{"content": []map[string]string{{"type": "text", "text": "ok"}}}
	default:
		result = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

// IsolateConfig points every MCP and settings source at fresh temp dirs, so
// only servers a test writes are configured, and returns a project directory
// to use as the session's cwd.
func IsolateConfig(t testing.TB) string {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	return t.TempDir()
}

// WriteUserServers writes servers to conduit's user MCP config
// ($CONDUIT_CONFIG_DIR/mcp.json); call IsolateConfig first.
func WriteUserServers(t testing.TB, servers map[string]mcp.ServerConfig) {
	t.Helper()
	writeJSON(t, filepath.Join(os.Getenv("CONDUIT_CONFIG_DIR"), "mcp.json"), mcp.McpJSON{McpServers: servers})
}

// WriteProjectServers writes servers to cwd's .mcp.json, where they are
// project-scoped and need approval before they connect.
func WriteProjectServers(t testing.TB, cwd string, servers map[string]mcp.ServerConfig) {
	t.Helper()
	writeJSON(t, filepath.Join(cwd, ".mcp.json"), mcp.McpJSON{McpServers: servers})
}

// ApproveProjectServers approves the named project-scoped servers for cwd the
// way the approval prompt does, in conduit's per-project state under the
// user's home. Settings files inside the project don't count.
func ApproveProjectServers(t testing.TB, cwd string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := settings.ApproveMcpjsonServer(name, "yes", cwd); err != nil {
			t.Fatal(err)
		}
	}
}

func writeJSON(t testing.TB, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
