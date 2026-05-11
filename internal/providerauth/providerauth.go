// Package providerauth defines the interface and built-in implementations for
// per-provider credential management. It covers API-key flows for catalog
// providers such as OpenAI, Gemini, and OpenRouter, and Account-based
// flows for product providers like GitHub Copilot and ChatGPT Plus/Pro.
package providerauth

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrMissingCredential is returned when a provider has no stored credential.
var ErrMissingCredential = errors.New("providerauth: no credential stored for provider")

// MethodKind identifies the auth mechanism.
const (
	MethodAPIKey = "api-key"
	MethodOAuth  = "oauth"
)

// Method describes one supported auth approach for a provider.
type Method struct {
	Kind   string // MethodAPIKey or MethodOAuth
	Label  string // user-facing label, e.g. "Enter API key"
	EnvVar string // optional: env var that supplies the key automatically
	Hint   string // placeholder / hint text shown in the input field
}

// Config describes a provider and its supported auth methods.
type Config struct {
	ID          string
	DisplayName string
	Methods     []Method
	DocsURL     string
}

// ProviderAccount represents non-secret metadata for a product-account provider.
type ProviderAccount struct {
	ID          string    `json:"id"`
	ProviderID  string    `json:"provider_id"`
	DisplayName string    `json:"display_name"`
	Method      string    `json:"method"`
	AddedAt     time.Time `json:"added_at"`
	Active      bool      `json:"active"`
}

// ProviderCredential represents secret material stored in secure storage.
type ProviderCredential struct {
	AccessToken  string            `json:"access_token"`
	RefreshToken string            `json:"refresh_token,omitempty"`
	Expiry       time.Time         `json:"expiry,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// Authorizer can issue and store credentials for one provider.
type Authorizer interface {
	ProviderID() string
	Methods() []Method
	// Authorize stores a credential of the given method kind.
	// params carries method-specific inputs, e.g. {"key": "sk-..."}.
	Authorize(ctx context.Context, kind string, params map[string]string) (credential string, err error)
	// Validate checks whether credential is syntactically and minimally valid.
	// It does not make a network call.
	Validate(ctx context.Context, credential string) error
}

// AccountAuthorizer is an extended interface for providers that use account-based OAuth.
type AccountAuthorizer interface {
	Authorizer
	// GetAccount returns current account metadata.
	GetAccount(ctx context.Context) (*ProviderAccount, error)
	// Refresh refreshes the access token if needed.
	Refresh(ctx context.Context) error
}

// looksLikeAPIKey is a conservative check that the string could be an API key.
func looksLikeAPIKey(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return false
	}
	if strings.ContainsAny(s, " \t\n\r") {
		return false
	}
	return true
}
