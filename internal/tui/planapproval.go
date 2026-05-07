package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
)

// planApprovalPickerState drives the plan-approval overlay shown when the model
// calls ExitPlanMode. The user chooses how to approve or reject the plan.
type planApprovalPickerState struct {
	reply    chan<- planmodetool.PlanApprovalDecision
	selected int // cursor index 0..4
}

// planApprovalAskMsg is sent by the ExitPlanMode callback to open the picker.
type planApprovalAskMsg struct {
	reply chan planmodetool.PlanApprovalDecision
}

// planApprovalItems are the fixed option labels shown in the approval picker.
var planApprovalItems = []string{
	"Approve — use auto mode (all tool calls run without prompts)",
	"Approve — accept edits only (file writes approved; shell commands still prompt)",
	"Approve — default mode (all non-read-only calls still prompt)",
	"Reject — return to plan mode",
}

// handlePlanApprovalKey handles keyboard input in the plan-approval overlay.
func (m Model) handlePlanApprovalKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	pa := m.planApproval
	if pa == nil {
		return m, nil
	}

	send := func(d planmodetool.PlanApprovalDecision) (Model, tea.Cmd) {
		reply := pa.reply
		m.planApproval = nil
		m.refreshViewport()
		return m, func() tea.Msg {
			reply <- d
			return nil
		}
	}

	switch msg.String() {
	case "up", "ctrl+p":
		if pa.selected > 0 {
			pa.selected--
		}
	case "down", "ctrl+n":
		if pa.selected < len(planApprovalItems)-1 {
			pa.selected++
		}
	case "home", "g":
		pa.selected = 0
	case "end", "G":
		pa.selected = len(planApprovalItems) - 1
	case "1":
		pa.selected = 0
		m.planApproval = pa
		return send(planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeBypassPermissions})
	case "2":
		pa.selected = 1
		m.planApproval = pa
		return send(planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeAcceptEdits})
	case "3":
		pa.selected = 2
		m.planApproval = pa
		return send(planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeDefault})
	case "4":
		pa.selected = 3
		m.planApproval = pa
		return send(planmodetool.PlanApprovalDecision{Approved: false})
	case "enter", "space":
		switch pa.selected {
		case 0:
			return send(planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeBypassPermissions})
		case 1:
			return send(planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeAcceptEdits})
		case 2:
			return send(planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeDefault})
		case 3:
			return send(planmodetool.PlanApprovalDecision{Approved: false})
		}
	case "esc", "ctrl+c":
		// Treat escape as rejection so the model can revise.
		return send(planmodetool.PlanApprovalDecision{Approved: false})
	}

	m.planApproval = pa
	m.refreshViewport()
	return m, nil
}

// renderPlanApprovalPicker renders the plan-approval picker overlay.
func (m Model) renderPlanApprovalPicker() string {
	pa := m.planApproval
	if pa == nil {
		return ""
	}

	contentW := floatingInnerWidth(m.width, floatingPickerSpec)
	var sb strings.Builder
	sb.WriteString(panelHeader("◆ Plan Approval", contentW) + "\n\n")
	fmt.Fprintf(&sb, "%s\n\n", stylePickerDesc.Render("Choose how to approve this plan:"))

	for i, label := range planApprovalItems {
		num := fmt.Sprintf("%d. ", i+1)
		if i == pa.selected {
			fmt.Fprintf(&sb, "%s\n", stylePickerItemSelected.Render("❯ "+num+label))
		} else {
			fmt.Fprintf(&sb, "%s\n", stylePickerItem.Render("  "+num+label))
		}
	}

	fmt.Fprintf(&sb, "\n%s", stylePickerDesc.Render("↑/↓ navigate · Enter select · 1-4 quick pick · Esc reject"))
	return sb.String()
}
