package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const currentConduitConfigSchemaVersion = 1

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

	Model          string                            `json:"model,omitempty"`
	ActiveProvider *ActiveProviderSettings           `json:"activeProvider,omitempty"`
	Providers      map[string]ActiveProviderSettings `json:"providers,omitempty"`
	Roles          map[string]string                 `json:"roles,omitempty"`

	OutputStyle        string                       `json:"outputStyle,omitempty"`
	Theme              string                       `json:"theme,omitempty"`
	UsageStatusEnabled bool                         `json:"usageStatusEnabled,omitempty"`
	ThemeOverrides     map[string]string            `json:"themeOverrides,omitempty"`
	Themes             map[string]map[string]string `json:"themes,omitempty"`

	Accounts *accountStoreSettings `json:"accounts,omitempty"`
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
		return ConduitConfig{}, err
	}
	return cfg, nil
}

func SaveConduitConfig(cfg ConduitConfig) error {
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
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

func UpdateConduitConfig(fn func(*ConduitConfig)) error {
	cfg, err := LoadConduitConfig()
	if err != nil {
		return err
	}
	fn(&cfg)
	return SaveConduitConfig(cfg)
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
