// Authorization and token-exchange flows. Types and discovery helpers live in
// oauth.go (same package).
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
