// MCP OAuth 2.0 + PKCE flow for HTTP/SSE servers that gate tool calls
// behind a bearer token. Mirrors src/services/mcp/auth.ts (which leans on
// the official @modelcontextprotocol/sdk; we implement the spec directly).
//
// The flow conduit performs:
//
//  1. Discover authorization server metadata (RFC 8414) by GETting
//     `<server-base>/.well-known/oauth-authorization-server`. Falls back to
//     `<server-base>/.well-known/openid-configuration` for AS that ship the
//     OIDC discovery doc only.
//
//  2. If the metadata advertises a registration_endpoint and we don't have
//     a client_id cached, perform Dynamic Client Registration (RFC 7591).
//     Many MCPs do require DCR — they don't pre-register clients.
//
//  3. Generate PKCE verifier + challenge (S256), generate state.
//
//  4. Bind a localhost callback listener and open the user's browser at
//     `<authorization_endpoint>?response_type=code&...&code_challenge=...`.
//
//  5. Receive the redirect with `code` + `state`, validate state.
//
//  6. Exchange code at `<token_endpoint>` for {access_token, refresh_token,
//     expires_in}.
//
//  7. Persist tokens in secure storage under a per-server key.
//
// On a subsequent 401, RefreshServerToken() exchanges the refresh_token for
// a fresh access_token without prompting the user again.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthServerMetadata is the subset of RFC 8414 / RFC 8615 metadata fields
// we use. JSON tags match the spec.
type AuthServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
}

// ClientRegistration is the response from a Dynamic Client Registration call
// (RFC 7591 §3.2.1). We store the client_id (and secret if any) per-server so
// subsequent token refreshes don't re-register.
type ClientRegistration struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at,omitempty"`
	RedirectURIs            []string `json:"redirect_uris,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// OAuthTokens is the bundle persisted per-server. ExpiresAt is computed from
// the response's expires_in at issuance time so we don't need a server clock.
type OAuthTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope,omitempty"`
	// Embedded so token refresh can re-auth without another DCR roundtrip.
	Client        ClientRegistration `json:"client"`
	TokenEndpoint string             `json:"token_endpoint"`
}

// Redact prevents accidental logging of bearer tokens.
func (OAuthTokens) String() string { return "<redacted mcp tokens>" }

// tokenResponse is the shape of token_endpoint POST responses.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// oauthHTTPClient is the default client used for OAuth requests. Tests override.
var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second}

// ProtectedResourceMetadata is the subset of RFC 9728 fields we use. The
// MCP spec requires servers that gate tools behind OAuth to expose this
// document so clients can find the right authorization server (which may
// be a different origin from the MCP server itself — e.g. GitHub
// Copilot's MCP at api.githubcopilot.com points at github.com as its AS).
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
}

// DiscoverProtectedResource probes the MCP server for an RFC 9728
// `/.well-known/oauth-protected-resource` document and returns the first
// authorization_servers entry. Mirrors what CC's MCP SDK does on a 401.
//
// Returns ("", nil) when the server doesn't expose this document — the
// caller can then fall through to the legacy "discover at server origin"
// path for MCPs where the server and the AS share an origin.
func DiscoverProtectedResource(ctx context.Context, serverURL string) (string, error) {
	base, err := originOf(serverURL)
	if err != nil {
		return "", err
	}
	url := base + "/.well-known/oauth-protected-resource"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mcp oauth: protected-resource at %s returned %d", url, resp.StatusCode)
	}
	var prm ProtectedResourceMetadata
	if err := json.NewDecoder(resp.Body).Decode(&prm); err != nil {
		return "", fmt.Errorf("mcp oauth: decode protected-resource: %w", err)
	}
	if len(prm.AuthorizationServers) == 0 {
		return "", nil
	}
	return prm.AuthorizationServers[0], nil
}

// DiscoverAuthServer fetches the authorization server metadata for the
// MCP server at serverURL.
//
// Discovery order (matches CC's MCP SDK):
//
//  1. RFC 9728 protected-resource document at
//     `<server-origin>/.well-known/oauth-protected-resource`. If present,
//     it points at the real authorization server origin (which may be
//     different from the MCP server's own origin — e.g. github.com as
//     the AS for an MCP at api.githubcopilot.com).
//
//  2. RFC 8414 metadata at the (resolved) AS origin's
//     `/.well-known/oauth-authorization-server`.
//
//  3. OIDC discovery at the (resolved) AS origin's
//     `/.well-known/openid-configuration`.
//
// If protected-resource discovery fails or returns no AS, we fall back to
// treating the MCP server's own origin as the AS — that's the right
// behavior for MCPs where the two are colocated.
func DiscoverAuthServer(ctx context.Context, serverURL string) (*AuthServerMetadata, error) {
	asOrigin := ""
	if as, err := DiscoverProtectedResource(ctx, serverURL); err == nil && as != "" {
		// Got a pointer to the real AS — use its origin for the well-known.
		if origin, err := originOf(as); err == nil {
			asOrigin = origin
		}
	}
	if asOrigin == "" {
		// No protected-resource doc — fall back to "MCP and AS share origin".
		o, err := originOf(serverURL)
		if err != nil {
			return nil, err
		}
		asOrigin = o
	}

	candidates := []string{
		asOrigin + "/.well-known/oauth-authorization-server",
		asOrigin + "/.well-known/openid-configuration",
	}
	var statuses []string
	for _, u := range candidates {
		md, err := fetchMetadata(ctx, u)
		if err == nil {
			return md, nil
		}
		statuses = append(statuses, u+": "+err.Error())
	}
	return nil, fmt.Errorf("mcp oauth: no OAuth metadata at %s — server does not expose RFC 8414/OIDC discovery (%s)",
		asOrigin, strings.Join(statuses, "; "))
}

func originOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("mcp oauth: parse server URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("mcp oauth: server URL must be absolute, got %q", rawURL)
	}
	return u.Scheme + "://" + u.Host, nil
}

func fetchMetadata(ctx context.Context, url string) (*AuthServerMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp oauth: %s returned %d", url, resp.StatusCode)
	}
	var md AuthServerMetadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		return nil, fmt.Errorf("mcp oauth: decode metadata: %w", err)
	}
	if md.AuthorizationEndpoint == "" || md.TokenEndpoint == "" {
		return nil, errors.New("mcp oauth: metadata missing required endpoints")
	}
	return &md, nil
}
