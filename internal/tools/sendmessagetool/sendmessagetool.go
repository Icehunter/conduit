// Package sendmessagetool implements the SendMessage tool — the mechanism by
// which teammates deliver messages to each other or back to the lead.
//
// Wire compatibility: SendMessage is a CC 2.1.178+ tool available only when
// agent teams are active (team.IsActive()). Registered in internal/app/registry.go.
//
// Sender identity is baked at construction time via NewFor so each teammate's
// tool instance stamps the correct From field without trusting model input.
//
// Divergence from CC: CC uses IPC/OS messaging; conduit routes through
// team.Team.Send (in-process channel, non-blocking, buffered).
package sendmessagetool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/icehunter/conduit/internal/team"
	"github.com/icehunter/conduit/internal/tool"
)

var schema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"recipient": {
			"type": "string",
			"description": "Name of the recipient — a teammate name or \"lead\" for the lead agent."
		},
		"message": {
			"type": "string",
			"description": "The message text to send. For plan-reject, this is the rejection feedback."
		},
		"kind": {
			"type": "string",
			"description": "Optional. Special kinds: plan-approve / plan-reject (lead approves/rejects teammate plan); shutdown-request (lead asks teammate to shut down); shutdown-approve / shutdown-reject (teammate responds to a shutdown request). Omit for regular messages."
		}
	},
	"required": ["recipient", "message"]
}`)

// Tool implements the SendMessage tool for one agent (identified by sender name).
type Tool struct {
	tool.NotDeferrable
	sender string
	t      *team.Team
}

// New returns a SendMessage tool that sends as the lead (ReservedLeadName).
func New(t *team.Team) *Tool {
	return &Tool{sender: team.ReservedLeadName, t: t}
}

// NewFor returns a SendMessage tool that sends under the given name.
// Inject this into each teammate's tool registry via ExtraTools.
func NewFor(name string, t *team.Team) *Tool {
	return &Tool{sender: name, t: t}
}

func (*Tool) Name() string                 { return "SendMessage" }
func (*Tool) Description() string          { return sendMessageDescription }
func (*Tool) InputSchema() json.RawMessage { return schema }

func (*Tool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (*Tool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

type input struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
	Kind      string `json:"kind,omitempty"`
}

func (t *Tool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	if !team.IsActive() {
		return tool.ErrorResult("sendmessage: agent teams are not active"), nil
	}
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("sendmessage: invalid input: %v", err)), nil
	}
	if in.Recipient == "" {
		return tool.ErrorResult("sendmessage: recipient is required"), nil
	}
	if in.Message == "" {
		return tool.ErrorResult("sendmessage: message is required"), nil
	}

	switch in.Kind {
	case "plan-approve":
		if err := t.t.SendPlanDecision(in.Recipient, team.PlanDecision{Approved: true}); err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		return tool.TextResult(fmt.Sprintf("Plan approval sent to %s.", in.Recipient)), nil

	case "plan-reject":
		if err := t.t.SendPlanDecision(in.Recipient, team.PlanDecision{Approved: false, Feedback: in.Message}); err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		return tool.TextResult(fmt.Sprintf("Plan rejection sent to %s.", in.Recipient)), nil

	case "shutdown-request":
		// Lead → teammate: route as KindShutdownRequest into the recipient's inbox.
		// The delivery pump formats it as <team-shutdown-request>.
		if err := t.t.Send(team.Message{
			From: t.sender,
			To:   in.Recipient,
			Kind: team.KindShutdownRequest,
			Text: in.Message,
		}); err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		return tool.TextResult(fmt.Sprintf("Shutdown request sent to %s.", in.Recipient)), nil

	case "shutdown-approve":
		// Teammate → self: write approval to the sender's own ShutdownReply channel.
		if err := t.t.SendShutdownReply(t.sender, true); err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		return tool.TextResult("Shutdown approval sent."), nil

	case "shutdown-reject":
		// Teammate → self: write rejection to the sender's own ShutdownReply channel.
		if err := t.t.SendShutdownReply(t.sender, false); err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		return tool.TextResult("Shutdown rejection sent."), nil

	default:
		if err := t.t.Send(team.Message{
			From: t.sender,
			To:   in.Recipient,
			Text: in.Message,
			Kind: team.KindMessage,
		}); err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		return tool.TextResult(fmt.Sprintf("Message delivered to %s.", in.Recipient)), nil
	}
}

const sendMessageDescription = `Send a message to another agent team member.

Use this tool to communicate with the lead agent or other teammates during a
team session. Messages are delivered non-blocking to the recipient's inbox and
consumed at the next turn boundary.

Recipients: any registered teammate name, or "lead" for the lead agent.`
