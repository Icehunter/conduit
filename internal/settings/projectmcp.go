package settings

import (
	"encoding/json"
	"fmt"
)

// LoadConduitProjectMCPServersRaw returns the raw JSON for each MCP server
// stored under projects[cwd].mcpServers in conduit.json, plus the file path
// it was read from. Empty map (not error) when the file or section is absent.
//
// Returned as raw JSON to keep the settings package free of an mcp import —
// callers in internal/mcp re-decode into ServerConfig.
func LoadConduitProjectMCPServersRaw(cwd string) (map[string]json.RawMessage, string, error) {
	path := ConduitSettingsPath()
	raw, err := readRawObject(path)
	if err != nil {
		return nil, path, err
	}
	projects := map[string]json.RawMessage{}
	if rawProjects, ok := raw["projects"]; ok && len(rawProjects) > 0 {
		if err := json.Unmarshal(rawProjects, &projects); err != nil {
			return nil, path, fmt.Errorf("settings: parse conduit projects: %w", err)
		}
	}
	var projectRaw json.RawMessage
	for _, key := range projectPathCandidates(cwd) {
		if entry, ok := projects[key]; ok && len(entry) > 0 {
			projectRaw = entry
			break
		}
	}
	out := map[string]json.RawMessage{}
	if len(projectRaw) == 0 {
		return out, path, nil
	}
	project := map[string]json.RawMessage{}
	if err := json.Unmarshal(projectRaw, &project); err != nil {
		return nil, path, fmt.Errorf("settings: parse conduit project %q: %w", cwd, err)
	}
	if servers, ok := project["mcpServers"]; ok && len(servers) > 0 {
		if err := json.Unmarshal(servers, &out); err != nil {
			return nil, path, fmt.Errorf("settings: parse mcpServers in project %q: %w", cwd, err)
		}
	}
	return out, path, nil
}

// SaveConduitProjectMCPServerRaw writes encoded under
// projects[cwd].mcpServers[name] in conduit.json. When overwrite is false
// and the entry already exists, returns ErrLocalServerExists.
func SaveConduitProjectMCPServerRaw(cwd, name string, encoded json.RawMessage, overwrite bool) error {
	if name == "" {
		return fmt.Errorf("settings: server name is required")
	}
	return updateConduitProjectRaw(cwd, func(project map[string]json.RawMessage) error {
		servers := map[string]json.RawMessage{}
		if existing, ok := project["mcpServers"]; ok && len(existing) > 0 {
			if err := json.Unmarshal(existing, &servers); err != nil {
				return fmt.Errorf("settings: parse mcpServers: %w", err)
			}
		}
		if _, exists := servers[name]; exists && !overwrite {
			return ErrLocalServerExists
		}
		servers[name] = encoded
		raw, err := json.Marshal(servers)
		if err != nil {
			return fmt.Errorf("settings: encode mcpServers: %w", err)
		}
		project["mcpServers"] = raw
		return nil
	})
}

// RemoveConduitProjectMCPServer deletes name from the local-scope mcpServers
// map. Returns ErrLocalServerNotFound when the entry is absent.
func RemoveConduitProjectMCPServer(cwd, name string) error {
	if name == "" {
		return fmt.Errorf("settings: server name is required")
	}
	return updateConduitProjectRaw(cwd, func(project map[string]json.RawMessage) error {
		servers := map[string]json.RawMessage{}
		if existing, ok := project["mcpServers"]; ok && len(existing) > 0 {
			if err := json.Unmarshal(existing, &servers); err != nil {
				return fmt.Errorf("settings: parse mcpServers: %w", err)
			}
		}
		if _, ok := servers[name]; !ok {
			return ErrLocalServerNotFound
		}
		delete(servers, name)
		if len(servers) == 0 {
			delete(project, "mcpServers")
			return nil
		}
		raw, err := json.Marshal(servers)
		if err != nil {
			return fmt.Errorf("settings: encode mcpServers: %w", err)
		}
		project["mcpServers"] = raw
		return nil
	})
}

// ErrLocalServerExists / ErrLocalServerNotFound are package-level sentinels
// the mcp package wraps into its own ErrServerExists / ErrServerNotFound.
var (
	ErrLocalServerExists   = fmt.Errorf("settings: local mcp server already exists")
	ErrLocalServerNotFound = fmt.Errorf("settings: local mcp server not found")
)
