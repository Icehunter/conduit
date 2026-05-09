package pendingedits

import (
	"strings"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

// GatedStager is a Stager that only stages writes when the permission gate's
// current mode is ModeAcceptEdits. In all other modes it returns ErrNotStaging
// so file tools fall through to the direct-write path.
//
// This lets the foreground loop wire one Stager unconditionally; the mode
// check happens at call time so dynamic mode changes (e.g., user picks "auto"
// in the plan-approval modal) take effect immediately without rewiring tools.
type GatedStager struct {
	Table *Table
	Gate  interface{ Mode() permissions.Mode }
}

// Stage implements Stager. It forwards to the table only in acceptEdits or
// acceptEditsLive mode; otherwise it returns ErrNotStaging so the caller
// falls through to direct write.
//
// Writes to conduit's own config directory (~/.conduit/…) are never staged —
// memory files and session state should always go to disk immediately.
func (g *GatedStager) Stage(e Entry) error {
	if g.Table == nil || g.Gate == nil {
		return ErrNotStaging
	}
	m := g.Gate.Mode()
	if m != permissions.ModeAcceptEdits && m != permissions.ModeAcceptEditsLive {
		return ErrNotStaging
	}
	conduitDir := settings.ConduitDir()
	if strings.HasPrefix(e.Path, conduitDir+"/") || e.Path == conduitDir {
		return ErrNotStaging
	}
	return g.Table.Stage(e)
}

// Pending returns the staged entry for path when staging is currently active.
// File tools use this as a virtual file layer so sequential staged edits compose
// against the latest staged content instead of stale disk bytes.
func (g *GatedStager) Pending(path string) (Entry, bool) {
	if g.Table == nil || g.Gate == nil {
		return Entry{}, false
	}
	m := g.Gate.Mode()
	if m != permissions.ModeAcceptEdits && m != permissions.ModeAcceptEditsLive {
		return Entry{}, false
	}
	return g.Table.Get(path)
}
