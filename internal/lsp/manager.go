package lsp

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// serverSpec describes how to launch a language server.
type serverSpec struct {
	cmd  string
	args []string
}

// extToSpec maps file extensions to language server launch specs.
// Multiple extensions may share the same spec (e.g. .ts and .tsx).
var extToSpec = map[string]serverSpec{
	".go":  {cmd: "gopls"},
	".ts":  {cmd: "typescript-language-server", args: []string{"--stdio"}},
	".tsx": {cmd: "typescript-language-server", args: []string{"--stdio"}},
	".js":  {cmd: "typescript-language-server", args: []string{"--stdio"}},
	".jsx": {cmd: "typescript-language-server", args: []string{"--stdio"}},
	".py":  {cmd: "pylsp"},
	".rs":  {cmd: "rust-analyzer"},
}

// alternateServers lists fallback commands when the primary is absent.
var alternateServers = map[string]serverSpec{
	"pylsp": {cmd: "pyright-langserver", args: []string{"--stdio", "--project-path", "."}},
}

// Manager owns one *Client per language and starts servers on demand.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client // language key → client
}

// NewManager creates an empty Manager.
func NewManager() *Manager {
	return &Manager{clients: make(map[string]*Client)}
}

// ServerFor returns the running LSP client for the given file path, launching
// the appropriate language server if it has not been started yet.
// Returns an error if no known server exists for the file's extension, or if
// the server binary is not on PATH.
func (m *Manager) ServerFor(ctx context.Context, filePath string) (*Client, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	spec, ok := extToSpec[ext]
	if !ok {
		return nil, fmt.Errorf("lsp: no known server for extension %q", ext)
	}

	// Use extension as the language key so .ts/.tsx/.js/.jsx share one client.
	langKey := languageKey(ext)

	m.mu.Lock()
	defer m.mu.Unlock()

	if cl, exists := m.clients[langKey]; exists {
		return cl, nil
	}

	// Try to find the binary; fall back to alternate if needed.
	chosenSpec, err := resolveSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("lsp: %w", err)
	}

	cl, err := NewClient(ctx, chosenSpec.cmd, chosenSpec.args...)
	if err != nil {
		return nil, fmt.Errorf("lsp: start server for %q: %w", ext, err)
	}

	m.clients[langKey] = cl
	return cl, nil
}

// Close shuts down all running language servers.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, cl := range m.clients {
		_ = cl.Close()
		delete(m.clients, key)
	}
}

// languageKey maps an extension to a stable language key so that .ts and .tsx
// share the same server instance.
func languageKey(ext string) string {
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx":
		return "typescript"
	default:
		return strings.TrimPrefix(ext, ".")
	}
}

// resolveSpec checks whether spec.cmd is on PATH, trying alternates if not.
func resolveSpec(spec serverSpec) (serverSpec, error) {
	if _, err := exec.LookPath(spec.cmd); err == nil {
		return spec, nil
	}
	// Try alternate.
	if alt, ok := alternateServers[spec.cmd]; ok {
		if _, err := exec.LookPath(alt.cmd); err == nil {
			return alt, nil
		}
	}
	return serverSpec{}, fmt.Errorf("server %q not found on PATH", spec.cmd)
}

// LanguageID returns the LSP languageId string for a file extension.
func LanguageID(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	default:
		return "plaintext"
	}
}
