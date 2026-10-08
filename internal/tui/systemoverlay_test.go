package tui

import (
	"testing"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tool"
)

func overlayTexts(blocks []api.SystemBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.Text)
	}
	return out
}

func TestApplySystemOverlay(t *testing.T) {
	const style = "# Output style: terse\n\nbe brief"
	tests := []struct {
		name   string
		mode   permissions.Mode
		prompt string
		want   []string
	}{
		{"default no style", permissions.ModeDefault, "", nil},
		{"plan no style", permissions.ModePlan, "", []string{agent.PlanModeDirective}},
		{"council no style", permissions.ModeCouncil, "", []string{agent.CouncilModeDirective}},
		{"style only", permissions.ModeDefault, "be brief", []string{style}},
		{"style then plan directive", permissions.ModePlan, "be brief", []string{style, agent.PlanModeDirective}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lp := agent.NewLoop(nil, tool.NewRegistry(), agent.LoopConfig{})
			m := New(Config{Resumed: true, Loop: lp})
			m.outputStyleName = "terse"
			m.outputStylePrompt = tc.prompt
			m.permissionMode = tc.mode
			m.applySystemOverlay()
			got := overlayTexts(lp.SystemOverlay())
			if len(got) != len(tc.want) {
				t.Fatalf("overlay = %q, want %q", got, tc.want)
			}
			// Children inherit the mode directive alone, never the style.
			var wantDir []string
			for _, w := range tc.want {
				if w == agent.PlanModeDirective || w == agent.CouncilModeDirective {
					wantDir = append(wantDir, w)
				}
			}
			gotDir := overlayTexts(lp.ModeDirective())
			if len(gotDir) != len(wantDir) || (len(wantDir) == 1 && gotDir[0] != wantDir[0]) {
				t.Errorf("mode directive = %q, want %q", gotDir, wantDir)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("overlay[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// Rapid toggles apply in call order because applySystemOverlay is synchronous.
func TestApplySystemOverlay_LastToggleWins(t *testing.T) {
	lp := agent.NewLoop(nil, tool.NewRegistry(), agent.LoopConfig{})
	m := New(Config{Resumed: true, Loop: lp})
	for _, mode := range []permissions.Mode{permissions.ModePlan, permissions.ModeDefault} {
		m.permissionMode = mode
		m.applySystemOverlay()
	}
	if got := lp.SystemOverlay(); len(got) != 0 {
		t.Errorf("overlay after plan->default = %q, want empty", overlayTexts(got))
	}
}
