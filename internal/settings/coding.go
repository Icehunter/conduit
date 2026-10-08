package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
)

// VerificationCheck describes one repository command. Runtime validation and
// timeout defaults belong to the verification runner.
type VerificationCheck struct {
	ID             string `json:"id"`
	Command        string `json:"command"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// DefinitionOfDoneSettings is opt-in and replaced as a whole by a later layer.
type DefinitionOfDoneSettings struct {
	Enabled             bool                `json:"enabled"`
	Checks              []VerificationCheck `json:"checks,omitempty"`
	MaxRepairAttempts   int                 `json:"maxRepairAttempts,omitempty"`
	TotalTimeoutSeconds int                 `json:"totalTimeoutSeconds,omitempty"`
	ProtectedPaths      []string            `json:"protectedPaths,omitempty"`
	RequireReview       bool                `json:"requireReview,omitempty"`
}

// EpisodeSettings governs the independent coding episode recorder.
type EpisodeSettings struct {
	Enabled        bool     `json:"enabled"`
	IncludeContent bool     `json:"includeContent,omitempty"`
	AllowedPaths   []string `json:"allowedPaths,omitempty"`
	ExcludedPaths  []string `json:"excludedPaths,omitempty"`
	RetentionDays  int      `json:"retentionDays,omitempty"`
}

// ProviderPoolSettings references existing providers; omitted capacities use one.
// Pool scheduling is separate from providerChains error failover.
type ProviderPoolSettings struct {
	Members       []string       `json:"members"`
	MaxConcurrent map[string]int `json:"maxConcurrent,omitempty"`
}

// CodingPolicies is the strict runtime policy snapshot.
type CodingPolicies struct {
	DefinitionOfDone *DefinitionOfDoneSettings
	Episodes         *EpisodeSettings
	ProviderPools    map[string]ProviderPoolSettings
	RolePools        map[string]string
}

// LoadCodingPolicies strictly reads the same layers as Load. Unlike legacy
// settings loading, malformed existing files and unknown policy keys are errors:
// an invalid enabled policy must never silently disable completion enforcement.
func LoadCodingPolicies(cwd string) (CodingPolicies, error) {
	providers := map[string]ActiveProviderSettings{}
	p := CodingPolicies{ProviderPools: map[string]ProviderPoolSettings{}, RolePools: map[string]string{}}
	for _, path := range settingsFiles(cwd) {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return p, fmt.Errorf("coding policy %s: %w", path, err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return p, fmt.Errorf("coding policy %s: %w", path, err)
		}
		if raw == nil {
			return p, fmt.Errorf("coding policy %s: expected settings object", path)
		}
		if value, ok := raw["providers"]; ok {
			var layer map[string]ActiveProviderSettings
			if err := json.Unmarshal(value, &layer); err != nil {
				return p, fmt.Errorf("coding policy %s providers: %w", path, err)
			}
			maps.Copy(providers, layer)
		}
		// Fresh objects preserve whole-object replacement across layers.
		if value, ok := raw["definitionOfDone"]; ok {
			var next DefinitionOfDoneSettings
			if err := decodePolicy(value, &next); err != nil {
				return p, fmt.Errorf("coding policy %s definitionOfDone: %w", path, err)
			}
			p.DefinitionOfDone = &next
		}
		if value, ok := raw["episodes"]; ok {
			var next EpisodeSettings
			if err := decodePolicy(value, &next); err != nil {
				return p, fmt.Errorf("coding policy %s episodes: %w", path, err)
			}
			p.Episodes = &next
		}
		if value, ok := raw["providerPools"]; ok {
			var pools map[string]ProviderPoolSettings
			if err := decodePolicy(value, &pools); err != nil {
				return p, fmt.Errorf("coding policy %s providerPools: %w", path, err)
			}
			maps.Copy(p.ProviderPools, pools)
		}
		if value, ok := raw["rolePools"]; ok {
			var roles map[string]string
			if err := decodePolicy(value, &roles); err != nil {
				return p, fmt.Errorf("coding policy %s rolePools: %w", path, err)
			}
			maps.Copy(p.RolePools, roles)
		}
	}
	canonicalizePoolRefs(providers, p.ProviderPools)
	return p, nil
}

func decodePolicy(raw []byte, target any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("policy must be an object, not null")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("expected exactly one policy object")
	}
	return nil
}

// ValidateProviderPools checks pool references before any request is scheduled.
func ValidateProviderPools(providers map[string]ActiveProviderSettings, pools map[string]ProviderPoolSettings, roles map[string]string) error {
	capacities := map[string]int{}
	for name, pool := range pools {
		if name == "" || len(pool.Members) == 0 {
			return fmt.Errorf("provider pool %q requires members", name)
		}
		seen := map[string]bool{}
		for _, ref := range pool.Members {
			if seen[ref] {
				return fmt.Errorf("provider pool %q repeats %q", name, ref)
			}
			seen[ref] = true
			provider, ok := providers[ref]
			if !ok {
				return fmt.Errorf("provider pool %q references missing provider %q", name, ref)
			}
			if err := ValidateProviderSettings(provider); err != nil {
				return fmt.Errorf("provider pool %q: %w", name, err)
			}
			n, explicit := pool.MaxConcurrent[ref]
			if explicit && n <= 0 {
				return fmt.Errorf("provider pool %q capacity for %q must be positive", name, ref)
			}
			if !explicit {
				n = 1
			}
			resource := provider.EndpointID
			if resource == "" {
				resource = ref
			}
			if previous, ok := capacities[resource]; ok && previous != n {
				return fmt.Errorf("provider pool %q has conflicting capacities for endpoint %q", name, resource)
			}
			capacities[resource] = n
		}
		for ref := range pool.MaxConcurrent {
			if !seen[ref] {
				return fmt.Errorf("provider pool %q capacity references non-member %q", name, ref)
			}
		}
	}
	for role, pool := range roles {
		if _, ok := pools[pool]; !ok {
			return fmt.Errorf("role %q references missing provider pool %q", role, pool)
		}
	}
	return nil
}

// validateCodingFields protects typed settings migrations from silently dropping
// malformed policy keys before the strict runtime loader can inspect them.
func validateCodingFields(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, field := range []struct {
		name   string
		target any
	}{
		{"definitionOfDone", new(DefinitionOfDoneSettings)}, {"episodes", new(EpisodeSettings)},
		{"providerPools", new(map[string]ProviderPoolSettings)}, {"rolePools", new(map[string]string)},
	} {
		if value, ok := raw[field.name]; ok {
			if err := decodePolicy(value, field.target); err != nil {
				return fmt.Errorf("%s: %w", field.name, err)
			}
		}
	}
	return nil
}

func canonicalizePoolRefs(providers map[string]ActiveProviderSettings, pools map[string]ProviderPoolSettings) bool {
	changed := false
	resolve := func(ref string) string {
		if p, ok := providers[ref]; ok {
			return ProviderKey(p)
		}
		return ref
	}
	for name, pool := range pools {
		members := make([]string, len(pool.Members))
		for i, ref := range pool.Members {
			members[i] = resolve(ref)
			changed = changed || members[i] != ref
		}
		capacities := make(map[string]int, len(pool.MaxConcurrent))
		for ref, n := range pool.MaxConcurrent {
			next := resolve(ref)
			capacities[next] = n
			changed = changed || next != ref
		}
		pool.Members = members
		pool.MaxConcurrent = capacities
		pools[name] = pool
	}
	return changed
}
