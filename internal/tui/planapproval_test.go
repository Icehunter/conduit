package tui

import (
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
)

// makePlanApprovalModel constructs a minimal Model with a populated
// plan-approval state. width is the screen width; the modal renders inside
// a typical chat-viewport rect.
func makePlanApprovalModel(t *testing.T, plan string) Model {
	t.Helper()
	m := Model{width: 100, height: 40}
	reply := make(chan planmodetool.PlanApprovalDecision, 1)
	// 80x20 inner viewport: matches a real-world inset modal at 100x40.
	m.planApproval = newPlanApprovalState(plan, reply, 80, 20)
	return m
}

func TestPlanApproval_RenderShowsPlanAndOptions(t *testing.T) {
	plan := "# Refactor the auth gate\n\n1. Extract evalRule\n2. Add tests\n3. Wire callers"
	m := makePlanApprovalModel(t, plan)

	out := plainText(m.renderPlanApprovalPicker(96, 30))

	// All four options must be present and numbered.
	for _, want := range []string{
		"1. Approve — auto mode",
		"2. Approve — accept edits",
		"3. Approve — default mode",
		"4. 💬 Chat about this",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan-approval modal missing option %q\nrendered:\n%s", want, out)
		}
	}

	// Plan content must appear (markdown renderer may transform # / 1. but
	// the literal "Refactor the auth gate" string survives).
	if !strings.Contains(out, "Refactor the auth gate") {
		t.Errorf("plan-approval modal missing plan content; rendered:\n%s", out)
	}

	// Header is present.
	if !strings.Contains(out, "Ready to code?") {
		t.Errorf("plan-approval modal missing header; rendered:\n%s", out)
	}
}

func TestPlanApproval_QuickPickReturnsCorrectDecision(t *testing.T) {
	tests := []struct {
		key      string
		want     planmodetool.PlanApprovalDecision
		wantSent bool
	}{
		{"1", planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeBypassPermissions}, true},
		{"2", planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeAcceptEdits}, true},
		{"3", planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeDefault}, true},
		{"4", planmodetool.PlanApprovalDecision{Approved: false}, true},
		{"esc", planmodetool.PlanApprovalDecision{Approved: false}, true},
		{"5", planmodetool.PlanApprovalDecision{}, false}, // out of range, no send
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m := makePlanApprovalModel(t, "stub plan")
			reply := make(chan planmodetool.PlanApprovalDecision, 1)
			m.planApproval.reply = reply

			m2, cmd := m.handlePlanApprovalKey(makeKey(tt.key))
			if !tt.wantSent {
				if cmd != nil {
					t.Errorf("expected no cmd for unbound key %q", tt.key)
				}
				if m2.planApproval == nil {
					t.Error("modal should still be open after unbound key")
				}
				return
			}
			if cmd == nil {
				t.Fatalf("key %q produced no command", tt.key)
			}
			// Run the command to push the decision onto the reply channel.
			cmd()
			select {
			case got := <-reply:
				if got != tt.want {
					t.Errorf("key %q: got %+v, want %+v", tt.key, got, tt.want)
				}
			default:
				t.Errorf("key %q: no decision sent on reply channel", tt.key)
			}
			if m2.planApproval != nil {
				t.Error("modal should be closed after a decision")
			}
		})
	}
}

// makeKey builds a KeyPressMsg from a short string. Named keys ("esc", "tab",
// "enter", "up", "down", "home", "end") use Code only (no Text) so that
// String() returns the canonical name. Printable single chars set both.
func makeKey(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	default:
		if len(s) == 1 {
			return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
		}
		return tea.KeyPressMsg{}
	}
}

func TestPlanApproval_ArrowsMoveOptionCursor(t *testing.T) {
	m := makePlanApprovalModel(t, "plan")

	m, _ = m.handlePlanApprovalKey(makeKey("down"))
	if m.planApproval.selected != 1 {
		t.Errorf("Down should advance cursor to 1, got %d", m.planApproval.selected)
	}
	m, _ = m.handlePlanApprovalKey(makeKey("end"))
	if m.planApproval.selected != len(planApprovalOptions)-1 {
		t.Errorf("End should jump to last option, got %d", m.planApproval.selected)
	}
	m, _ = m.handlePlanApprovalKey(makeKey("home"))
	if m.planApproval.selected != 0 {
		t.Errorf("Home should jump to first option, got %d", m.planApproval.selected)
	}
}

func TestPlanApproval_RecordPlanRectPositionsViewport(t *testing.T) {
	m := makePlanApprovalModel(t, "stub")
	modalRect := image.Rect(10, 5, 90, 25)
	m.planApproval.recordPlanRect(modalRect)
	got := m.planApproval.planRect
	// Plan rect is offset by border (1) + body padX (2) + dashed pad (1) on
	// the left, and the same on the right. Top offset is 6 rows from modal top.
	if got.Min.X != modalRect.Min.X+4 {
		t.Errorf("planRect.Min.X = %d, want %d", got.Min.X, modalRect.Min.X+4)
	}
	if got.Max.X != modalRect.Max.X-4 {
		t.Errorf("planRect.Max.X = %d, want %d", got.Max.X, modalRect.Max.X-4)
	}
	if got.Min.Y != modalRect.Min.Y+6 {
		t.Errorf("planRect.Min.Y = %d, want %d", got.Min.Y, modalRect.Min.Y+6)
	}
	if got.Dy() != m.planApproval.vp.Height() {
		t.Errorf("planRect height = %d, want %d", got.Dy(), m.planApproval.vp.Height())
	}
}
