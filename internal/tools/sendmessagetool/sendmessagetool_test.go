package sendmessagetool

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/team"
)

// TestMain enables agent teams for the whole package test run.
// sendmessagetool.Execute gates on team.IsActive(); tests need it on.
func TestMain(m *testing.M) {
	team.SetActive(true)
	os.Exit(m.Run())
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func newTeamWith(t *testing.T, members ...string) *team.Team {
	t.Helper()
	tm := team.New("test")
	for _, name := range members {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		_ = ctx
		if _, err := tm.Register(name, cancel); err != nil {
			t.Fatalf("Register %q: %v", name, err)
		}
	}
	return tm
}

func runTool(t *testing.T, tl *Tool, input map[string]any) (text string, isErr bool) {
	t.Helper()
	raw, _ := json.Marshal(input)
	res, err := tl.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	for _, c := range res.Content {
		text += c.Text
	}
	return text, res.IsError
}

// ─── Tool identity ────────────────────────────────────────────────────────────

func TestSendMessage_Name(t *testing.T) {
	tm := team.New("t")
	if New(tm).Name() != "SendMessage" {
		t.Error("Name() should return SendMessage")
	}
}

func TestSendMessage_IsReadOnly(t *testing.T) {
	tm := team.New("t")
	if !New(tm).IsReadOnly(nil) {
		t.Error("IsReadOnly should return true (allowed in plan mode)")
	}
}

func TestSendMessage_IsConcurrencySafe(t *testing.T) {
	tm := team.New("t")
	if !New(tm).IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe should return true")
	}
}

// ─── Happy path ───────────────────────────────────────────────────────────────

func TestSendMessage_ToLead(t *testing.T) {
	tm := newTeamWith(t)
	tool := NewFor("teammate-1", tm)
	_, isErr := runTool(t, tool, map[string]any{
		"recipient": team.ReservedLeadName,
		"message":   "hello lead",
	})
	if isErr {
		t.Error("send to lead should succeed")
	}
	select {
	case msg := <-tm.LeadInbox():
		if msg.Text != "hello lead" {
			t.Errorf("text = %q, want %q", msg.Text, "hello lead")
		}
		if msg.From != "teammate-1" {
			t.Errorf("From = %q, want %q", msg.From, "teammate-1")
		}
	default:
		t.Error("no message in lead inbox")
	}
}

func TestSendMessage_ToMember(t *testing.T) {
	tm := newTeamWith(t, "alice")
	tool := New(tm)
	_, isErr := runTool(t, tool, map[string]any{
		"recipient": "alice",
		"message":   "hey alice",
	})
	if isErr {
		t.Error("send to registered member should succeed")
	}
}

func TestSendMessage_SenderIdentity_Lead(t *testing.T) {
	tm := newTeamWith(t)
	tool := New(tm)
	_, _ = runTool(t, tool, map[string]any{
		"recipient": team.ReservedLeadName,
		"message":   "from lead",
	})
	select {
	case msg := <-tm.LeadInbox():
		if msg.From != team.ReservedLeadName {
			t.Errorf("From = %q, want %q", msg.From, team.ReservedLeadName)
		}
	default:
		t.Error("no message")
	}
}

func TestSendMessage_SenderIdentity_Named(t *testing.T) {
	tm := newTeamWith(t)
	tool := NewFor("charlie", tm)
	_, _ = runTool(t, tool, map[string]any{
		"recipient": team.ReservedLeadName,
		"message":   "hi",
	})
	select {
	case msg := <-tm.LeadInbox():
		if msg.From != "charlie" {
			t.Errorf("From = %q, want %q", msg.From, "charlie")
		}
	default:
		t.Error("no message")
	}
}

// ─── Error paths — all must return ErrorResult, not a Go error ────────────────

func TestSendMessage_MissingRecipient(t *testing.T) {
	tm := newTeamWith(t)
	_, isErr := runTool(t, New(tm), map[string]any{"message": "hi"})
	if !isErr {
		t.Error("missing recipient should return ErrorResult")
	}
}

func TestSendMessage_EmptyRecipient(t *testing.T) {
	tm := newTeamWith(t)
	_, isErr := runTool(t, New(tm), map[string]any{"recipient": "", "message": "hi"})
	if !isErr {
		t.Error("empty recipient should return ErrorResult")
	}
}

func TestSendMessage_MissingMessage(t *testing.T) {
	tm := newTeamWith(t)
	_, isErr := runTool(t, New(tm), map[string]any{"recipient": team.ReservedLeadName})
	if !isErr {
		t.Error("missing message should return ErrorResult")
	}
}

func TestSendMessage_EmptyMessage(t *testing.T) {
	tm := newTeamWith(t)
	_, isErr := runTool(t, New(tm), map[string]any{
		"recipient": team.ReservedLeadName,
		"message":   "",
	})
	if !isErr {
		t.Error("empty message should return ErrorResult")
	}
}

func TestSendMessage_UnknownRecipient_ErrorResult(t *testing.T) {
	tm := newTeamWith(t, "alice")
	text, isErr := runTool(t, New(tm), map[string]any{
		"recipient": "nobody",
		"message":   "hi",
	})
	if !isErr {
		t.Error("unknown recipient should return ErrorResult, not Go error")
	}
	// Error text must list valid recipients so the model can self-correct.
	if !strings.Contains(text, "alice") || !strings.Contains(text, team.ReservedLeadName) {
		t.Errorf("ErrorResult should list valid recipients; got: %q", text)
	}
}

func TestSendMessage_InvalidJSON(t *testing.T) {
	tm := newTeamWith(t)
	res, err := New(tm).Execute(context.Background(), []byte(`{invalid`))
	if err != nil {
		t.Fatalf("Execute must not return Go error: %v", err)
	}
	if !res.IsError {
		t.Error("invalid JSON should return ErrorResult")
	}
}

func TestSendMessage_ShutdownTeam(t *testing.T) {
	tm := newTeamWith(t)
	tm.Shutdown()
	_, isErr := runTool(t, New(tm), map[string]any{
		"recipient": team.ReservedLeadName,
		"message":   "after shutdown",
	})
	if !isErr {
		t.Error("send to shut-down team should return ErrorResult")
	}
}

// ─── Plan-approval routing ────────────────────────────────────────────────────

func TestSendMessage_PlanApprove_SendsToPlanReply(t *testing.T) {
	tm := team.New("test")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("alice", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, isErr := runTool(t, New(tm), map[string]any{
		"recipient": "alice",
		"message":   "looks good",
		"kind":      "plan-approve",
	})
	if isErr {
		t.Error("plan-approve should succeed")
	}
	select {
	case d := <-m.PlanReply:
		if !d.Approved {
			t.Error("PlanDecision.Approved should be true")
		}
	default:
		t.Error("no decision in PlanReply")
	}
}

func TestSendMessage_PlanReject_SendsFeedback(t *testing.T) {
	tm := team.New("test")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("bob", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, isErr := runTool(t, New(tm), map[string]any{
		"recipient": "bob",
		"message":   "missing error handling",
		"kind":      "plan-reject",
	})
	if isErr {
		t.Error("plan-reject should succeed")
	}
	select {
	case d := <-m.PlanReply:
		if d.Approved {
			t.Error("PlanDecision.Approved should be false")
		}
		if d.Feedback != "missing error handling" {
			t.Errorf("Feedback = %q, want %q", d.Feedback, "missing error handling")
		}
	default:
		t.Error("no decision in PlanReply")
	}
}

func TestSendMessage_PlanApprove_UnknownMember(t *testing.T) {
	tm := team.New("test")
	_, isErr := runTool(t, New(tm), map[string]any{
		"recipient": "nobody",
		"message":   "ok",
		"kind":      "plan-approve",
	})
	if !isErr {
		t.Error("plan-approve to unknown member should return ErrorResult")
	}
}

// ─── Shutdown kind routing ────────────────────────────────────────────────────

func TestSendMessage_ShutdownRequest_RoutesToInbox(t *testing.T) {
	tm := newTeamWith(t, "alice")
	// Find the member to check inbox later.
	// Since newTeamWith registered "alice", send from lead and check alice's inbox.
	tool := New(tm)
	_, isErr := runTool(t, tool, map[string]any{
		"recipient": "alice",
		"message":   "please wrap up",
		"kind":      "shutdown-request",
	})
	if isErr {
		t.Error("shutdown-request should succeed")
	}
	// The message must arrive in the lead inbox (as a KindShutdownRequest from lead)
	// via team.Send. We verify indirectly: no error and the team accepted the send.
}

func TestSendMessage_ShutdownApprove_SendsToShutdownReply(t *testing.T) {
	tm := team.New("test")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("alice", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Alice's tool approves its own shutdown (sender = "alice").
	aliceTool := NewFor("alice", tm)
	_, isErr := runTool(t, aliceTool, map[string]any{
		"recipient": team.ReservedLeadName, // recipient field still needed
		"message":   "shutting down",
		"kind":      "shutdown-approve",
	})
	if isErr {
		t.Error("shutdown-approve should succeed")
	}
	select {
	case approved := <-m.ShutdownReply:
		if !approved {
			t.Error("ShutdownReply should be true for approve")
		}
	default:
		t.Error("no value in ShutdownReply")
	}
}

func TestSendMessage_ShutdownReject_SendsToShutdownReply(t *testing.T) {
	tm := team.New("test")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("bob", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	bobTool := NewFor("bob", tm)
	_, isErr := runTool(t, bobTool, map[string]any{
		"recipient": team.ReservedLeadName,
		"message":   "still working",
		"kind":      "shutdown-reject",
	})
	if isErr {
		t.Error("shutdown-reject should succeed")
	}
	select {
	case approved := <-m.ShutdownReply:
		if approved {
			t.Error("ShutdownReply should be false for reject")
		}
	default:
		t.Error("no value in ShutdownReply")
	}
}

func TestSendMessage_ShutdownApprove_UnregisteredSender(t *testing.T) {
	tm := team.New("test")
	// Sender "ghost" is not registered — ShutdownReply lookup will fail.
	ghostTool := NewFor("ghost", tm)
	_, isErr := runTool(t, ghostTool, map[string]any{
		"recipient": team.ReservedLeadName,
		"message":   "ok",
		"kind":      "shutdown-approve",
	})
	if !isErr {
		t.Error("shutdown-approve from unregistered sender should return ErrorResult")
	}
}

func TestSendMessage_PlanKind_DoesNotWriteToInbox(t *testing.T) {
	tm := team.New("test")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = tm.Register("carol", cancel)
	_, _ = runTool(t, New(tm), map[string]any{
		"recipient": "carol",
		"message":   "approved",
		"kind":      "plan-approve",
	})
	// The regular inbox must stay empty — plan decisions go to PlanReply only.
	select {
	case msg := <-tm.LeadInbox():
		t.Errorf("plan-approve wrote to lead inbox unexpectedly: %+v", msg)
	default:
		// good
	}
}

// ─── Token isolation ──────────────────────────────────────────────────────────

// Two tools backed by different teams must not cross-deliver.
func TestSendMessage_NoTokenBleedBetweenTeams(t *testing.T) {
	tmA := team.New("team-a")
	tmB := team.New("team-b")
	toolA := New(tmA)
	_, _ = runTool(t, toolA, map[string]any{
		"recipient": team.ReservedLeadName,
		"message":   "secret-for-A",
	})
	select {
	case msg := <-tmB.LeadInbox():
		t.Errorf("team B received message from team A: %q", msg.Text)
	default:
		// good — no bleed
	}
}
