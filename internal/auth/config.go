package auth

// Config holds the OAuth endpoint URLs and client metadata.
//
// Values come from decoded/0533.js (the prod block of v_q). The leaked TS
// source allows USER_TYPE=ant + USE_STAGING_OAUTH to swap these out; we
// don't replicate that — Anthropic-internal staging is out of scope (see
// plan §"Out of scope").
type Config struct {
	BaseAPIURL           string
	ConsoleAuthorizeURL  string
	ClaudeAIAuthorizeURL string
	ClaudeAIOrigin       string
	TokenURL             string
	APIKeyURL            string
	RolesURL             string
	ConsoleSuccessURL    string
	ClaudeAISuccessURL   string
	ManualRedirectURL    string
	ClientID             string
	MCPProxyURL          string
	MCPProxyPath         string
}

// ProdConfig is the production OAuth configuration shipping in
// Claude Code v2.1.126. The client_id is a public identifier (not a secret).
//
// Note on ClaudeAIAuthorizeURL: the decoded reference (decoded/0533.js:10)
// uses "https://claude.com/cai/oauth/authorize", which is meant to 307 to
// claude.ai/oauth/authorize for attribution. Empirically that bounce
// rejects requests with the same query string we send, returning "Invalid
// request format". We point straight at the terminal claude.ai URL — same
// client_id, same accepted query, no attribution bounce.
var ProdConfig = Config{
	BaseAPIURL:           "https://api.anthropic.com",
	ConsoleAuthorizeURL:  "https://platform.claude.com/oauth/authorize",
	ClaudeAIAuthorizeURL: "https://claude.ai/oauth/authorize",
	ClaudeAIOrigin:       "https://claude.ai",
	TokenURL:             "https://platform.claude.com/v1/oauth/token",
	APIKeyURL:            "https://api.anthropic.com/api/oauth/claude_cli/create_api_key",
	RolesURL:             "https://api.anthropic.com/api/oauth/claude_cli/roles",
	ConsoleSuccessURL:    "https://platform.claude.com/buy_credits?returnUrl=/oauth/code/success%3Fapp%3Dclaude-code",
	ClaudeAISuccessURL:   "https://platform.claude.com/oauth/code/success?app=claude-code",
	ManualRedirectURL:    "https://platform.claude.com/oauth/code/callback",
	ClientID:             "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
	MCPProxyURL:          "https://mcp-proxy.anthropic.com",
	MCPProxyPath:         "/v1/mcp/{server_id}",
}

// Scope literals (decoded/0532.js:119-121).
const (
	ScopeUserInference   = "user:inference"
	ScopeUserProfile     = "user:profile"
	ScopeOrgCreateAPIKey = "org:create_api_key"
	ScopeUserSessionsCC  = "user:sessions:claude_code"
	ScopeUserMCPServers  = "user:mcp_servers"
	ScopeUserFileUpload  = "user:file_upload"
)

// ScopesInferenceOnly is requested for long-lived inference-only tokens
// (decoded/1220.js:52: `inferenceOnly ? [VS] : ZV6`).
var ScopesInferenceOnly = []string{ScopeUserInference}

// ScopesConsole is the Console OAuth scope set, used when minting an API key
// via the Console flow (decoded/0533.js:4: `N_q = [SA4, LJH]`).
var ScopesConsole = []string{ScopeOrgCreateAPIKey, ScopeUserProfile}

// ScopesClaudeAI is the Claude.ai subscriber scope set
// (decoded/0533.js:5: `qv_ = [LJH, VS, "user:sessions:claude_code", "user:mcp_servers", "user:file_upload"]`).
var ScopesClaudeAI = []string{
	ScopeUserProfile,
	ScopeUserInference,
	ScopeUserSessionsCC,
	ScopeUserMCPServers,
	ScopeUserFileUpload,
}

// ScopesAll is the de-duped union of console + claude.ai scopes
// (decoded/0533.js:6: `ZV6 = unique([...N_q, ...qv_])`).
var ScopesAll = []string{
	ScopeOrgCreateAPIKey,
	ScopeUserProfile,
	ScopeUserInference,
	ScopeUserSessionsCC,
	ScopeUserMCPServers,
	ScopeUserFileUpload,
}
