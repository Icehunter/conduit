package main

import (
	"path/filepath"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/session"
)

// latestSessionWithHistory picks the newest session that has messages.
// sessions is newest first. A run that exits before its first turn leaves an
// empty transcript behind, and --continue must skip it rather than resume
// nothing and show the welcome card.
func latestSessionWithHistory(sessions []session.SessionMeta, load func(string) ([]api.Message, error), warnf func(string, ...any)) (session.SessionMeta, []api.Message, bool) {
	for _, s := range sessions {
		history, err := load(s.FilePath)
		if err != nil {
			warnf("Could not load session %q: %v", filepath.Base(s.FilePath), err)
			continue
		}
		if len(history) > 0 {
			return s, history, true
		}
	}
	return session.SessionMeta{}, nil, false
}
