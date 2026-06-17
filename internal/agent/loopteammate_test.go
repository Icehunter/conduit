package agent

// Tests for Phase 3b: async teammate spawn via SpawnTeammate.
//
// Delivery correctness (message → InjectMessage path) is verified by
// TestDeliveryPump_* tests that call runDeliveryPump directly. The
// SpawnTeammate integration tests verify registration, completion signalling,
// cleanup, and token isolation — all without relying on pump timing.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/team"
	"github.com/icehunter/conduit/internal/tool"
)

// makeTeamParentLoop returns a parent loop backed by a test server using srvFn.
// The server is closed via t.Cleanup.
func makeTeamParentLoop(t *testing.T, srvFn http.HandlerFunc) *Loop {
	t.Helper()
	reg := tool.NewRegistry()
	srv := httptest.NewServer(srvFn)
	t.Cleanup(srv.Close)
	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "test"}, srv.Client())
	return NewLoop(c, reg, LoopConfig{Model: "m", MaxTokens: 128})
}

// drainLeadInbox reads one completion message from tm's lead inbox, failing on timeout.
func drainLeadInbox(t *testing.T, tm *team.Team) {
	t.Helper()
	select {
	case <-tm.LeadInbox():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout draining lead inbox")
	}
}

// ─── SpawnTeammate integration ────────────────────────────────────────────────

// TestSpawnTeammate_RegistersInTeam: after SpawnTeammate returns the teammate is in the team.
func TestSpawnTeammate_RegistersInTeam(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	if _, err := lp.SpawnTeammate(context.Background(), "alice", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}
	if !slices.Contains(tm.Names(), "alice") {
		t.Errorf("alice not in team.Names() = %v", tm.Names())
	}
	drainLeadInbox(t, tm)
}

// TestSpawnTeammate_DuplicateName_Error: registering the same name twice errors immediately.
func TestSpawnTeammate_DuplicateName_Error(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	if _, err := lp.SpawnTeammate(context.Background(), "alice", "w1", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("first SpawnTeammate: %v", err)
	}
	if _, err := lp.SpawnTeammate(context.Background(), "alice", "w2", SubAgentSpec{}, tm); err == nil {
		t.Error("second SpawnTeammate with duplicate name should return error")
	}
	drainLeadInbox(t, tm)
}

// TestSpawnTeammate_CompletionReachesLead: when child loop finishes, lead gets KindCompletion.
func TestSpawnTeammate_CompletionReachesLead(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	if _, err := lp.SpawnTeammate(context.Background(), "alice", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	select {
	case msg := <-tm.LeadInbox():
		if msg.Kind != team.KindCompletion {
			t.Errorf("expected KindCompletion; got %q", msg.Kind)
		}
		if msg.From != "alice" {
			t.Errorf("From = %q; want %q", msg.From, "alice")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for completion message from teammate")
	}
}

// TestSpawnTeammate_UnregistersOnCompletion: after the completion message arrives,
// the teammate is no longer in the team. (Unregister happens before Send.)
func TestSpawnTeammate_UnregistersOnCompletion(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	if _, err := lp.SpawnTeammate(context.Background(), "alice", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	select {
	case <-tm.LeadInbox():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	if slices.Contains(tm.Names(), "alice") {
		t.Error("alice still in team after completion (Unregister not called)")
	}
}

// TestSpawnTeammate_SubagentRemovedOnCompletion: teammate removed from the global
// subagent tracker once the child loop returns.
func TestSpawnTeammate_SubagentRemovedOnCompletion(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	agentID, err := lp.SpawnTeammate(context.Background(), "alice", "work", SubAgentSpec{}, tm)
	if err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	drainLeadInbox(t, tm)

	for _, e := range subagent.Default.SnapshotAll() {
		if e.ID == agentID && e.IsRunning() {
			t.Error("teammate still active in subagent tracker after completion")
		}
	}
}

// TestSpawnTeammate_FireAndForget: SpawnTeammate returns before the child loop finishes.
func TestSpawnTeammate_FireAndForget(t *testing.T) {
	tm := team.New("test")
	block := make(chan struct{})
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	spawnDone := make(chan struct{})
	go func() {
		_, _ = lp.SpawnTeammate(context.Background(), "alice", "work", SubAgentSpec{}, tm)
		close(spawnDone)
	}()

	select {
	case <-spawnDone:
		// SpawnTeammate returned while server is blocked — confirmed fire-and-forget.
	case <-time.After(1 * time.Second):
		t.Error("SpawnTeammate blocked (should be non-blocking)")
	}

	close(block)
	drainLeadInbox(t, tm)
}

// TestSpawnTeammate_ContextCancel: when the parent context is already cancelled
// before the child loop makes any HTTP request, the completion message still
// reaches lead. Pre-cancelling avoids any blocking-server timing issues.
func TestSpawnTeammate_ContextCancel(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called — context was pre-cancelled")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before spawn — child.Run sees a done context immediately

	if _, err := lp.SpawnTeammate(ctx, "bob", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	select {
	case msg := <-tm.LeadInbox():
		if msg.Kind != team.KindCompletion {
			t.Errorf("expected KindCompletion after cancel; got %q", msg.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for pre-cancelled teammate's completion message")
	}
}

// TestSpawnTeammate_TokenIsolation: two independent teams keep their members isolated.
func TestSpawnTeammate_TokenIsolation(t *testing.T) {
	tmA := team.New("team-a")
	tmB := team.New("team-b")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	if _, err := lp.SpawnTeammate(context.Background(), "alice", "do", SubAgentSpec{}, tmA); err != nil {
		t.Fatalf("spawn alice: %v", err)
	}
	if _, err := lp.SpawnTeammate(context.Background(), "bob", "do", SubAgentSpec{}, tmB); err != nil {
		t.Fatalf("spawn bob: %v", err)
	}

	if !slices.Contains(tmA.Names(), "alice") {
		t.Error("alice not in team A")
	}
	if slices.Contains(tmB.Names(), "alice") {
		t.Error("alice bled into team B (token isolation failure)")
	}
	if !slices.Contains(tmB.Names(), "bob") {
		t.Error("bob not in team B")
	}
	if slices.Contains(tmA.Names(), "bob") {
		t.Error("bob bled into team A (token isolation failure)")
	}

	drainLeadInbox(t, tmA)
	drainLeadInbox(t, tmB)
}

// ─── runDeliveryPump unit tests ───────────────────────────────────────────────

// TestDeliveryPump_InjectsMessages: pump reads messages from closed inbox and injects them.
func TestDeliveryPump_InjectsMessages(t *testing.T) {
	child := &Loop{}
	inbox := make(chan team.Message, 4)
	inbox <- team.Message{From: "lead", Text: "msg1", Kind: team.KindMessage}
	inbox <- team.Message{From: "lead", Text: "msg2", Kind: team.KindMessage}
	close(inbox) // pump exits when inbox is drained and closed (ok=false)

	runDeliveryPump(child, inbox, make(chan struct{}))

	child.msgMu.Lock()
	n := len(child.msgQueue)
	child.msgMu.Unlock()
	if n != 2 {
		t.Errorf("expected 2 queued messages; got %d", n)
	}
}

// TestDeliveryPump_ExitsOnDone: pump exits when done is closed.
func TestDeliveryPump_ExitsOnDone(t *testing.T) {
	child := &Loop{}
	inbox := make(chan team.Message, 64)
	done := make(chan struct{})

	finished := make(chan struct{})
	go func() {
		runDeliveryPump(child, inbox, done)
		close(finished)
	}()

	close(done)

	select {
	case <-finished:
	case <-time.After(1 * time.Second):
		t.Error("runDeliveryPump did not exit after done was closed")
	}
}

// TestDeliveryPump_FormatsMessageXML: injected text wraps sender+content in XML.
func TestDeliveryPump_FormatsMessageXML(t *testing.T) {
	child := &Loop{}
	inbox := make(chan team.Message, 1)
	inbox <- team.Message{From: "boss", Text: "hello there", Kind: team.KindMessage}
	close(inbox)

	runDeliveryPump(child, inbox, make(chan struct{}))

	child.msgMu.Lock()
	q := child.msgQueue
	child.msgMu.Unlock()
	if len(q) != 1 {
		t.Fatalf("expected 1 queued message; got %d", len(q))
	}
	if !strings.Contains(q[0], "boss") || !strings.Contains(q[0], "hello there") {
		t.Errorf("formatted message missing from/text: %q", q[0])
	}
}
