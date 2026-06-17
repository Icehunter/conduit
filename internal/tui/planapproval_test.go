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
// plan-approval state. The guard is cleared so direct handlePlanApprovalKey
// calls exercise the key-handling logic without triggering the first-key guard
// (the guard itself is tested in TestPlanApproval_GuardFirstKey below).
func makePlanApprovalModel(t *testing.T, plan string) Model {
	t.Helper()
	m := Model{width: 100, height: 40}
	reply := make(chan planmodetool.PlanApprovalDecision, 1)
	// 80x20 inner viewport: matches a real-world inset modal at 100x40.
	m.planApproval = newPlanApprovalState(plan, reply, 80, 20)
	m.planApproval.guardFirstKey = false // guard tests are in TestPlanApproval_GuardFirstKey
	return m
}

func TestPlanApproval_RenderShowsPlanAndOptions(t *testing.T) {
	plan := "# Refactor the auth gate\n\n1. Extract evalRule\n2. Add tests\n3. Wire callers"
	m := makePlanApprovalModel(t, plan)

	out := plainText(m.renderPlanApprovalPicker(96, 30))

	// All five options must be present and numbered.
	for _, want := range []string{
		"1. Approve — auto mode",
		"2. Approve — accept edits",
		"3. Approve — live review",
		"4. Approve — default mode",
		"5. 💬 Chat about this",
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

func TestPlanApproval_EnterAndEscBehavior(t *testing.T) {
	tests := []struct {
		key      string
		want     planmodetool.PlanApprovalDecision
		wantSent bool
	}{
		// Enter commits whichever option is currently selected (default = 0 = bypass).
		{"enter", planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeBypassPermissions}, true},
		// Esc is a clean reject regardless of selected option.
		{"esc", planmodetool.PlanApprovalDecision{Approved: false, Discuss: false}, true},
		// Digit shortcuts are removed — no longer bind to options.
		{"1", planmodetool.PlanApprovalDecision{}, false},
		{"2", planmodetool.PlanApprovalDecision{}, false},
		{"6", planmodetool.PlanApprovalDecision{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m := makePlanApprovalModel(t, "stub plan")
			reply := make(chan planmodetool.PlanApprovalDecision, 1)
			m.planApproval.reply = reply

			m2, cmd := m.handlePlanApprovalKey(makeKey(tt.key))
			if !tt.wantSent {
				if cmd != nil {
					t.Errorf("expected no cmd for key %q (digit shortcuts removed)", tt.key)
				}
				if m2.planApproval == nil {
					t.Error("modal should still be open after unbound key")
				}
				return
			}
			if cmd == nil {
				t.Fatalf("key %q produced no command", tt.key)
			}
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

// makeKey builds a KeyPressMsg from a short string. Named keys use Code only
// (no Text) so that String() returns the canonical name. Printable single chars
// set both Code and Text.
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
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace}
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
	m, _ = m.handlePlanApprovalKey(makeKey("up"))
	if m.planApproval.selected != 0 {
		t.Errorf("Up should move cursor back to 0, got %d", m.planApproval.selected)
	}
	// Down to the last option via repeated presses.
	last := len(planApprovalOptions) - 1
	for i := 0; i < last; i++ {
		m, _ = m.handlePlanApprovalKey(makeKey("down"))
	}
	if m.planApproval.selected != last {
		t.Errorf("Repeated Down should reach last option %d, got %d", last, m.planApproval.selected)
	}
	// Another Down at the bottom should clamp (no wrap).
	m, _ = m.handlePlanApprovalKey(makeKey("down"))
	if m.planApproval.selected != last {
		t.Errorf("Down past end should clamp to %d, got %d", last, m.planApproval.selected)
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

// TestPlanApproval_GuardFirstKey verifies that the first keystroke after the
// plan-approval modal opens is swallowed (so an in-flight Enter from the user's
// text box cannot auto-approve the plan). The guard resets after one key, so
// the second identical key should act normally.
func TestPlanApproval_GuardFirstKey(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		passThru bool // esc/ctrl+c bypass the guard
	}{
		{"enter swallowed", "enter", false},
		{"space swallowed", " ", false},
		{"down swallowed", "down", false},
		{"esc passes through", "esc", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := make(chan planmodetool.PlanApprovalDecision, 1)
			m := Model{width: 100, height: 40}
			m.planApproval = newPlanApprovalState("stub plan", reply, 80, 20)
			// Guard must be set by newPlanApprovalState.
			if !m.planApproval.guardFirstKey {
				t.Fatal("guardFirstKey should be true after newPlanApprovalState")
			}

			m2, cmd := m.handlePlanApprovalKey(makeKey(tt.key))
			if tt.passThru {
				// Esc should close the modal immediately (reject decision).
				if m2.planApproval != nil {
					t.Error("esc should bypass the guard and close the modal")
				}
				if cmd == nil {
					t.Error("esc should produce a command")
				}
			} else {
				// Key should be swallowed: modal stays open, no command, guard cleared.
				if m2.planApproval == nil {
					t.Error("modal should still be open after guard swallow")
				}
				if cmd != nil {
					t.Errorf("key %q should be swallowed (no cmd) but got a command", tt.key)
				}
				if m2.planApproval.guardFirstKey {
					t.Error("guardFirstKey should be false after first key is swallowed")
				}
				// After guard clears, Enter commits and Down moves cursor.
				if tt.key == "down" {
					m3, cmd2 := m2.handlePlanApprovalKey(makeKey("enter"))
					if cmd2 == nil {
						t.Error("enter after guard clears should produce a command")
					}
					if m3.planApproval != nil {
						t.Error("modal should be closed after enter")
					}
				}
			}
		})
	}
}
