package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
)

// Scope identifies which config file an operation targets.
//   - "project" — <cwd>/.mcp.json (shared via VCS)
//   - "user"    — ~/.conduit/mcp.json (this user, all projects)
//   - "local"   — ~/.conduit/conduit.json projects[abs-cwd].mcpServers (private to this checkout)
const (
	ScopeProject = "project"
	ScopeUser    = "user"
	ScopeLocal   = "local"
)

// ErrServerNotFound is returned by RemoveServer when the named server isn't
// present in any writable scope (or the requested scope when one is given).
var ErrServerNotFound = errors.New("mcp: server not found")

// ErrServerExists is returned by AddServer when a server with the same name
// is already configured in the target scope.
var ErrServerExists = errors.New("mcp: server already exists in scope")

// ConfiguredServer is one row from ListConfiguredServers — the on-disk view
// (whether the manager has connected to it or not).
type ConfiguredServer struct {
	Name   string
	Scope  string
	Source string // file path the entry lives in
	Config ServerConfig
}

// AddServer writes cfg under name to the file backing scope.
//
// If a server with that name already exists in the target scope, returns
// ErrServerExists — callers can wrap with --force semantics if needed.
// Sibling fields in the JSON file (settings the user added by hand) are
// preserved via map[string]json.RawMessage round-trip.
func AddServer(name string, cfg ServerConfig, scope, cwd string) error {
	if name == "" {
		return errors.New("mcp: server name is required")
	}
	switch scope {
	case ScopeProject, ScopeUser, ScopeLocal:
	default:
		return fmt.Errorf("mcp: unknown scope %q (want project|user|local)", scope)
	}
	if (scope == ScopeProject || scope == ScopeLocal) && cwd == "" {
		return fmt.Errorf("mcp: scope %q requires a working directory", scope)
	}
	// Strip the not-serialized fields — they're set by LoadConfigs at read time.
	cfg.Source = ""
	cfg.Scope = ""
	cfg.PluginName = ""

	switch scope {
	case ScopeProject:
		return addToMcpJSON(filepath.Join(cwd, ".mcp.json"), name, cfg, false)
	case ScopeUser:
		return addToMcpJSON(conduitMCPFile(), name, cfg, false)
	case ScopeLocal:
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("mcp: encode server %q: %w", name, err)
		}
		err = settings.SaveConduitProjectMCPServerRaw(cwd, name, encoded, false)
		if errors.Is(err, settings.ErrLocalServerExists) {
			return ErrServerExists
		}
		return err
	}
	return nil
}

// RemoveServer deletes name from the file backing scope. If scope is "" it
// searches all writable scopes and removes the first match. Returns the
// scope it was actually removed from (useful for confirmation messages).
func RemoveServer(name, scope, cwd string) (removedFrom string, err error) {
	if name == "" {
		return "", errors.New("mcp: server name is required")
	}
	tryScopes := []string{scope}
	if scope == "" {
		// Default search order: project (closest to user intent) → local → user.
		tryScopes = []string{ScopeProject, ScopeLocal, ScopeUser}
	}
	for _, s := range tryScopes {
		var rmErr error
		switch s {
		case ScopeProject:
			if cwd == "" {
				continue
			}
			rmErr = removeFromMcpJSON(filepath.Join(cwd, ".mcp.json"), name)
		case ScopeUser:
			rmErr = removeFromMcpJSON(conduitMCPFile(), name)
		case ScopeLocal:
			if cwd == "" {
				continue
			}
			rmErr = settings.RemoveConduitProjectMCPServer(cwd, name)
			if errors.Is(rmErr, settings.ErrLocalServerNotFound) {
				rmErr = ErrServerNotFound
			}
		default:
			return "", fmt.Errorf("mcp: unknown scope %q", s)
		}
		if rmErr == nil {
			return s, nil
		}
		if !errors.Is(rmErr, ErrServerNotFound) && scope != "" {
			return "", rmErr
		}
	}
	return "", ErrServerNotFound
}

// ListConfiguredServers walks every writable scope and returns the on-disk
// servers. Unlike Manager.Servers(), this works even when no manager has
// been started yet (so the CLI can `conduit mcp list` without booting).
//
// Order: project → local → user (closest-scope first).
func ListConfiguredServers(cwd string) ([]ConfiguredServer, error) {
	var out []ConfiguredServer

	if cwd != "" {
		path := filepath.Join(cwd, ".mcp.json")
		if cfg, err := loadMcpFile(path); err == nil {
			for name, srv := range cfg.McpServers {
				srv.Source = path
				srv.Scope = ScopeProject
				out = append(out, ConfiguredServer{
					Name: name, Scope: ScopeProject, Source: path, Config: srv,
				})
			}
		}
		// local: ~/.conduit/conduit.json projects[cwd].mcpServers
		localRaw, localPath, err := settings.LoadConduitProjectMCPServersRaw(cwd)
		if err != nil {
			return nil, err
		}
		for name, encoded := range localRaw {
			var srv ServerConfig
			if err := json.Unmarshal(encoded, &srv); err != nil {
				return nil, fmt.Errorf("mcp: decode local server %q: %w", name, err)
			}
			srv.Source = localPath
			srv.Scope = ScopeLocal
			out = append(out, ConfiguredServer{
				Name: name, Scope: ScopeLocal, Source: localPath, Config: srv,
			})
		}
	}
	userPath := conduitMCPFile()
	if cfg, err := loadMcpFile(userPath); err == nil {
		for name, srv := range cfg.McpServers {
			srv.Source = userPath
			srv.Scope = ScopeUser
			out = append(out, ConfiguredServer{
				Name: name, Scope: ScopeUser, Source: userPath, Config: srv,
			})
		}
	}
	return out, nil
}

// addToMcpJSON adds an entry to a {"mcpServers": {...}} file at path,
// preserving any sibling top-level keys.
func addToMcpJSON(path, name string, cfg ServerConfig, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mcp: prepare %s: %w", path, err)
	}
	raw, err := readRawJSONObject(path)
	if err != nil {
		return fmt.Errorf("mcp: read %s: %w", path, err)
	}
	servers := map[string]json.RawMessage{}
	if existing, ok := raw["mcpServers"]; ok && len(existing) > 0 {
		if err := json.Unmarshal(existing, &servers); err != nil {
			return fmt.Errorf("mcp: parse mcpServers in %s: %w", path, err)
		}
	}
	if _, exists := servers[name]; exists && !overwrite {
		return ErrServerExists
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("mcp: encode server %q: %w", name, err)
	}
	servers[name] = encoded

	serversRaw, err := json.Marshal(servers)
	if err != nil {
		return fmt.Errorf("mcp: encode mcpServers: %w", err)
	}
	raw["mcpServers"] = serversRaw

	return writeJSONFileAtomic(path, raw)
}

// removeFromMcpJSON deletes name from {"mcpServers": {...}} in path.
// Returns ErrServerNotFound when the file or the key is absent.
func removeFromMcpJSON(path, name string) error {
	raw, err := readRawJSONObject(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrServerNotFound
		}
		return fmt.Errorf("mcp: read %s: %w", path, err)
	}
	servers := map[string]json.RawMessage{}
	if existing, ok := raw["mcpServers"]; ok && len(existing) > 0 {
		if err := json.Unmarshal(existing, &servers); err != nil {
			return fmt.Errorf("mcp: parse mcpServers in %s: %w", path, err)
		}
	}
	if _, ok := servers[name]; !ok {
		return ErrServerNotFound
	}
	delete(servers, name)

	if len(servers) == 0 {
		// Drop the key entirely if no servers remain — keeps the file tidy.
		delete(raw, "mcpServers")
	} else {
		serversRaw, err := json.Marshal(servers)
		if err != nil {
			return fmt.Errorf("mcp: encode mcpServers: %w", err)
		}
		raw["mcpServers"] = serversRaw
	}
	return writeJSONFileAtomic(path, raw)
}

// readRawJSONObject reads a JSON object as a map of raw values, preserving
// fields we don't care about. Returns an empty map (not error) if the file
// doesn't exist; callers can pass that map straight into a write.
func readRawJSONObject(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// writeJSONFileAtomic marshals raw with stable indentation and writes it
// via temp-file + rename so a crash mid-write can't corrupt the config.
func writeJSONFileAtomic(path string, raw map[string]json.RawMessage) error {
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("mcp: encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mcp: prepare %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("mcp: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("mcp: write %s: %w", path, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("mcp: chmod %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("mcp: close %s: %w", path, err)
	}
	return os.Rename(tmpName, path)
}

// DetectTransport returns "http" if endpoint looks like a URL, else "stdio".
// SSE is never auto-detected — Claude treats it as opt-in via --transport.
func DetectTransport(endpoint string) string {
	lower := strings.ToLower(endpoint)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return "http"
	}
	return "stdio"
}

// NormalizeTransport accepts both "http" and the spec name "streamable-http"
// (which Claude also accepts). Other values pass through.
func NormalizeTransport(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "streamable-http", "http":
		return "http"
	case "sse":
		return "sse"
	case "stdio", "":
		return "stdio"
	}
	return t
}
