package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ConduitProjectState is conduit's per-workspace state stored under
// conduit.json projects[abs-cwd]. It intentionally mirrors the Claude global
// project object where compatibility is useful, but writes only to Conduit.
type ConduitProjectState struct {
	HasTrustDialogAccepted     bool
	EnabledMcpjsonServers      []string
	DisabledMcpjsonServers     []string
	EnableAllProjectMcpServers bool
	DisabledMcpServers         []string

	DisabledMcpServersPresent bool
}

func normalizedProjectPath(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return cwd
	}
	return abs
}

func projectPathCandidates(cwd string) []string {
	key := normalizedProjectPath(cwd)
	candidates := []string{key}
	if slash := filepath.ToSlash(key); slash != key {
		candidates = append(candidates, slash)
	}
	return candidates
}

func LoadConduitProjectState(cwd string) (ConduitProjectState, bool, error) {
	raw, err := readRawObject(ConduitSettingsPath())
	if err != nil {
		return ConduitProjectState{}, false, err
	}
	projects := map[string]json.RawMessage{}
	if rawProjects, ok := raw["projects"]; ok && len(rawProjects) > 0 {
		if err := json.Unmarshal(rawProjects, &projects); err != nil {
			return ConduitProjectState{}, false, fmt.Errorf("settings: parse conduit projects: %w", err)
		}
	}
	var (
		projectRaw json.RawMessage
		ok         bool
	)
	for _, key := range projectPathCandidates(cwd) {
		projectRaw, ok = projects[key]
		if ok && len(projectRaw) > 0 {
			break
		}
	}
	if !ok || len(projectRaw) == 0 {
		return ConduitProjectState{}, false, nil
	}
	project := map[string]json.RawMessage{}
	if err := json.Unmarshal(projectRaw, &project); err != nil {
		return ConduitProjectState{}, false, fmt.Errorf("settings: parse conduit project %q: %w", cwd, err)
	}
	state := ConduitProjectState{}
	if rawTrusted, ok := project["hasTrustDialogAccepted"]; ok {
		_ = json.Unmarshal(rawTrusted, &state.HasTrustDialogAccepted)
	}
	state.EnabledMcpjsonServers = decodeStringList(project["enabledMcpjsonServers"])
	state.DisabledMcpjsonServers = decodeStringList(project["disabledMcpjsonServers"])
	if rawEnableAll, ok := project["enableAllProjectMcpServers"]; ok {
		_ = json.Unmarshal(rawEnableAll, &state.EnableAllProjectMcpServers)
	}
	if rawDisabled, ok := project["disabledMcpServers"]; ok {
		state.DisabledMcpServersPresent = true
		state.DisabledMcpServers = decodeStringList(rawDisabled)
	}
	return state, true, nil
}

func updateConduitProjectRaw(cwd string, fn func(map[string]json.RawMessage) error) error {
	conduitConfigMu.Lock()
	defer conduitConfigMu.Unlock()
	path := ConduitSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := readRawObject(path)
	if err != nil {
		return err
	}
	projects := map[string]json.RawMessage{}
	if rawProjects, ok := raw["projects"]; ok && len(rawProjects) > 0 {
		if err := json.Unmarshal(rawProjects, &projects); err != nil {
			return fmt.Errorf("settings: parse conduit projects: %w", err)
		}
	}
	key := normalizedProjectPath(cwd)
	project := map[string]json.RawMessage{}
	foundKey := key
	for _, candidate := range projectPathCandidates(cwd) {
		if rawProject, ok := projects[candidate]; ok && len(rawProject) > 0 {
			foundKey = candidate
			if err := json.Unmarshal(rawProject, &project); err != nil {
				return fmt.Errorf("settings: parse conduit project %q: %w", candidate, err)
			}
			break
		}
	}
	if err := fn(project); err != nil {
		return err
	}
	projectRaw, err := json.Marshal(project)
	if err != nil {
		return err
	}
	projects[key] = projectRaw
	if foundKey != key {
		delete(projects, foundKey)
	}
	projectsRaw, err := json.Marshal(projects)
	if err != nil {
		return err
	}
	raw["projects"] = projectsRaw
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'))
}

func SetConduitProjectTrusted(cwd string) error {
	return updateConduitProjectRaw(cwd, func(project map[string]json.RawMessage) error {
		project["hasTrustDialogAccepted"] = json.RawMessage("true")
		return nil
	})
}

func SaveConduitProjectMcpjsonApproval(cwd, name, choice string) error {
	return updateConduitProjectRaw(cwd, func(project map[string]json.RawMessage) error {
		enabled := decodeStringList(project["enabledMcpjsonServers"])
		disabled := decodeStringList(project["disabledMcpjsonServers"])
		switch choice {
		case "yes", "yes_all":
			enabled = appendUnique(enabled, name)
			disabled = removeFrom(disabled, name)
			if choice == "yes_all" {
				project["enableAllProjectMcpServers"] = json.RawMessage("true")
			}
		case "no":
			disabled = appendUnique(disabled, name)
			enabled = removeFrom(enabled, name)
		default:
			return fmt.Errorf("ApproveMcpjsonServer: unknown choice %q", choice)
		}
		if b, err := json.Marshal(enabled); err == nil {
			project["enabledMcpjsonServers"] = b
		}
		if b, err := json.Marshal(disabled); err == nil {
			project["disabledMcpjsonServers"] = b
		}
		return nil
	})
}

func SetConduitProjectMCPDisabled(cwd, name string, disabled bool) error {
	return updateConduitProjectRaw(cwd, func(project map[string]json.RawMessage) error {
		disabledServers := decodeStringList(project["disabledMcpServers"])
		if disabled {
			disabledServers = appendUnique(disabledServers, name)
		} else {
			disabledServers = removeFrom(disabledServers, name)
		}
		if disabledServers == nil {
			disabledServers = []string{}
		}
		raw, err := json.Marshal(disabledServers)
		if err != nil {
			return err
		}
		project["disabledMcpServers"] = raw
		return nil
	})
}
