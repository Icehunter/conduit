package mcp

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/icehunter/conduit/internal/secure"
)

// oauthService is the secure-storage service identifier for MCP OAuth
// tokens. Distinct from the Anthropic auth service so /logout for the
// account doesn't wipe MCP creds (and vice versa).
const oauthService = "com.icehunter.conduit.mcp"

// SaveServerToken persists an OAuthTokens bundle for the named MCP server.
func SaveServerToken(s secure.Storage, serverName string, tokens *OAuthTokens) error {
	if serverName == "" {
		return errors.New("mcp oauth: empty server name")
	}
	buf, err := json.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("mcp oauth: marshal tokens: %w", err)
	}
	return s.Set(oauthService, serverName, buf)
}

// LoadServerToken returns the persisted OAuthTokens bundle for serverName.
// Returns secure.ErrNotFound when no bundle is present.
func LoadServerToken(s secure.Storage, serverName string) (*OAuthTokens, error) {
	raw, err := s.Get(oauthService, serverName)
	if err != nil {
		return nil, err
	}
	var tokens OAuthTokens
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return nil, fmt.Errorf("mcp oauth: decode tokens: %w", err)
	}
	return &tokens, nil
}

// DeleteServerToken removes the persisted OAuthTokens bundle. Idempotent
// — returns nil if no bundle was present.
func DeleteServerToken(s secure.Storage, serverName string) error {
	if err := s.Delete(oauthService, serverName); err != nil {
		if errors.Is(err, secure.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("mcp oauth: delete tokens: %w", err)
	}
	return nil
}
