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

	"github.com/icehunter/conduit/internal/auth"
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

// RegisterClient performs Dynamic Client Registration (RFC 7591). Called
// when AuthServerMetadata.RegistrationEndpoint is set and we don't already
// have a cached client_id. clientName/redirectURI must be supplied.
func RegisterClient(ctx context.Context, registrationEndpoint, clientName string, redirectURIs []string) (*ClientRegistration, error) {
	body := map[string]any{
		"client_name":                clientName,
		"redirect_uris":              redirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none", // public client
		"application_type":           "native",
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, strings.NewReader(string(buf)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp oauth: registration request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("mcp oauth: registration returned %d", resp.StatusCode)
	}
	var reg ClientRegistration
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		return nil, fmt.Errorf("mcp oauth: decode registration: %w", err)
	}
	if reg.ClientID == "" {
		return nil, errors.New("mcp oauth: registration response missing client_id")
	}
	return &reg, nil
}

// AuthorizeURL builds the URL the user opens in their browser. State and
// codeChallenge are provided by the caller (the listener side already has
// them). scopes may be empty — many MCPs use the metadata's
// scopes_supported, others ignore the parameter entirely.
func AuthorizeURL(metadata *AuthServerMetadata, clientID, redirectURI, state, codeChallenge string, scopes []string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	sep := "?"
	if strings.Contains(metadata.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return metadata.AuthorizationEndpoint + sep + q.Encode()
}

// ExchangeCode trades an authorization code for tokens at token_endpoint.
// codeVerifier is the original PKCE verifier (the AS will hash it and
// compare against the challenge it received in the authorize request).
func ExchangeCode(ctx context.Context, tokenEndpoint, code, redirectURI, clientID, codeVerifier string) (*OAuthTokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", codeVerifier)
	return postToken(ctx, tokenEndpoint, form)
}

// RefreshToken trades a refresh_token for fresh tokens. Used on 401 when
// the access token has expired. Returns the new bundle (refresh_token may
// be rotated; persist the returned value).
func RefreshToken(ctx context.Context, tokenEndpoint, refreshToken, clientID string) (*OAuthTokens, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	return postToken(ctx, tokenEndpoint, form)
}

func postToken(ctx context.Context, tokenEndpoint string, form url.Values) (*OAuthTokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp oauth: token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Try to surface the AS error description when present.
		var oerr struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&oerr)
		msg := oerr.Error
		if oerr.ErrorDescription != "" {
			msg = msg + ": " + oerr.ErrorDescription
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("mcp oauth: token endpoint: %s", msg)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("mcp oauth: decode tokens: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("mcp oauth: token response missing access_token")
	}
	expiresAt := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return &OAuthTokens{
		AccessToken:   tr.AccessToken,
		RefreshToken:  tr.RefreshToken,
		TokenType:     tr.TokenType,
		ExpiresAt:     expiresAt,
		Scope:         tr.Scope,
		TokenEndpoint: tokenEndpoint,
	}, nil
}

// PerformOAuthFlow drives the whole flow: discovery → optional DCR → PKCE +
// browser → callback → token exchange. Returns the OAuthTokens bundle
// ready to persist.
//
// browser may be nil to use the default system browser launcher.
//
// The returned tokens have Client and TokenEndpoint populated so a later
// RefreshToken call can be made without re-running discovery.
func PerformOAuthFlow(ctx context.Context, serverName, serverURL string, scopes []string, browser auth.BrowserOpener) (*OAuthTokens, error) {
	md, err := DiscoverAuthServer(ctx, serverURL)
	if err != nil {
		return nil, err
	}

	// Bind a localhost callback listener first so we know which port to
	// register as a redirect URI in DCR.
	listener, err := auth.NewCallbackListener("/callback")
	if err != nil {
		return nil, err
	}
	defer func() { _ = listener.Close() }()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Port())

	// Dynamic Client Registration if no client_id was pre-configured.
	var client *ClientRegistration
	if md.RegistrationEndpoint != "" {
		client, err = RegisterClient(ctx, md.RegistrationEndpoint, "conduit ("+serverName+")", []string{redirectURI})
		if err != nil {
			return nil, err
		}
	} else {
		// Without registration, we can't get a client_id. Some MCPs publish
		// a static one in metadata under a non-standard key; we don't try
		// to handle those — the user must register out-of-band.
		return nil, fmt.Errorf("mcp oauth: %s incompatible auth server: does not support dynamic client registration", serverName)
	}

	verifier, err := auth.GenerateVerifier()
	if err != nil {
		return nil, err
	}
	challenge := auth.S256Challenge(verifier)
	state, err := auth.GenerateState()
	if err != nil {
		return nil, err
	}
	if err := listener.Register(state); err != nil {
		return nil, err
	}

	authURL := AuthorizeURL(md, client.ClientID, redirectURI, state, challenge, scopes)

	if browser == nil {
		browser = auth.SystemBrowser{}
	}
	// Best-effort browser open. The user can paste the URL manually if it
	// fails (the listener is still bound and ready to receive).
	_ = browser.Open(authURL)

	code, err := listener.Wait(ctx, state)
	if err != nil {
		return nil, err
	}
	listener.SendSuccessRedirect("about:blank")

	tokens, err := ExchangeCode(ctx, md.TokenEndpoint, code, redirectURI, client.ClientID, verifier)
	if err != nil {
		return nil, err
	}
	tokens.Client = *client
	return tokens, nil
}

// AuthorizeURLForFlow returns the URL to open along with the listener so
// callers (like McpAuthTool) that want to surface the URL to the LLM
// before opening a browser can do so. The caller must Close the listener
// after exchange (or on error).
//
// This is a lower-level alternative to PerformOAuthFlow. The full flow is:
//
//	url, listener, state, verifier, client, md, _ := AuthorizeURLForFlow(...)
//	defer func() { _ = listener.Close() }()
//	// surface url to the user/LLM
//	code, _ := listener.Wait(ctx, state)
//	tokens, _ := ExchangeCode(ctx, md.TokenEndpoint, code, redirectURI, client.ClientID, verifier)
func AuthorizeURLForFlow(ctx context.Context, serverName, serverURL string, scopes []string) (
	authURL string,
	listener *auth.CallbackListener,
	state, verifier, redirectURI string,
	client *ClientRegistration,
	md *AuthServerMetadata,
	err error,
) {
	md, err = DiscoverAuthServer(ctx, serverURL)
	if err != nil {
		return
	}
	listener, err = auth.NewCallbackListener("/callback")
	if err != nil {
		return
	}
	redirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Port())

	if md.RegistrationEndpoint == "" {
		_ = listener.Close()
		err = fmt.Errorf("mcp oauth: %s incompatible auth server: does not support dynamic client registration", serverName)
		return
	}
	client, err = RegisterClient(ctx, md.RegistrationEndpoint, "conduit ("+serverName+")", []string{redirectURI})
	if err != nil {
		_ = listener.Close()
		return
	}

	verifier, err = auth.GenerateVerifier()
	if err != nil {
		_ = listener.Close()
		return
	}
	challenge := auth.S256Challenge(verifier)
	state, err = auth.GenerateState()
	if err != nil {
		_ = listener.Close()
		return
	}
	if err = listener.Register(state); err != nil {
		_ = listener.Close()
		return
	}

	authURL = AuthorizeURL(md, client.ClientID, redirectURI, state, challenge, scopes)
	return
}
