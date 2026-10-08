package copilot

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/icehunter/conduit/internal/credlock"
	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/settings"
)

// Credential metadata keys.
const (
	// metaAPIEndpoint is endpoints.api from the token exchange: the host a
	// Business or Enterprise seat must talk to.
	metaAPIEndpoint = "api_endpoint"
	// metaModelEndpoints is a JSON object, model id to supported_endpoints,
	// recorded each time /models is fetched.
	metaModelEndpoints = "model_endpoints"
)

// Route is the Copilot wire a model is sent on.
type Route int

const (
	RouteChat Route = iota
	RouteResponses
	RouteMessages
)

// Endpoint paths as /models lists them in supported_endpoints.
const (
	endpointChat      = "/chat/completions"
	endpointResponses = "/responses"
	endpointMessages  = "/v1/messages"
)

// RouteForModel picks the wire for model from the supported_endpoints /models
// reported: /responses first, then /v1/messages, otherwise chat completions.
// With no endpoint list it falls back to the model-name heuristics
// (ShouldUseResponsesAPI, UsesMessagesAPI).
//
// gpt-5-mini stays on chat completions when it offers it, the known exception
// ShouldUseResponsesAPI also carries.
func RouteForModel(model string, endpoints []string) Route {
	if len(endpoints) == 0 {
		switch {
		case ShouldUseResponsesAPI(model):
			return RouteResponses
		case UsesMessagesAPI(model):
			return RouteMessages
		}
		return RouteChat
	}
	has := map[string]bool{}
	for _, e := range endpoints {
		has[strings.ToLower(strings.TrimSpace(e))] = true
	}
	switch {
	case has[endpointResponses] && !(strings.HasPrefix(strings.ToLower(model), "gpt-5-mini") && has[endpointChat]):
		return RouteResponses
	case has[endpointMessages]:
		return RouteMessages
	}
	return RouteChat
}

// validAPIEndpoint accepts the endpoints.api value only when it is an https
// origin on a GitHub Copilot host, since the bearer token is sent to it.
func validAPIEndpoint(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".githubcopilot.com") && !strings.HasSuffix(host, ".ghe.com") {
		return "", false
	}
	return "https://" + u.Host, true
}

// APIEndpoint is the Business/Enterprise API origin stored with cred, or "".
func APIEndpoint(cred *providerauth.ProviderCredential) string {
	if cred == nil {
		return ""
	}
	endpoint, _ := validAPIEndpoint(cred.Metadata[metaAPIEndpoint])
	return endpoint
}

// ResolveBaseURL is the base URL for requests made with cred. A configured
// URL that is empty or the default ChatBaseURL is replaced by the credential's
// own API endpoint when it has one; a URL the user chose is kept.
func ResolveBaseURL(configured string, cred *providerauth.ProviderCredential) string {
	configured = strings.TrimRight(strings.TrimSpace(configured), "/")
	if configured == "" || strings.EqualFold(configured, ChatBaseURL) {
		if endpoint := APIEndpoint(cred); endpoint != "" {
			return endpoint
		}
		return ChatBaseURL
	}
	return configured
}

// MessagesBaseURL is the origin for the Claude /v1/messages route: baseURL
// when it is a valid Copilot API endpoint (a Business or Enterprise host),
// otherwise ChatBaseURL, since the route is not served from arbitrary hosts.
func MessagesBaseURL(baseURL string) string {
	if endpoint, ok := validAPIEndpoint(baseURL); ok {
		return endpoint
	}
	return ChatBaseURL
}

// modelsEndpoint is the /models URL for cred: the test override or default
// URL, moved to the credential's API endpoint when it has one.
func modelsEndpoint(cred *providerauth.ProviderCredential) string {
	u := modelsURLFunc()
	if u == modelsURL {
		if endpoint := APIEndpoint(cred); endpoint != "" {
			return endpoint + "/models"
		}
	}
	return u
}

// ModelEndpoints returns the supported_endpoints recorded with cred for model,
// nil when /models has not been fetched for it.
func ModelEndpoints(cred *providerauth.ProviderCredential, model string) []string {
	if cred == nil || cred.Metadata[metaModelEndpoints] == "" {
		return nil
	}
	var all map[string][]string
	if json.Unmarshal([]byte(cred.Metadata[metaModelEndpoints]), &all) != nil {
		return nil
	}
	return all[model]
}

func (a *Authorizer) saveModelEndpoints(ctx context.Context, endpoints map[string][]string) error {
	if len(endpoints) == 0 {
		return nil
	}
	raw, err := json.Marshal(endpoints)
	if err != nil {
		return err
	}
	// Same lock as Refresh: a token exchange rewrites the whole credential, and
	// an unlocked read-modify-write here could save the old token over it.
	// A lock failure is not fatal.
	release, _ := credlock.Acquire(ctx, "copilot:"+a.credentialName)
	defer release()
	cred, err := settings.LoadStructuredProviderCredential(a.store, a.credentialName)
	if err != nil {
		return err
	}
	if cred.Metadata == nil {
		cred.Metadata = map[string]string{}
	}
	cred.Metadata[metaModelEndpoints] = string(raw)
	return settings.SaveStructuredProviderCredential(a.store, a.credentialName, cred)
}

// isModelPolicyBody reports whether a 403 body is about a model's policy or
// enablement ("model_not_supported", "policy"), not about the account.
func isModelPolicyBody(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "model_not_supported") || strings.Contains(s, "model_not_available") ||
		(strings.Contains(s, "model") && strings.Contains(s, "policy"))
}
