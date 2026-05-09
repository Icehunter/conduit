package pendingedits

import "github.com/icehunter/conduit/internal/permissions"

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
func (g *GatedStager) Stage(e Entry) error {
	if g.Gate == nil {
		return ErrNotStaging
	}
	m := g.Gate.Mode()
	if m != permissions.ModeAcceptEdits && m != permissions.ModeAcceptEditsLive {
		return ErrNotStaging
	}
	return g.Table.Stage(e)
}
