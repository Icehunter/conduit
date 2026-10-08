package xai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceCode is the RFC 8628 device authorization grant.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// pollInterval is the polling interval when the server names none; a variable
// so tests can shorten it.
var pollInterval = 5 * time.Second

// RequestDeviceCode starts the device flow, the sign-in path for machines whose
// browser cannot reach the loopback callback.
func RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	form := url.Values{}
	form.Set("client_id", ClientID)
	form.Set("scope", Scope)
	form.Set("referrer", referrer)
	endpoint := strings.TrimRight(issuerURL(), "/") + "/oauth2/device/code"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai: device code request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("xai: read device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xai: device code request: %s", resp.Status)
	}
	var dc DeviceCode
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("xai: decode device code response: %w", err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, fmt.Errorf("xai: device code response missing codes")
	}
	return &dc, nil
}

// PollForToken polls until the user approves the code, it expires, or it is
// denied. interval is the server's advertised seconds (0 for the default).
func PollForToken(ctx context.Context, deviceCode string, expiresIn, interval int) (*TokenResponse, error) {
	wait := pollInterval
	if interval > 0 {
		wait = time.Duration(interval) * time.Second
	}
	step := wait
	life := time.Duration(expiresIn) * time.Second
	if expiresIn <= 0 {
		life = 15 * time.Minute
	}
	deadline := time.Now().Add(life)
	for {
		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("device_code", deviceCode)
		form.Set("client_id", ClientID)
		tokens, err := tokenRequest(ctx, form)
		if err == nil {
			return tokens, nil
		}
		te, ok := err.(*tokenError) //nolint:errorlint // returned unwrapped by tokenRequest
		if !ok {
			return nil, err
		}
		switch te.Code {
		case "authorization_pending":
		case "slow_down":
			wait += step
		case "expired_token":
			return nil, fmt.Errorf("xai: device code expired, try again")
		case "access_denied":
			return nil, fmt.Errorf("xai: authorization was denied")
		default:
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("xai: device code expired, try again")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}
