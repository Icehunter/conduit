// Package team manages agent team state for conduit's in-process agent teams
// feature. Unlike upstream Claude Code (which uses separate OS processes and
// tmux), conduit teammates are goroutine-level nested Loops sharing the same
// process.
//
// This file holds the activation flag and session-naming helpers. The runtime
// (member registry, mailbox, spawn) will be added in the full implementation.
package team

import (
	"sync/atomic"
)

var active atomic.Bool

// IsActive reports whether agent teams are enabled for this session.
// Toggled at startup from conduit.json "agentTeams" and live via SetActive.
func IsActive() bool {
	return active.Load()
}

// SetActive enables or disables agent teams. Called at startup from the loaded
// ConduitConfig and immediately when the /config panel toggles the setting.
func SetActive(on bool) {
	active.Store(on)
}

// SessionName returns the deterministic team name derived from a session ID:
// "team-" + the first 8 characters of the session ID. Replaces the removed
// TeamCreate/TeamDelete tools (CC divergence: session-scoped, no create/delete).
func SessionName(sessionID string) string {
	if len(sessionID) > 8 {
		return "team-" + sessionID[:8]
	}
	return "team-" + sessionID
}
