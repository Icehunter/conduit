package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const currentConduitConfigSchemaVersion = 1

var conduitConfigMu sync.Mutex

// ConduitConfig is the typed ~/.conduit/conduit.json shape. It intentionally
// owns Conduit runtime preferences so Claude Code's settings are only an import
// source, not a live dependency after first load.
type ConduitConfig struct {
	SchemaVersion int `json:"schemaVersion,omitempty"`
	NumStartups   int `json:"numStartups,omitempty"`

	Permissions Permissions       `json:"permissions,omitempty"`
	Hooks       HooksSettings     `json:"hooks,omitempty"`
	Env         map[string]string `json:"env,omitempty"`

	Projects map[string]map[string]json.RawMessage `json:"projects,omitempty"`

	EnabledPlugins     map[string]bool `json:"enabledPlugins,omitempty"`
	OnboardingComplete bool            `json:"onboardingComplete,omitempty"`

	EnabledMcpjsonServers      []string `json:"enabledMcpjsonServers,omitempty"`
	DisabledMcpjsonServers     []string `json:"disabledMcpjsonServers,omitempty"`
	EnableAllProjectMcpServers bool     `json:"enableAllProjectMcpServers,omitempty"`

	Model                       string                            `json:"model,omitempty"`
	ActiveProvider              *ActiveProviderSettings           `json:"activeProvider,omitempty"`
	Providers                   map[string]ActiveProviderSettings `json:"providers,omitempty"`
	Roles                       map[string]string                 `json:"roles,omitempty"`
	CouncilProviders            []string                          `json:"councilProviders,omitempty"`
	CouncilMaxRounds            int                               `json:"councilMaxRounds,omitempty"`
	CouncilMemberTimeoutSec     int                               `json:"councilMemberTimeoutSec,omitempty"`
	CouncilSynthesizer          string                            `json:"councilSynthesizer,omitempty"`
	CouncilSynthesizerMaxTokens int                               `json:"councilSynthesizerMaxTokens,omitempty"`
	CouncilConvergenceThreshold float64                           `json:"councilConvergenceThreshold,omitempty"`
	CouncilRoles                map[string]string                 `json:"councilRoles,omitempty"`

	OutputStyle        string                       `json:"outputStyle,omitempty"`
	Theme              string                       `json:"theme,omitempty"`
	UsageStatusEnabled bool                         `json:"usageStatusEnabled,omitempty"`
	ThemeOverrides     map[string]string            `json:"themeOverrides,omitempty"`
	Themes             map[string]map[string]string `json:"themes,omitempty"`

	Accounts *accountStoreSettings `json:"accounts,omitempty"`

	// LSPServers maps langKey (e.g. "go", "typescript") to per-server overrides.
	LSPServers map[string]LSPServerOverride `json:"lspServers,omitempty"`
}

// LSPServerOverride holds per-language-server configuration overrides.
type LSPServerOverride struct {
	Cmd      string   `json:"cmd,omitempty"`
	Args     []string `json:"args,omitempty"`
	Env      []string `json:"env,omitempty"`
	Disabled bool     `json:"disabled,omitempty"`
}

func conduitConfigFromSettings(s Settings) ConduitConfig {
	return ConduitConfig{
		SchemaVersion: currentConduitConfigSchemaVersion,
		Permissions:   s.Permissions,
		Hooks:         s.Hooks,
		Env:           s.Env,

		EnabledPlugins:     s.EnabledPlugins,
		OnboardingComplete: s.OnboardingComplete,

		EnabledMcpjsonServers:      s.EnabledMcpjsonServers,
		DisabledMcpjsonServers:     s.DisabledMcpjsonServers,
		EnableAllProjectMcpServers: s.EnableAllProjectMcpServers,

		Model:          s.Model,
		ActiveProvider: s.ActiveProvider,
		Providers:      s.Providers,
		Roles:          s.Roles,

		OutputStyle:        s.OutputStyle,
		Theme:              s.Theme,
		UsageStatusEnabled: s.UsageStatusEnabled,
		ThemeOverrides:     s.ThemeOverrides,
		Themes:             s.Themes,
	}
}

func LoadConduitConfig() (ConduitConfig, error) {
	data, err := os.ReadFile(ConduitSettingsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ConduitConfig{}, nil
		}
		return ConduitConfig{}, err
	}
	if len(data) == 0 {
		return ConduitConfig{}, nil
	}
	var cfg ConduitConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		if decErr := dec.Decode(&cfg); decErr != nil {
			return ConduitConfig{}, err
		}
	}
	return cfg, nil
}

func SaveConduitConfig(cfg ConduitConfig) error {
	conduitConfigMu.Lock()
	defer conduitConfigMu.Unlock()
	return saveConduitConfigUnlocked(cfg)
}

func saveConduitConfigUnlocked(cfg ConduitConfig) error {
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = currentConduitConfigSchemaVersion
	}
	path := ConduitSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'))
}

func UpdateConduitConfig(fn func(*ConduitConfig)) error {
	conduitConfigMu.Lock()
	defer conduitConfigMu.Unlock()
	cfg, err := LoadConduitConfig()
	if err != nil {
		return err
	}
	fn(&cfg)
	return saveConduitConfigUnlocked(cfg)
}

func ensureConduitConfigImported() error {
	if _, err := os.Stat(ConduitSettingsPath()); err == nil {
		cfg, err := LoadConduitConfig()
		if err != nil {
			return err
		}
		if cfg.SchemaVersion == 0 {
			cfg.SchemaVersion = currentConduitConfigSchemaVersion
			return SaveConduitConfig(cfg)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s, err := readFile(UserSettingsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	cfg := conduitConfigFromSettings(*s)
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = currentConduitConfigSchemaVersion
	}
	return SaveConduitConfig(cfg)
}

func SaveConduitModel(model string) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		cfg.Model = model
	})
}

func SaveConduitOutputStyle(name string) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		cfg.OutputStyle = name
	})
}

func SaveConduitTheme(name string) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		cfg.Theme = name
	})
}

func SaveConduitUsageStatusEnabled(on bool) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		cfg.UsageStatusEnabled = on
	})
}

func SaveConduitOnboardingComplete(done bool) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		cfg.OnboardingComplete = done
	})
}

func SaveConduitEnabledPlugin(pluginID string, enabled bool) error {
	if pluginID == "" {
		return fmt.Errorf("settings: plugin id required")
	}
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		if cfg.EnabledPlugins == nil {
			cfg.EnabledPlugins = map[string]bool{}
		}
		cfg.EnabledPlugins[pluginID] = enabled
	})
}
