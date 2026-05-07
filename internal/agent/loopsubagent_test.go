package agent

import (
	"testing"

	"github.com/icehunter/conduit/internal/permissions"
)

func TestSubAgentModeIsolation(t *testing.T) {
	// Build a minimal parent gate in default mode.
	parentGate := permissions.New("", nil, permissions.ModeDefault, nil, nil, nil)

	// Clone produces an independent gate.
	childGate := parentGate.Clone()
	childGate.SetMode(permissions.ModePlan)

	// Parent must remain unchanged after child mode is set.
	if parentGate.Mode() != permissions.ModeDefault {
		t.Errorf("parent mode changed: got %v, want ModeDefault", parentGate.Mode())
	}
	if childGate.Mode() != permissions.ModePlan {
		t.Errorf("child mode wrong: got %v, want ModePlan", childGate.Mode())
	}

	// Changing parent after clone must not affect the child.
	parentGate.SetMode(permissions.ModeBypassPermissions)
	if childGate.Mode() != permissions.ModePlan {
		t.Errorf("child mode leaked from parent: got %v, want ModePlan", childGate.Mode())
	}

	// Verify we can construct a Loop with the cloned gate without panicking.
	parentLoop := &Loop{
		cfg: LoopConfig{
			Gate:  parentGate,
			Model: "claude-haiku-4-5-20251001",
		},
	}
	_ = parentLoop
}
