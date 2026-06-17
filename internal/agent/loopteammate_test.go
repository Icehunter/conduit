package agent

// Tests for Phase 3b + 3d: async teammate spawn, idle, and self-claim.
//
// Delivery correctness (message → InjectMessage path) is verified by
// TestDeliveryPump_* tests that call runDeliveryPump directly. The
// SpawnTeammate integration tests verify registration, completion signalling,
// cleanup, token isolation, idle signalling, and self-claim semantics.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/team"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/tasktool"
)

// makeTeamParentLoop returns a parent loop backed by a test server using srvFn.
// The server is closed via t.Cleanup.
func makeTeamParentLoop(t *testing.T, srvFn http.HandlerFunc) *Loop {
	t.Helper()
	return makeTeamParentLoopWithStore(t, srvFn, nil)
}

// makeTeamParentLoopWithStore is like makeTeamParentLoop but configures a
// custom TaskStore for Phase 3d (idle/self-claim) tests.
func makeTeamParentLoopWithStore(t *testing.T, srvFn http.HandlerFunc, store *tasktool.Store) *Loop {
	t.Helper()
	reg := tool.NewRegistry()
	srv := httptest.NewServer(srvFn)
	t.Cleanup(srv.Close)
	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "test"}, srv.Client())
	return NewLoop(c, reg, LoopConfig{Model: "m", MaxTokens: 128, TaskStore: store})
}

// waitForKind blocks on tm's lead inbox until a message with the given kind
// arrives or the timeout expires.
func waitForKind(t *testing.T, tm *team.Team, kind team.MessageKind) team.Message {
	t.Helper()
	select {
	case msg := <-tm.LeadInbox():
		if msg.Kind != kind {
			t.Errorf("expected %q; got %q (text: %q)", kind, msg.Kind, msg.Text)
		}
		return msg
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %q message", kind)
		return team.Message{}
	}
}

// ─── SpawnTeammate integration ────────────────────────────────────────────────
//
// Lifecycle with 3d idle/self-claim: after a normal run, the goroutine sends
// KindIdle then parks until the context is cancelled (or the inbox receives a
// message). Cancelling the context causes the goroutine to exit → KindCompletion.
// Tests use this pattern:
//   ctx, cancel := context.WithCancel(...)
//   defer cancel()
//   SpawnTeammate(ctx, ...)
//   waitForKind(t, tm, team.KindIdle)
//   cancel()
//   waitForKind(t, tm, team.KindCompletion)

// TestSpawnTeammate_RegistersInTeam: after SpawnTeammate returns the teammate is in the team.
func TestSpawnTeammate_RegistersInTeam(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "alice", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}
	if !slices.Contains(tm.Names(), "alice") {
		t.Errorf("alice not in team.Names() = %v", tm.Names())
	}
	waitForKind(t, tm, team.KindIdle)
	cancel()
	waitForKind(t, tm, team.KindCompletion)
}

// TestSpawnTeammate_DuplicateName_Error: registering the same name twice errors immediately.
func TestSpawnTeammate_DuplicateName_Error(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "alice", "w1", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("first SpawnTeammate: %v", err)
	}
	if _, err := lp.SpawnTeammate(ctx, "alice", "w2", SubAgentSpec{}, tm); err == nil {
		t.Error("second SpawnTeammate with duplicate name should return error")
	}
	waitForKind(t, tm, team.KindIdle)
	cancel()
	waitForKind(t, tm, team.KindCompletion)
}

// TestSpawnTeammate_CompletionReachesLead: when a run finishes and idle is
// cancelled, lead gets KindCompletion with the correct From field.
func TestSpawnTeammate_CompletionReachesLead(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "alice", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	waitForKind(t, tm, team.KindIdle)
	cancel()

	msg := waitForKind(t, tm, team.KindCompletion)
	if msg.From != "alice" {
		t.Errorf("From = %q; want %q", msg.From, "alice")
	}
}

// TestSpawnTeammate_UnregistersOnCompletion: after KindCompletion arrives,
// the teammate is no longer in the team. (Unregister happens before Send.)
func TestSpawnTeammate_UnregistersOnCompletion(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "alice", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	waitForKind(t, tm, team.KindIdle)
	cancel()
	waitForKind(t, tm, team.KindCompletion)

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentID, err := lp.SpawnTeammate(ctx, "alice", "work", SubAgentSpec{}, tm)
	if err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	waitForKind(t, tm, team.KindIdle)
	cancel()
	waitForKind(t, tm, team.KindCompletion)

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	spawnDone := make(chan struct{})
	go func() {
		_, _ = lp.SpawnTeammate(ctx, "alice", "work", SubAgentSpec{}, tm)
		close(spawnDone)
	}()

	select {
	case <-spawnDone:
		// SpawnTeammate returned while server is blocked — confirmed fire-and-forget.
	case <-time.After(1 * time.Second):
		t.Error("SpawnTeammate blocked (should be non-blocking)")
	}

	close(block)
	waitForKind(t, tm, team.KindIdle)
	cancel()
	waitForKind(t, tm, team.KindCompletion)
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

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	if _, err := lp.SpawnTeammate(ctxA, "alice", "do", SubAgentSpec{}, tmA); err != nil {
		t.Fatalf("spawn alice: %v", err)
	}
	if _, err := lp.SpawnTeammate(ctxB, "bob", "do", SubAgentSpec{}, tmB); err != nil {
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

	waitForKind(t, tmA, team.KindIdle)
	cancelA()
	waitForKind(t, tmA, team.KindCompletion)

	waitForKind(t, tmB, team.KindIdle)
	cancelB()
	waitForKind(t, tmB, team.KindCompletion)
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

// ─── Phase 3d: idle + self-claim ─────────────────────────────────────────────
//
// Pattern: pass a cancellable context to SpawnTeammate. SpawnTeammate wraps it
// in a child context (tmCtx). Cancelling the outer ctx cancels tmCtx, which
// unblocks the idle select and causes the goroutine to exit → KindCompletion.

// TestSpawnTeammate_IdleWhenNoTask: when a run finishes with no claimable task,
// teammate sends KindIdle then parks. Cancelling the context produces KindCompletion.
func TestSpawnTeammate_IdleWhenNoTask(t *testing.T) {
	tm := team.New("test")
	store := tasktool.NewStore() // empty — no claimable tasks

	lp := makeTeamParentLoopWithStore(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	}, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "alice", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	waitForKind(t, tm, team.KindIdle)

	// Cancel outer ctx → tmCtx Done → goroutine exits → KindCompletion.
	cancel()
	waitForKind(t, tm, team.KindCompletion)
}

// TestSpawnTeammate_SelfClaimsNextTask: when a claimable task exists after a run,
// the teammate claims it and runs again before going idle.
func TestSpawnTeammate_SelfClaimsNextTask(t *testing.T) {
	tm := team.New("test")
	store := tasktool.NewStore()

	// Create a claimable task (no assignee, no deps) before spawning.
	task := store.Create("do the thing", "detailed description", "", nil)

	var calls atomic.Int32
	lp := makeTeamParentLoopWithStore(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	}, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "worker", "initial task", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	// Two runs expected: initial + self-claimed task. After both, no tasks
	// remain claimable → KindIdle.
	waitForKind(t, tm, team.KindIdle)

	if n := calls.Load(); n < 2 {
		t.Errorf("expected ≥2 HTTP calls (initial + self-claim); got %d", n)
	}

	// The task should now be in_progress with the worker as assignee.
	claimed, ok := store.Get(task.ID)
	if !ok {
		t.Fatal("task not found in store")
	}
	if claimed.Status != tasktool.StatusInProgress {
		t.Errorf("task.Status = %q; want in_progress", claimed.Status)
	}
	if claimed.Assignee != "worker" {
		t.Errorf("task.Assignee = %q; want %q", claimed.Assignee, "worker")
	}

	cancel()
	waitForKind(t, tm, team.KindCompletion)
}

// TestSpawnTeammate_IdleThenNewTask: after going idle, a message from lead
// triggers a second run. A second KindIdle arrives; then cancel yields KindCompletion.
func TestSpawnTeammate_IdleThenNewTask(t *testing.T) {
	tm := team.New("test")
	store := tasktool.NewStore() // no tasks

	var calls atomic.Int32
	lp := makeTeamParentLoopWithStore(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	}, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "bob", "first task", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	// First run → idle.
	waitForKind(t, tm, team.KindIdle)

	// Dispatch a new task via team mailbox. bob is still registered here.
	if err := tm.Send(team.Message{From: "lead", To: "bob", Kind: team.KindMessage, Text: "new task: do Y"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Second run triggered → second idle.
	waitForKind(t, tm, team.KindIdle)

	if n := calls.Load(); n < 2 {
		t.Errorf("expected ≥2 HTTP calls (first + new-task); got %d", n)
	}

	cancel()
	waitForKind(t, tm, team.KindCompletion)
}

// ─── Phase 6: plan-approval workflow ──────────────────────────────────────────

// singleToolUseSSEWithInput builds an SSE stream for exactly one tool_use turn
// with a custom input JSON payload (stop_reason=tool_use, no follow-up message).
// The loop will execute the tool then make a second HTTP call for the follow-up.
func singleToolUseSSEWithInput(toolName, toolID, inputJSON string) string {
	esc := strings.ReplaceAll(inputJSON, `"`, `\"`)
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"" + toolID + "\",\"name\":\"" + toolName + "\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"" + esc + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

// TestSpawnTeammate_PlanApproval_Approved: when the child calls ExitPlanMode and
// the parent's PlanReply is pre-loaded with Approved=true, the child continues
// past plan mode and eventually reaches idle.
func TestSpawnTeammate_PlanApproval_Approved(t *testing.T) {
	var reqCount atomic.Int32
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		switch reqCount.Add(1) {
		case 1:
			// First request: ExitPlanMode tool use
			_, _ = w.Write([]byte(singleToolUseSSEWithInput(
				"ExitPlanMode", "tu_plan_01", `{"plan":"build the thing"}`)))
		default:
			_, _ = w.Write([]byte(textOnlySSE("proceeding with approved plan")))
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "alice", "make a plan first", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	// Pre-load approval into PlanReply (buffered 1) before the child reaches AskApprove.
	if err := tm.SendPlanDecision("alice", team.PlanDecision{Approved: true}); err != nil {
		t.Fatalf("SendPlanDecision: %v", err)
	}

	// Child runs ExitPlanMode → AskApprove receives pre-loaded approval → continues → idle.
	waitForKind(t, tm, team.KindIdle)
	cancel()
	waitForKind(t, tm, team.KindCompletion)

	if n := reqCount.Load(); n < 2 {
		t.Errorf("expected ≥2 HTTP requests (plan + follow-up); got %d", n)
	}
}

// TestSpawnTeammate_PlanApproval_Rejected: when the lead rejects the plan,
// ExitPlanMode returns the feedback to the model and the run continues in plan mode.
func TestSpawnTeammate_PlanApproval_Rejected(t *testing.T) {
	var reqCount atomic.Int32
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		n := reqCount.Add(1)
		switch n {
		case 1:
			_, _ = w.Write([]byte(singleToolUseSSEWithInput(
				"ExitPlanMode", "tu_plan_02", `{"plan":"my initial plan"}`)))
		default:
			_, _ = w.Write([]byte(textOnlySSE("revising plan")))
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "bob", "plan something", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	// Pre-load rejection with feedback.
	if err := tm.SendPlanDecision("bob", team.PlanDecision{Approved: false, Feedback: "too vague"}); err != nil {
		t.Fatalf("SendPlanDecision: %v", err)
	}

	// Child gets rejection, loop continues (model sees error result), eventually idles.
	waitForKind(t, tm, team.KindIdle)
	cancel()
	waitForKind(t, tm, team.KindCompletion)
}

// ─── Phase 7: shutdown protocol ───────────────────────────────────────────────

// TestDeliveryPump_ShutdownRequest_FormatsAsTag: KindShutdownRequest messages
// are injected as <team-shutdown-request> rather than <team-message>.
func TestDeliveryPump_ShutdownRequest_FormatsAsTag(t *testing.T) {
	child := &Loop{}
	inbox := make(chan team.Message, 1)
	inbox <- team.Message{From: "lead", Kind: team.KindShutdownRequest, Text: "wrap up"}
	close(inbox)

	runDeliveryPump(child, inbox, make(chan struct{}))

	child.msgMu.Lock()
	q := child.msgQueue
	child.msgMu.Unlock()
	if len(q) != 1 {
		t.Fatalf("expected 1 queued message; got %d", len(q))
	}
	if strings.Contains(q[0], "<team-message") {
		t.Errorf("shutdown-request should not use <team-message> tag; got %q", q[0])
	}
	if !strings.Contains(q[0], "team-shutdown-request") {
		t.Errorf("shutdown-request should produce <team-shutdown-request> tag; got %q", q[0])
	}
}

// drainLeadInboxN reads exactly n messages from the lead inbox (5s timeout per message).
func drainLeadInboxN(t *testing.T, tm *team.Team, n int) []team.Message {
	t.Helper()
	msgs := make([]team.Message, 0, n)
	for range n {
		select {
		case m := <-tm.LeadInbox():
			msgs = append(msgs, m)
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for message %d/%d; got so far: %v", len(msgs)+1, n, msgs)
		}
	}
	return msgs
}

// TestSpawnTeammate_ShutdownApproved: when SendShutdownReply(true) is called,
// the monitoring goroutine fires KindShutdownApprove to lead and cancels the
// teammate; KindCompletion follows.
func TestSpawnTeammate_ShutdownApproved(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "alice", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	waitForKind(t, tm, team.KindIdle)

	if err := tm.SendShutdownReply("alice", true); err != nil {
		t.Fatalf("SendShutdownReply: %v", err)
	}

	// Monitoring goroutine sends KindShutdownApprove then calls cancel().
	// Main goroutine exits idle select → KindCompletion.
	// Drain both in any order.
	msgs := drainLeadInboxN(t, tm, 2)
	kindsGot := make(map[team.MessageKind]bool)
	for _, m := range msgs {
		kindsGot[m.Kind] = true
	}
	if !kindsGot[team.KindShutdownApprove] {
		t.Errorf("expected KindShutdownApprove in lead inbox; got kinds: %v", kindsGot)
	}
	if !kindsGot[team.KindCompletion] {
		t.Errorf("expected KindCompletion in lead inbox; got kinds: %v", kindsGot)
	}
}

// TestSpawnTeammate_ShutdownRejected: when SendShutdownReply(false) is called,
// KindShutdownReject is sent to lead but the teammate keeps running.
func TestSpawnTeammate_ShutdownRejected(t *testing.T) {
	tm := team.New("test")
	lp := makeTeamParentLoop(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "alice", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	waitForKind(t, tm, team.KindIdle)

	if err := tm.SendShutdownReply("alice", false); err != nil {
		t.Fatalf("SendShutdownReply: %v", err)
	}

	// Rejection: monitoring goroutine sends KindShutdownReject, keeps watching.
	msg := waitForKind(t, tm, team.KindShutdownReject)
	if msg.From != "alice" {
		t.Errorf("KindShutdownReject From = %q, want %q", msg.From, "alice")
	}

	// Alice should still be registered.
	if !slices.Contains(tm.Names(), "alice") {
		t.Error("alice should still be in team after shutdown rejection")
	}

	// Now really cancel to clean up.
	cancel()
	waitForKind(t, tm, team.KindCompletion)
}

// TestSpawnTeammate_IdleContextCancel: cancelling the context during idle
// produces KindCompletion without a second run.
func TestSpawnTeammate_IdleContextCancel(t *testing.T) {
	tm := team.New("test")
	store := tasktool.NewStore()

	var calls atomic.Int32
	lp := makeTeamParentLoopWithStore(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	}, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := lp.SpawnTeammate(ctx, "carol", "work", SubAgentSpec{}, tm); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	waitForKind(t, tm, team.KindIdle)
	cancel()
	waitForKind(t, tm, team.KindCompletion)

	// Only one run should have happened (no self-claim, no message).
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 HTTP call; got %d", n)
	}
}
