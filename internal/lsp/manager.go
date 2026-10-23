package lsp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ServerOverride allows per-language-server customisation in conduit.json.
// The key is the langKey (e.g. "go", "typescript", "python").
type ServerOverride struct {
	Cmd      string   `json:"cmd,omitempty"`
	Args     []string `json:"args,omitempty"`
	Env      []string `json:"env,omitempty"` // each entry is "KEY=VALUE"
	Disabled bool     `json:"disabled,omitempty"`
}

// ServerStatus describes the life-cycle state of a language server.
type ServerStatus int

const (
	StatusUnknown    ServerStatus = iota // not yet requested
	StatusConnecting                     // launching / handshaking
	StatusConnected                      // ready
	StatusBroken                         // exited or failed to start
	StatusDisabled                       // disabled via config override
)

func (s ServerStatus) String() string {
	switch s {
	case StatusConnecting:
		return "connecting"
	case StatusConnected:
		return "connected"
	case StatusBroken:
		return "broken"
	case StatusDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// serverSpec describes how to launch a language server.
type serverSpec struct {
	cmd  string
	args []string
}

// extToSpec maps file extensions to language server launch specs.
var extToSpec = map[string]serverSpec{
	// Go
	".go": {cmd: "gopls"},
	// TypeScript / JavaScript
	".ts":  {cmd: "typescript-language-server", args: []string{"--stdio"}},
	".tsx": {cmd: "typescript-language-server", args: []string{"--stdio"}},
	".js":  {cmd: "typescript-language-server", args: []string{"--stdio"}},
	".jsx": {cmd: "typescript-language-server", args: []string{"--stdio"}},
	// Python
	".py": {cmd: "pylsp"},
	// Rust
	".rs": {cmd: "rust-analyzer"},
	// Vue
	".vue": {cmd: "vue-language-server", args: []string{"--stdio"}},
	// Svelte
	".svelte": {cmd: "svelte-language-server", args: []string{"--stdio"}},
	// Astro
	".astro": {cmd: "astro-ls", args: []string{"--stdio"}},
	// YAML
	".yaml": {cmd: "yaml-language-server", args: []string{"--stdio"}},
	".yml":  {cmd: "yaml-language-server", args: []string{"--stdio"}},
	// Lua
	".lua": {cmd: "lua-language-server"},
	// C#
	".cs": {cmd: "OmniSharp", args: []string{"-lsp"}},
	// Java
	".java": {cmd: "jdtls"},
	// Bash / shell
	".sh":   {cmd: "bash-language-server", args: []string{"start"}},
	".bash": {cmd: "bash-language-server", args: []string{"start"}},
	// Dockerfile
	".dockerfile": {cmd: "docker-langserver", args: []string{"--stdio"}},
	// Terraform
	".tf": {cmd: "terraform-ls", args: []string{"serve"}},
	// Nix
	".nix": {cmd: "nil"},
}

// dockerfileBasenames handles files named literally "Dockerfile".
var dockerfileBasenames = map[string]serverSpec{
	"dockerfile": {cmd: "docker-langserver", args: []string{"--stdio"}},
}

// alternateServers lists fallback commands when the primary is absent.
var alternateServers = map[string]serverSpec{
	"pylsp": {cmd: "pyright-langserver", args: []string{"--stdio", "--project-path", "."}},
}

// Manager owns one *Client per language and starts servers on demand.
type Manager struct {
	mu        sync.Mutex
	clients   map[string]*Client        // langKey → client
	status    sync.Map                  // langKey → ServerStatus
	overrides map[string]ServerOverride // langKey → override

	// everConnected latches true once any language server has connected. It
	// backs the LSP tool's isEnabled gate: the tool is only offered after a
	// server has connected at least once (mirrors Claude Code's
	// LSPManager.hasEverConnected). Set via a CompareAndSwap so it is
	// irreversible for the lifetime of the Manager.
	everConnected atomic.Bool
}

// NewManager creates an empty Manager with no server overrides.
func NewManager() *Manager {
	return &Manager{
		clients:   make(map[string]*Client),
		overrides: nil,
	}
}

// NewManagerWithOverrides creates a Manager that applies per-server overrides
// before falling back to the built-in registry.
func NewManagerWithOverrides(overrides map[string]ServerOverride) *Manager {
	return &Manager{
		clients:   make(map[string]*Client),
		overrides: overrides,
	}
}

// HasEverConnected reports whether any language server has connected on this
// manager since it was created. Mirrors Claude Code's LSPManager.hasEverConnected
// getter, which gates the LSP tool's isEnabled.
func (m *Manager) HasEverConnected() bool {
	return m.everConnected.Load()
}

// latchConnected records that a server connected for the first time. Idempotent
// and safe to call from concurrent goroutines.
func (m *Manager) latchConnected() {
	m.everConnected.CompareAndSwap(false, true)
}

// ServerFor returns the running LSP client for the given file path, launching
// the appropriate language server if it has not been started yet.
func (m *Manager) ServerFor(ctx context.Context, filePath string) (*Client, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	base := strings.ToLower(filepath.Base(filePath))

	// Determine langKey and base spec.
	var spec serverSpec
	var langKey string

	if s, ok := extToSpec[ext]; ok {
		langKey = languageKey(ext)
		spec = s
	} else if s, ok := dockerfileBasenames[base]; ok {
		langKey = "dockerfile"
		spec = s
	} else {
		return nil, fmt.Errorf("lsp: no known server for extension %q", ext)
	}

	// Apply config overrides.
	if ov, ok := m.overrides[langKey]; ok {
		if ov.Disabled {
			m.status.Store(langKey, StatusDisabled)
			return nil, fmt.Errorf("lsp: server %q is disabled by config", langKey)
		}
		if ov.Cmd != "" {
			spec.cmd = ov.Cmd
		}
		if len(ov.Args) > 0 {
			spec.args = ov.Args
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if cl, exists := m.clients[langKey]; exists {
		return cl, nil
	}

	m.status.Store(langKey, StatusConnecting)

	chosenSpec, err := resolveSpec(spec)
	if err != nil {
		m.status.Store(langKey, StatusBroken)
		return nil, fmt.Errorf("lsp: %w", err)
	}

	// Apply env overrides.
	var envVars []string
	if ov, ok := m.overrides[langKey]; ok {
		envVars = ov.Env
	}

	cl, err := newClientWithEnv(ctx, chosenSpec.cmd, chosenSpec.args, envVars)
	if err != nil {
		m.status.Store(langKey, StatusBroken)
		return nil, fmt.Errorf("lsp: start server for %q: %w", langKey, err)
	}

	m.clients[langKey] = cl
	m.status.Store(langKey, StatusConnected)
	// Record that a server has connected at least once so the LSP tool's
	// isEnabled gate (HasEverConnected) is satisfied.
	m.latchConnected()

	// Watch for unexpected server exit.
	go func() {
		<-cl.done
		m.mu.Lock()
		delete(m.clients, langKey)
		m.mu.Unlock()
		m.status.Store(langKey, StatusBroken)
	}()

	return cl, nil
}

// Status returns the current status of the language server for the given langKey.
func (m *Manager) Status(langKey string) ServerStatus {
	v, ok := m.status.Load(langKey)
	if !ok {
		return StatusUnknown
	}
	return v.(ServerStatus)
}

// Statuses returns a snapshot of all lang keys whose status is not unknown.
// The map is a copy; callers may read it freely without holding any lock.
func (m *Manager) Statuses() map[string]ServerStatus {
	out := make(map[string]ServerStatus)
	m.status.Range(func(k, v any) bool {
		if s, ok := v.(ServerStatus); ok && s != StatusUnknown {
			out[k.(string)] = s
		}
		return true
	})
	return out
}

// CloseTimeout is the maximum time to wait for LSP servers to shut down.
const CloseTimeout = 5 * time.Second

// Close shuts down all running language servers with a timeout.
func (m *Manager) Close() {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for key, cl := range m.clients {
		clients = append(clients, cl)
		delete(m.clients, key)
	}
	m.mu.Unlock()

	if len(clients) == 0 {
		return
	}

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, cl := range clients {
			wg.Add(1)
			go func(c *Client) {
				defer wg.Done()
				_ = c.Close()
			}(cl)
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(CloseTimeout):
		// Timeout: some LSP servers didn't shut down cleanly.
		// Continue anyway to avoid blocking the user.
	}
}

// WarmUp starts language servers for file types present in the workspace so the
// LSP tool's enabled gate (HasEverConnected) is satisfied as soon as possible,
// independent of any tool call. Without this the LSP tool would be permanently
// disabled: it is only offered once a server has connected, but servers only
// connect through the tool — a circular dependency. WarmUp breaks the cycle by
// starting servers at session start, mirroring Claude Code's LSP manager
// warming up servers from the workspace at session initialization.
//
// It never blocks the caller: the workspace scan is synchronous (file names
// only, no reads) but server startup runs in the background. Servers that fail
// to start (missing binary, disabled override) are silently ignored; only a
// successful connection flips the latch.
func (m *Manager) WarmUp(cwd string) {
	if cwd == "" {
		return
	}
	exts := m.scanWorkspaceExtensions(cwd)
	if len(exts) == 0 {
		return
	}
	// Start each server in the background. ServerFor guards all shared state
	// with m.mu, so concurrent calls are safe; only the first per langKey
	// actually launches a server.
	for ext := range exts {
		go func(ext string) {
			// A synthetic path carrying the target extension drives ServerFor to
			// the same langKey resolution a real file of that type would.
			_, _ = m.ServerFor(context.Background(), "warmup"+ext)
		}(ext)
	}
}

// scanWorkspaceExtensions returns the set of known file extensions present in
// the workspace, walking up to a bounded depth. It inspects only file names —
// never file contents — so it stays cheap even in large projects.
func (m *Manager) scanWorkspaceExtensions(cwd string) map[string]bool {
	exts := make(map[string]bool)
	const maxDepth = 3 // bound directory traversal to stay responsive
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > maxDepth {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				// Skip noisy directories to bound traversal.
				if name == ".git" || name == "node_modules" || name == ".venv" || name == "vendor" {
					continue
				}
				if err := walk(filepath.Join(dir, name), depth+1); err != nil {
					return err
				}
				continue
			}
			if e.Type().IsRegular() {
				if ext := strings.ToLower(filepath.Ext(name)); ext != "" {
					if _, ok := extToSpec[ext]; ok {
						exts[ext] = true
					}
				}
			}
		}
		return nil
	}
	_ = walk(cwd, 0)
	return exts
}

// languageKey maps an extension to a stable language key.
func languageKey(ext string) string {
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx":
		return "typescript"
	case ".yaml", ".yml":
		return "yaml"
	case ".sh", ".bash":
		return "bash"
	case ".cs":
		return "csharp"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".tf":
		return "terraform"
	default:
		return strings.TrimPrefix(ext, ".")
	}
}

// resolveSpec checks whether spec.cmd is on PATH, trying alternates if not.
func resolveSpec(spec serverSpec) (serverSpec, error) {
	if _, err := exec.LookPath(spec.cmd); err == nil {
		return spec, nil
	}
	if alt, ok := alternateServers[spec.cmd]; ok {
		if _, err := exec.LookPath(alt.cmd); err == nil {
			return alt, nil
		}
	}
	return serverSpec{}, fmt.Errorf("server %q not found on PATH", spec.cmd)
}

// LanguageIDForPath returns the LSP languageId string for a file path.
func LanguageIDForPath(filePath string) string {
	if strings.EqualFold(filepath.Base(filePath), "Dockerfile") {
		return "dockerfile"
	}
	return LanguageID(filepath.Ext(filePath))
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
	case ".vue":
		return "vue"
	case ".svelte":
		return "svelte"
	case ".astro":
		return "astro"
	case ".yaml", ".yml":
		return "yaml"
	case ".lua":
		return "lua"
	case ".cs":
		return "csharp"
	case ".java":
		return "java"
	case ".sh", ".bash":
		return "shellscript"
	case ".dockerfile":
		return "dockerfile"
	case ".tf":
		return "terraform"
	case ".nix":
		return "nix"
	default:
		return "plaintext"
	}
}
