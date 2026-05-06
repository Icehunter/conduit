package app

import (
	"os"

	"github.com/icehunter/conduit/internal/agent"
)

// BuildMetadata returns the API metadata block.
func BuildMetadata() map[string]any {
	deviceID := os.Getenv("CLAUDE_CODE_DEVICE_ID")
	if deviceID == "" {
		deviceID = "00000000000000000000000000000000"
	}
	accountUUID := os.Getenv("CLAUDE_CODE_ACCOUNT_UUID")
	sessionID := NewSessionID()
	return agent.BuildMetadata(deviceID, accountUUID, sessionID)
}
