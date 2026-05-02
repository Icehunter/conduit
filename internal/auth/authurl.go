package auth

import (
	"fmt"
	"net/url"
	"strings"
)

// BuildAuthURLParams gathers every input the reference's GI_ function takes
// (decoded/1220.js:32). All fields except CodeChallenge and State are optional
// in some combination — see the field docs.
type BuildAuthURLParams struct {
	// CodeChallenge is the BASE64URL(SHA256(verifier)) string.
	CodeChallenge string
	// State is the OAuth state nonce echoed back at the callback.
	State string
	// Port is the local listener port for the automatic flow. Ignored when
	// IsManual is true.
	Port int
	// IsManual selects the `MANUAL_REDIRECT_URL` redirect_uri (the user pastes
	// the code) instead of `http://localhost:<port>/callback`.
	IsManual bool
	// LoginWithClaudeAI selects `ClaudeAIAuthorizeURL` over
	// `ConsoleAuthorizeURL`. Use true for Max/Pro/Team/Enterprise; false for
	// the Console (API key) flow.
	LoginWithClaudeAI bool
	// InferenceOnly requests the long-lived inference-only scope set
	// (`user:inference` only). Used by SDK callers like Cowork.
	InferenceOnly bool
	// OrgUUID, LoginHint, LoginMethod are optional pre-fill / routing hints.
	OrgUUID     string
	LoginHint   string
	LoginMethod string
}

// BuildAuthURL constructs the OAuth authorize URL exactly as Claude Code
// v2.1.126's GI_ function does. Param insertion order is preserved (Go's
// `url.Values.Encode` would sort alphabetically, which would break the
// fixture comparison), and values are URL-escaped using the same
// application/x-www-form-urlencoded scheme JS's URLSearchParams uses
// (space -> "+", reserved chars percent-encoded).
//
// Reference: decoded/1220.js:32-64.
func BuildAuthURL(cfg Config, p BuildAuthURLParams) (string, error) {
	if p.CodeChallenge == "" {
		return "", fmt.Errorf("auth: BuildAuthURL: missing code_challenge")
	}
	if p.State == "" {
		return "", fmt.Errorf("auth: BuildAuthURL: missing state")
	}
	if !p.IsManual && p.Port <= 0 {
		return "", fmt.Errorf("auth: BuildAuthURL: automatic flow requires Port > 0")
	}

	base := cfg.ConsoleAuthorizeURL
	if p.LoginWithClaudeAI {
		base = cfg.ClaudeAIAuthorizeURL
	}

	redirect := fmt.Sprintf("http://localhost:%d/callback", p.Port)
	if p.IsManual {
		redirect = cfg.ManualRedirectURL
	}

	scopes := ScopesAll
	if p.InferenceOnly {
		scopes = ScopesInferenceOnly
	}

	// Order matches the reference's append() sequence exactly.
	pairs := [][2]string{
		{"code", "true"},
		{"client_id", cfg.ClientID},
		{"response_type", "code"},
		{"redirect_uri", redirect},
		{"scope", strings.Join(scopes, " ")},
		{"code_challenge", p.CodeChallenge},
		{"code_challenge_method", "S256"},
		{"state", p.State},
	}
	if p.OrgUUID != "" {
		pairs = append(pairs, [2]string{"orgUUID", p.OrgUUID})
	}
	if p.LoginHint != "" {
		pairs = append(pairs, [2]string{"login_hint", p.LoginHint})
	}
	if p.LoginMethod != "" {
		pairs = append(pairs, [2]string{"login_method", p.LoginMethod})
	}

	var sb strings.Builder
	sb.Grow(len(base) + 256)
	sb.WriteString(base)
	for i, kv := range pairs {
		if i == 0 {
			// JS's URL constructor preserves any existing query — but our
			// authorize URLs are bare, so always start with "?".
			sb.WriteByte('?')
		} else {
			sb.WriteByte('&')
		}
		sb.WriteString(url.QueryEscape(kv[0]))
		sb.WriteByte('=')
		sb.WriteString(url.QueryEscape(kv[1]))
	}
	return sb.String(), nil
}
