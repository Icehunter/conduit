package settings

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateProviderSettings checks that a provider has enough information to be
// persisted and shown in provider selection surfaces.
func ValidateProviderSettings(p ActiveProviderSettings) error {
	switch p.Kind {
	case ProviderKindClaudeSubscription:
		if p.Model == "" {
			return fmt.Errorf("settings: claude-subscription provider requires a model")
		}
	case ProviderKindAnthropicAPI:
		if p.Model == "" {
			return fmt.Errorf("settings: anthropic-api provider requires a model")
		}
	case ProviderKindOpenAICompatible:
		if looksLikeProviderSecret(p.Credential) {
			return fmt.Errorf("settings: openai-compatible credential must be an alias, not an API key")
		}
		if p.BaseURL == "" {
			return fmt.Errorf("settings: openai-compatible provider requires a baseURL")
		}
		parsed, err := url.Parse(p.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("settings: openai-compatible provider requires an absolute baseURL")
		}
		if parsed.Scheme != "https" && parsed.Scheme != "http" {
			return fmt.Errorf("settings: openai-compatible provider baseURL must use http or https")
		}
		if p.Model == "" {
			return fmt.Errorf("settings: openai-compatible provider requires a model")
		}
	case ProviderKindMCP:
		if p.Server == "" {
			return fmt.Errorf("settings: mcp provider requires a server")
		}
	default:
		if p.Kind == "" {
			return fmt.Errorf("settings: provider kind is required")
		}
		return fmt.Errorf("settings: unsupported provider kind %q", p.Kind)
	}
	return nil
}

// ValidateProviderRegistry validates provider values and role references for
// user-facing config surfaces such as Ctrl+M and /providers.
func ValidateProviderRegistry(providers map[string]ActiveProviderSettings, roles map[string]string) []error {
	var errs []error
	for key, provider := range providers {
		if expected := ProviderKey(provider); expected != key {
			errs = append(errs, fmt.Errorf("provider %q should be keyed as %q", key, expected))
		}
		if err := ValidateProviderSettings(provider); err != nil {
			errs = append(errs, fmt.Errorf("provider %q: %w", key, err))
		}
	}
	for role, ref := range roles {
		if ref == "" {
			continue
		}
		if _, ok := providers[ref]; !ok {
			errs = append(errs, fmt.Errorf("role %q references missing provider %q", role, ref))
		}
	}
	return errs
}

func looksLikeProviderSecret(value string) bool {
	v := strings.TrimSpace(value)
	if len(v) > 48 && !strings.ContainsAny(v, " ./@") {
		return true
	}
	prefixes := []string{"sk-", "AIza", "xai-", "ghp_", "glpat-"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}
