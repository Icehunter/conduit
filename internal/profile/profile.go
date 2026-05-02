// Package profile fetches the authenticated user's account and subscription
// info from the Anthropic oauth profile endpoint.
package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const endpoint = "https://api.anthropic.com/api/oauth/profile"

// Info holds the fields we surface in the welcome card.
type Info struct {
	DisplayName      string // account.display_name
	Email            string // account.email_address
	OrganizationName string // organization.organization_name
	SubscriptionType string // derived from organization.organization_type
}

// oauthProfileResponse is the raw JSON shape returned by the endpoint.
type oauthProfileResponse struct {
	Account *struct {
		DisplayName  string `json:"display_name"`
		EmailAddress string `json:"email_address"`
	} `json:"account"`
	Organization *struct {
		OrganizationName string `json:"organization_name"`
		OrganizationType string `json:"organization_type"`
	} `json:"organization"`
}

// subscriptionLabel maps organization_type → human label.
var subscriptionLabel = map[string]string{
	"claude_max":        "Claude Max",
	"claude_pro":        "Claude Pro",
	"claude_enterprise": "Claude Enterprise",
	"claude_team":       "Claude Team",
}

// Fetch retrieves profile info using the given OAuth access token.
// Non-fatal: returns a zero Info and nil error when the endpoint is
// unreachable or returns a non-200 status (e.g. API-key-only users).
func Fetch(ctx context.Context, accessToken string) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Info{}, fmt.Errorf("profile: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Info{}, nil // network failure — treat as no profile
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Info{}, nil // 403 for API-key users — no profile, no error
	}

	var raw oauthProfileResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Info{}, nil // malformed — degrade gracefully
	}

	info := Info{}
	if raw.Account != nil {
		info.DisplayName = raw.Account.DisplayName
		info.Email = raw.Account.EmailAddress
	}
	if raw.Organization != nil {
		info.OrganizationName = raw.Organization.OrganizationName
		if label, ok := subscriptionLabel[raw.Organization.OrganizationType]; ok {
			info.SubscriptionType = label
		} else {
			info.SubscriptionType = raw.Organization.OrganizationType
		}
	}
	return info, nil
}
