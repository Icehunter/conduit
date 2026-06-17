package agent

// Tests for the per-loop programmatic message injection queue (InjectMessage).
// This is distinct from InjectSteerMessage (human steering, last-write-wins).
//
// Token-bleed protection: each Loop has its own msgQueue field (not shared),
// so injecting into Loop A never leaks into Loop B's context.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
)

// ─── InjectMessage queue semantics ───────────────────────────────────────────

func TestLoop_InjectMessage_AllPreservedInOrder(t *testing.T) {
	// Pre-inject 3 messages before the loop runs. All must be delivered to the
	// model in order at the next turn boundary (after first tool use).
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "ok"})

	var capturedBodies [][]byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capturedBodies = append(capturedBodies, body)
		n := len(capturedBodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// First call: tool-use only — loop will execute tool then make a second request.
			_, _ = w.Write([]byte(singleToolUseSSE("tool_1")))
		} else {
			_, _ = w.Write([]byte(textOnlySSE("finished")))
		}
	}))
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "test"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{Model: "m", MaxTokens: 128, System: []api.SystemBlock{{Type: "text", Text: "s"}}})

	// Pre-inject before calling Run.
	lp.InjectMessage("<team-message from=\"a\">first</team-message>")
	lp.InjectMessage("<team-message from=\"b\">second</team-message>")
	lp.InjectMessage("<team-message from=\"c\">third</team-message>")

	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "start"}}},
	}, func(LoopEvent) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(capturedBodies) < 2 {
		t.Fatalf("expected ≥2 API calls; got %d", len(capturedBodies))
	}
	// Second request must contain all three injected messages in order.
	second := string(capturedBodies[1])
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(second, want) {
			t.Errorf("second API request missing %q injected message; body excerpt: %.200s", want, second)
		}
	}
	// Verify ordering: "first" appears before "second" before "third".
	iFirst := strings.Index(second, "first")
	iSecond := strings.Index(second, "second")
	iThird := strings.Index(second, "third")
	if !(iFirst < iSecond && iSecond < iThird) {
		t.Errorf("injected messages out of order in second request (first=%d, second=%d, third=%d)", iFirst, iSecond, iThird)
	}
}

func TestLoop_InjectMessage_QueueClearedAfterDrain(t *testing.T) {
	// After the first drain, the queue must be empty (no double-delivery).
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "ok"})

	callCount := 0
	var capturedBodies [][]byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capturedBodies = append(capturedBodies, body)
		callCount++
		cc := callCount
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if cc == 1 {
			_, _ = w.Write([]byte(singleToolUseSSE("t1")))
		} else {
			_, _ = w.Write([]byte(textOnlySSE("ok")))
		}
	}))
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "test"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{Model: "m", MaxTokens: 128})
	lp.InjectMessage("injected-once")

	_, _ = lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}},
	}, func(LoopEvent) {})

	mu.Lock()
	defer mu.Unlock()
	if len(capturedBodies) < 2 {
		t.Fatalf("expected ≥2 calls; got %d", len(capturedBodies))
	}
	// Third call (if any) must NOT contain "injected-once".
	for i, body := range capturedBodies[2:] {
		if strings.Contains(string(body), "injected-once") {
			t.Errorf("injected message re-appeared in call %d (double-delivery)", i+3)
		}
	}
}

// ─── Token isolation (no bleed between loops) ─────────────────────────────────

// TestLoop_InjectMessage_NoTokenBleed verifies that injecting into Loop A
// does not affect Loop B's context window. This is the core anti-bleed
// guarantee: msgQueue is a per-Loop field, not a package-level global.
func TestLoop_InjectMessage_NoTokenBleed(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "ok"})

	// Two independent test servers: one for loop A, one for loop B.
	var bodiesA, bodiesB [][]byte
	var muA, muB sync.Mutex

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		muA.Lock()
		bodiesA = append(bodiesA, body)
		n := len(bodiesA)
		muA.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = w.Write([]byte(singleToolUseSSE("ta1")))
		} else {
			_, _ = w.Write([]byte(textOnlySSE("loop-a-done")))
		}
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		muB.Lock()
		bodiesB = append(bodiesB, body)
		muB.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("loop-b-done")))
	}))
	defer srvB.Close()

	clientA := api.NewClient(api.Config{BaseURL: srvA.URL, AuthToken: "test"}, srvA.Client())
	clientB := api.NewClient(api.Config{BaseURL: srvB.URL, AuthToken: "test"}, srvB.Client())

	loopA := NewLoop(clientA, reg, LoopConfig{Model: "m", MaxTokens: 128})
	loopB := NewLoop(clientB, reg, LoopConfig{Model: "m", MaxTokens: 128})

	// Inject a secret into Loop A only.
	loopA.InjectMessage("LOOP-A-SECRET")

	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = loopA.Run(context.Background(), []api.Message{
			{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "a"}}},
		}, func(LoopEvent) {})
	})
	wg.Go(func() {
		_, _ = loopB.Run(context.Background(), []api.Message{
			{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "b"}}},
		}, func(LoopEvent) {})
	})
	wg.Wait()

	// Verify: A's second request body contains the secret.
	muA.Lock()
	aHasSecret := len(bodiesA) >= 2 && strings.Contains(string(bodiesA[1]), "LOOP-A-SECRET")
	muA.Unlock()
	if !aHasSecret {
		t.Error("Loop A should have received the injected message in its second request")
	}

	// Verify: none of B's request bodies contain the secret.
	muB.Lock()
	defer muB.Unlock()
	for i, body := range bodiesB {
		if strings.Contains(string(body), "LOOP-A-SECRET") {
			t.Errorf("Loop B request %d contains Loop A's secret (token bleed!)", i)
		}
	}
}

// ─── InjectMessage concurrent safety ─────────────────────────────────────────

func TestLoop_InjectMessage_ConcurrentSafe(t *testing.T) {
	lp := &Loop{}

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			lp.InjectMessage("concurrent inject")
		})
	}
	wg.Wait()

	lp.msgMu.Lock()
	n := len(lp.msgQueue)
	lp.msgMu.Unlock()
	if n != 50 {
		t.Errorf("expected 50 queued messages; got %d", n)
	}
}

// ─── InjectSteerMessage unchanged semantics ───────────────────────────────────

// InjectSteerMessage must remain last-write-wins (human responsiveness).
// InjectMessage must NOT share this behavior — all messages must be preserved.
func TestLoop_SteerVsInject_Independence(t *testing.T) {
	lp := &Loop{}

	// Three steer messages: only the last should win.
	lp.InjectSteerMessage("steer-A")
	lp.InjectSteerMessage("steer-B")
	lp.InjectSteerMessage("steer-C")

	// Three inject messages: all must be preserved.
	lp.InjectMessage("inject-1")
	lp.InjectMessage("inject-2")
	lp.InjectMessage("inject-3")

	// Drain steer: only last wins.
	v := lp.steerMsg.Swap("")
	steer, _ := v.(string)
	if steer != "steer-C" {
		t.Errorf("steerMsg = %q; want last-write-wins %q", steer, "steer-C")
	}

	// Drain inject queue: all three must be present.
	lp.msgMu.Lock()
	queued := lp.msgQueue
	lp.msgMu.Unlock()
	if len(queued) != 3 {
		t.Errorf("msgQueue len = %d; want 3", len(queued))
	}
	for i, want := range []string{"inject-1", "inject-2", "inject-3"} {
		if i >= len(queued) || queued[i] != want {
			t.Errorf("queued[%d] = %q; want %q", i, queued[i], want)
		}
	}
}
