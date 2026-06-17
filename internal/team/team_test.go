package team

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── SessionName ──────────────────────────────────────────────────────────────

func TestSessionName(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		want      string
	}{
		{"long id truncated to 8", "abcdef1234567890", "team-abcdef12"},
		{"exactly 8 chars", "12345678", "team-12345678"},
		{"short id kept whole", "abc", "team-abc"},
		{"empty id", "", "team-"},
		{"nine chars truncated", "123456789", "team-12345678"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SessionName(tt.sessionID); got != tt.want {
				t.Errorf("SessionName(%q) = %q, want %q", tt.sessionID, got, tt.want)
			}
		})
	}
}

func TestSessionName_Deterministic(t *testing.T) {
	id := "fixed-session-id"
	a := SessionName(id)
	b := SessionName(id)
	if a != b {
		t.Errorf("SessionName not deterministic: %q != %q", a, b)
	}
}

// ─── New / Register / Unregister ─────────────────────────────────────────────

func TestTeam_New(t *testing.T) {
	tm := New("test-team")
	if tm == nil {
		t.Fatal("New returned nil")
	}
}

func TestTeam_RegisterSucceeds(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("teammate-1", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if m == nil {
		t.Fatal("Register returned nil member")
	}
	if m.Name != "teammate-1" {
		t.Errorf("Name = %q, want %q", m.Name, "teammate-1")
	}
	if m.Inbox == nil {
		t.Error("Inbox must be non-nil")
	}
	if m.PlanReply == nil {
		t.Error("PlanReply must be non-nil")
	}
	if m.ShutdownReply == nil {
		t.Error("ShutdownReply must be non-nil")
	}
}

func TestTeam_RegisterDuplicate(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := tm.Register("dup", cancel); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if _, err := tm.Register("dup", cancel); err == nil {
		t.Error("duplicate Register should return error")
	}
}

func TestTeam_UnregisterUnknown(t *testing.T) {
	tm := New("t")
	// must not panic
	tm.Unregister("does-not-exist")
}

func TestTeam_UnregisterRemovesMember(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := tm.Register("m1", cancel); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tm.Unregister("m1")
	for _, n := range tm.Names() {
		if n == "m1" {
			t.Error("Unregister did not remove member from Names()")
		}
	}
}

func TestTeam_RegisterAfterUnregister(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := tm.Register("recyclable", cancel); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	tm.Unregister("recyclable")
	if _, err := tm.Register("recyclable", cancel); err != nil {
		t.Errorf("re-Register after Unregister should succeed: %v", err)
	}
}

// ─── Names ────────────────────────────────────────────────────────────────────

func TestTeam_NamesAlwaysIncludesLead(t *testing.T) {
	tm := New("t")
	found := false
	for _, n := range tm.Names() {
		if n == ReservedLeadName {
			found = true
		}
	}
	if !found {
		t.Errorf("Names() does not include %q", ReservedLeadName)
	}
}

func TestTeam_NamesSorted(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, n := range []string{"charlie", "alpha", "bravo"} {
		if _, err := tm.Register(n, cancel); err != nil {
			t.Fatalf("Register %q: %v", n, err)
		}
	}
	names := tm.Names()
	if !sort.StringsAreSorted(names) {
		t.Errorf("Names() not sorted: %v", names)
	}
}

func TestTeam_NamesIncludesMembers(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = tm.Register("alice", cancel)
	_, _ = tm.Register("bob", cancel)
	names := tm.Names()
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	for _, want := range []string{ReservedLeadName, "alice", "bob"} {
		if !nameSet[want] {
			t.Errorf("Names() missing %q", want)
		}
	}
}

// ─── Send ─────────────────────────────────────────────────────────────────────

func TestTeam_SendToLead(t *testing.T) {
	tm := New("t")
	if err := tm.Send(Message{From: "m1", To: ReservedLeadName, Text: "hello lead", Kind: KindMessage}); err != nil {
		t.Fatalf("Send to lead: %v", err)
	}
	select {
	case msg := <-tm.LeadInbox():
		if msg.Text != "hello lead" {
			t.Errorf("got text %q, want %q", msg.Text, "hello lead")
		}
		if msg.From != "m1" {
			t.Errorf("got From %q, want %q", msg.From, "m1")
		}
		if msg.Kind != KindMessage {
			t.Errorf("got Kind %q, want %q", msg.Kind, KindMessage)
		}
	default:
		t.Error("LeadInbox should have a message")
	}
}

func TestTeam_SendToMember(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("receiver", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := tm.Send(Message{From: ReservedLeadName, To: "receiver", Text: "hey receiver"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case msg := <-m.Inbox:
		if msg.Text != "hey receiver" {
			t.Errorf("got %q, want %q", msg.Text, "hey receiver")
		}
		if msg.From != ReservedLeadName {
			t.Errorf("From = %q, want %q", msg.From, ReservedLeadName)
		}
	default:
		t.Error("member Inbox should have a message")
	}
}

func TestTeam_SendToUnknown(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = tm.Register("known", cancel)

	err := tm.Send(Message{From: "lead", To: "no-such-member", Text: "hi"})
	if err == nil {
		t.Fatal("Send to unknown should return error")
	}
	// Error must list valid recipients so the model can self-correct.
	if !strings.Contains(err.Error(), "known") {
		t.Errorf("error %q should mention %q", err.Error(), "known")
	}
	if !strings.Contains(err.Error(), ReservedLeadName) {
		t.Errorf("error %q should mention %q", err.Error(), ReservedLeadName)
	}
}

func TestTeam_SendStampsTime(t *testing.T) {
	tm := New("t")
	before := time.Now()
	_ = tm.Send(Message{To: ReservedLeadName, Text: "timestamp test"})
	after := time.Now()

	select {
	case msg := <-tm.LeadInbox():
		if msg.SentAt.Before(before) || msg.SentAt.After(after) {
			t.Errorf("SentAt %v outside window [%v, %v]", msg.SentAt, before, after)
		}
	default:
		t.Error("no message in lead inbox")
	}
}

func TestTeam_SendPreservesAllFields(t *testing.T) {
	tm := New("t")
	_ = tm.Send(Message{From: "alice", To: ReservedLeadName, Text: "body", Kind: KindShutdownRequest})
	select {
	case msg := <-tm.LeadInbox():
		if msg.From != "alice" || msg.To != ReservedLeadName || msg.Text != "body" || msg.Kind != KindShutdownRequest {
			t.Errorf("fields not preserved: %+v", msg)
		}
	default:
		t.Error("no message")
	}
}

// ─── Buffered / overflow ──────────────────────────────────────────────────────

func TestTeam_InboxBuffered_NoBlock(t *testing.T) {
	tm := New("t")
	done := make(chan struct{})
	go func() {
		// Fill the entire lead inbox capacity without blocking.
		for range InboxBufferSize {
			if err := tm.Send(Message{To: ReservedLeadName, Text: "msg"}); err != nil {
				// Premature error — report but don't hang.
				break
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("Send to non-full inbox blocked (deadlock)")
	}
}

func TestTeam_SendToFullInbox_ReturnsError(t *testing.T) {
	tm := New("t")
	// Fill to capacity.
	for i := range InboxBufferSize {
		if err := tm.Send(Message{To: ReservedLeadName, Text: "fill"}); err != nil {
			t.Fatalf("unexpected error filling inbox at %d: %v", i, err)
		}
	}
	// One more must fail non-blocking.
	err := tm.Send(Message{To: ReservedLeadName, Text: "overflow"})
	if err == nil {
		t.Error("Send to full inbox should return error (not block)")
	}
}

func TestTeam_MemberInboxBuffered(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = tm.Register("m", cancel)
	for i := range InboxBufferSize {
		if err := tm.Send(Message{To: "m", Text: "fill"}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if err := tm.Send(Message{To: "m", Text: "over"}); err == nil {
		t.Error("should error when member inbox is full")
	}
}

// ─── LeadInbox ────────────────────────────────────────────────────────────────

func TestTeam_LeadInbox_ReceiveChannel(t *testing.T) {
	tm := New("t")
	ch := tm.LeadInbox()
	if ch == nil {
		t.Error("LeadInbox() returned nil")
	}
}

// ─── Shutdown ─────────────────────────────────────────────────────────────────

func TestTeam_ShutdownIdempotent(t *testing.T) {
	tm := New("t")
	tm.Shutdown()
	tm.Shutdown() // must not panic
	tm.Shutdown()
}

func TestTeam_ShutdownCancelsAllMembers(t *testing.T) {
	tm := New("t")
	const n = 5
	ctxs := make([]context.Context, n)
	cancels := make([]context.CancelFunc, n)
	for i := range ctxs {
		ctxs[i], cancels[i] = context.WithCancel(context.Background())
	}
	for i := range n {
		if _, err := tm.Register(fmt.Sprintf("m%d", i), cancels[i]); err != nil {
			t.Fatalf("Register m%d: %v", i, err)
		}
	}

	tm.Shutdown()

	for i, ctx := range ctxs {
		select {
		case <-ctx.Done():
			// good
		case <-time.After(200 * time.Millisecond):
			t.Errorf("Shutdown did not cancel context for m%d", i)
		}
	}
}

func TestTeam_SendAfterShutdown(t *testing.T) {
	tm := New("t")
	tm.Shutdown()
	if err := tm.Send(Message{To: ReservedLeadName, Text: "after shutdown"}); err == nil {
		t.Error("Send after Shutdown should return error")
	}
}

func TestTeam_RegisterAfterShutdown(t *testing.T) {
	tm := New("t")
	tm.Shutdown()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := tm.Register("late", cancel); err == nil {
		t.Error("Register after Shutdown should return error")
	}
}

// ─── Token isolation (no shared state between teams) ─────────────────────────

// TestTeam_Isolation verifies that two independent teams do not share mailboxes.
// This is the structural guarantee against cross-team token bleed.
func TestTeam_Isolation(t *testing.T) {
	tmA := New("team-a")
	tmB := New("team-b")

	_ = tmA.Send(Message{To: ReservedLeadName, Text: "for A only"})

	// B's lead inbox must be empty.
	select {
	case msg := <-tmB.LeadInbox():
		t.Errorf("team B received message intended for team A: %q", msg.Text)
	default:
		// good: no bleed
	}
}

func TestTeam_MemberIsolation(t *testing.T) {
	tmA := New("team-a")
	tmB := New("team-b")
	_, cancelA := context.WithCancel(context.Background())
	_, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()

	mA, err := tmA.Register("shared-name", cancelA)
	if err != nil {
		t.Fatalf("Register in A: %v", err)
	}
	mB, err := tmB.Register("shared-name", cancelB)
	if err != nil {
		t.Fatalf("Register in B: %v", err)
	}

	_ = tmA.Send(Message{To: "shared-name", Text: "for A"})

	select {
	case msg := <-mA.Inbox:
		if msg.Text != "for A" {
			t.Errorf("A got %q", msg.Text)
		}
	default:
		t.Error("A should have received its message")
	}
	select {
	case msg := <-mB.Inbox:
		t.Errorf("B received message intended for A: %q", msg.Text)
	default:
		// good
	}
}

// ─── Concurrent access (race detector) ───────────────────────────────────────

func TestTeam_ConcurrentSend(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = tm.Register("target", cancel)

	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			_ = tm.Send(Message{From: "lead", To: "target", Text: "concurrent"})
		})
	}
	wg.Wait()
}

func TestTeam_ConcurrentRegisterUnregister(t *testing.T) {
	tm := New("t")
	var wg sync.WaitGroup
	for i := range 10 {
		name := fmt.Sprintf("m%d", i)
		wg.Go(func() {
			_, c := context.WithCancel(context.Background())
			defer c()
			_, _ = tm.Register(name, c)
			tm.Unregister(name)
		})
	}
	wg.Wait()
}

func TestTeam_ConcurrentShutdownAndSend(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = tm.Register("m1", cancel)

	var wg sync.WaitGroup
	wg.Go(func() { tm.Shutdown() })
	wg.Go(func() {
		// Ignore error — just must not race or panic.
		_ = tm.Send(Message{To: ReservedLeadName, Text: "race"})
	})
	wg.Wait()
}

// ─── Default and Reset ───────────────────────────────────────────────────────

func TestReset_ReplacesDefault(t *testing.T) {
	old := Default
	Reset("new-session-id-xyz")
	if Default == old {
		t.Error("Reset should replace Default with a new Team")
	}
	// Restore so other tests using Default are unaffected.
	Default = old
}

func TestReset_OldTeamShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register a member in the old Default.
	old := New("old")
	_, _ = old.Register("m1", cancel)
	Default = old

	Reset("fresh-session")
	defer func() { Default = New("restored") }()

	// Old team should be shut down after Reset.
	select {
	case <-ctx.Done():
		// good
	case <-time.After(200 * time.Millisecond):
		t.Error("Reset did not shut down the old team's members")
	}
}

// ─── SendPlanDecision ──────────────────────────────────────────────────────────

func TestTeam_SendPlanDecision_Approved(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("alice", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := tm.SendPlanDecision("alice", PlanDecision{Approved: true}); err != nil {
		t.Fatalf("SendPlanDecision: %v", err)
	}
	select {
	case d := <-m.PlanReply:
		if !d.Approved {
			t.Error("Approved should be true")
		}
	default:
		t.Error("no decision in PlanReply")
	}
}

func TestTeam_SendPlanDecision_Rejected(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("bob", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := tm.SendPlanDecision("bob", PlanDecision{Approved: false, Feedback: "needs work"}); err != nil {
		t.Fatalf("SendPlanDecision: %v", err)
	}
	select {
	case d := <-m.PlanReply:
		if d.Approved {
			t.Error("Approved should be false")
		}
		if d.Feedback != "needs work" {
			t.Errorf("Feedback = %q, want %q", d.Feedback, "needs work")
		}
	default:
		t.Error("no decision in PlanReply")
	}
}

func TestTeam_SendPlanDecision_UnknownMember(t *testing.T) {
	tm := New("t")
	if err := tm.SendPlanDecision("ghost", PlanDecision{Approved: true}); err == nil {
		t.Error("SendPlanDecision to unknown member should return error")
	}
}

func TestTeam_SendPlanDecision_BufferFull(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := tm.Register("carol", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// First send fills the buffer.
	if err := tm.SendPlanDecision("carol", PlanDecision{Approved: true}); err != nil {
		t.Fatalf("first SendPlanDecision: %v", err)
	}
	// Second send must fail (buffer full, not block).
	if err := tm.SendPlanDecision("carol", PlanDecision{Approved: false}); err == nil {
		t.Error("second SendPlanDecision should return error when buffer is full")
	}
}

// ─── SendShutdownReply ────────────────────────────────────────────────────────

func TestTeam_SendShutdownReply_Approved(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("alice", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := tm.SendShutdownReply("alice", true); err != nil {
		t.Fatalf("SendShutdownReply: %v", err)
	}
	select {
	case approved := <-m.ShutdownReply:
		if !approved {
			t.Error("expected approved=true")
		}
	default:
		t.Error("no value in ShutdownReply")
	}
}

func TestTeam_SendShutdownReply_Rejected(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := tm.Register("bob", cancel)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := tm.SendShutdownReply("bob", false); err != nil {
		t.Fatalf("SendShutdownReply: %v", err)
	}
	select {
	case approved := <-m.ShutdownReply:
		if approved {
			t.Error("expected approved=false")
		}
	default:
		t.Error("no value in ShutdownReply")
	}
}

func TestTeam_SendShutdownReply_UnknownMember(t *testing.T) {
	tm := New("t")
	if err := tm.SendShutdownReply("ghost", true); err == nil {
		t.Error("SendShutdownReply to unknown member should return error")
	}
}

func TestTeam_SendShutdownReply_BufferFull(t *testing.T) {
	tm := New("t")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := tm.Register("carol", cancel); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := tm.SendShutdownReply("carol", true); err != nil {
		t.Fatalf("first SendShutdownReply: %v", err)
	}
	if err := tm.SendShutdownReply("carol", false); err == nil {
		t.Error("second SendShutdownReply should error when buffer full")
	}
}
