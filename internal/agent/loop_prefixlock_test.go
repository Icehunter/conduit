package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
)

func think(sig string) api.ContentBlock {
	return api.ContentBlock{Type: "thinking", Thinking: "t", Signature: sig}
}
func text(s string) api.ContentBlock { return api.ContentBlock{Type: "text", Text: s} }
func asst(b ...api.ContentBlock) api.Message {
	return api.Message{Role: "assistant", Content: b}
}
func user(s string) api.Message {
	return api.Message{Role: "user", Content: []api.ContentBlock{text(s)}}
}

func TestStripMessageThinking(t *testing.T) {
	tests := []struct {
		name    string
		in      api.Message
		fromOrd int
		want    api.Message
		wantOK  bool
	}{
		{"user message untouched", user("q"), 0, user("q"), false},
		{"no thinking untouched", asst(text("a")), 0, asst(text("a")), false},
		{"ord 0 drops all thinking", asst(think("s1"), text("a"), think("s2")), 0, asst(text("a")), true},
		{"ord 1 keeps blocks before the cut", asst(think("s1"), text("a"), think("s2"), text("b")), 1, asst(think("s1"), text("a"), text("b")), true},
		{"ord beyond count untouched", asst(think("s1")), 1, asst(think("s1")), false},
		{"empty text dropped alongside", asst(think("s1"), text("  \n")), 0, asst(text(thinkingRemovedPlaceholder)), true},
		{"thinking-only gets placeholder", asst(think("s1")), 0, asst(text(thinkingRemovedPlaceholder)), true},
		{"tool_use survives", asst(think("s1"), api.ContentBlock{Type: "tool_use", ID: "t1", Name: "Bash"}), 0,
			asst(api.ContentBlock{Type: "tool_use", ID: "t1", Name: "Bash"}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := stripMessageThinking(tt.in, tt.fromOrd)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v; want %v", ok, tt.wantOK)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestStripThinkingFrom_DoesNotMutateInput(t *testing.T) {
	in := []api.Message{user("q"), asst(think("s1"), text("a"))}
	snapshot := []api.Message{user("q"), asst(think("s1"), text("a"))}
	out, changed := stripThinkingFrom(in, 0, 0)
	if !changed {
		t.Fatal("expected change")
	}
	if !reflect.DeepEqual(in, snapshot) {
		t.Errorf("input mutated: %+v", in)
	}
	if len(out[1].Content) != 1 || out[1].Content[0].Type != "text" {
		t.Errorf("out[1] = %+v", out[1])
	}
}

func TestHealPrefixLock(t *testing.T) {
	history := func() []api.Message {
		return []api.Message{
			user("q1"),
			asst(think("s1"), text("a1")),
			user("q2"),
			asst(think("s2"), text("a2"), think("s3"), text("a2b")),
			user("q3"),
			asst(think("s4"), text("a3")),
		}
	}
	tests := []struct {
		name      string
		mm        *api.ThinkingMismatch
		attempt   int
		wantScope string
		wantOK    bool
		want      []api.Message
	}{
		{
			name: "partial strip from named block, earlier thinking kept",
			// messages[3].content[2] is the second thinking block (ord 1).
			mm: &api.ThinkingMismatch{MessageIndex: 3, BlockIndex: 2, HasBlock: true}, attempt: 1,
			wantScope: "partial", wantOK: true,
			want: []api.Message{
				user("q1"),
				asst(think("s1"), text("a1")),
				user("q2"),
				asst(think("s2"), text("a2"), text("a2b")),
				user("q3"),
				asst(text("a3")),
			},
		},
		{
			name: "attempt past partial budget strips all",
			mm:   &api.ThinkingMismatch{MessageIndex: 3, BlockIndex: 2, HasBlock: true}, attempt: 3,
			wantScope: "all", wantOK: true,
			want: []api.Message{
				user("q1"), asst(text("a1")), user("q2"), asst(text("a2"), text("a2b")), user("q3"), asst(text("a3")),
			},
		},
		{
			name: "no header strips all",
			mm:   nil, attempt: 1,
			wantScope: "all", wantOK: true,
			want: []api.Message{
				user("q1"), asst(text("a1")), user("q2"), asst(text("a2"), text("a2b")), user("q3"), asst(text("a3")),
			},
		},
		{
			name: "block index off a thinking block falls back to all",
			mm:   &api.ThinkingMismatch{MessageIndex: 3, BlockIndex: 1, HasBlock: true}, attempt: 1,
			wantScope: "all", wantOK: true,
			want: []api.Message{
				user("q1"), asst(text("a1")), user("q2"), asst(text("a2"), text("a2b")), user("q3"), asst(text("a3")),
			},
		},
		{
			name: "out-of-range message index falls back to all",
			mm:   &api.ThinkingMismatch{MessageIndex: 99, BlockIndex: 0, HasBlock: true}, attempt: 1,
			wantScope: "all", wantOK: true,
			want: []api.Message{
				user("q1"), asst(text("a1")), user("q2"), asst(text("a2"), text("a2b")), user("q3"), asst(text("a3")),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, scope, ok := healPrefixLock(history(), tt.mm, tt.attempt)
			if ok != tt.wantOK || scope != tt.wantScope {
				t.Fatalf("ok/scope = %v/%q; want %v/%q", ok, scope, tt.wantOK, tt.wantScope)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}

	t.Run("nothing to strip reports unchanged", func(t *testing.T) {
		in := []api.Message{user("q"), asst(text("a"))}
		got, scope, ok := healPrefixLock(in, nil, 1)
		if ok || scope != "all" || !reflect.DeepEqual(got, in) {
			t.Errorf("ok=%v scope=%q got=%+v", ok, scope, got)
		}
	})
}

func TestPrefixLockRejection(t *testing.T) {
	plain := errors.New("api: 400 Bad Request: not created in this conversation")
	if prefixLockRejection(plain) != nil {
		t.Error("untyped error must not match")
	}
	typed := &api.APIStatusError{Status: 400, Message: "bound to a different conversation"}
	if prefixLockRejection(typed) != typed {
		t.Error("typed prefix-lock error must match")
	}
	wrapped := errors.Join(errors.New("outer"), typed)
	if prefixLockRejection(wrapped) != typed {
		t.Error("wrapped typed error must match via errors.As")
	}
}

// TestLoop_HealsPrefixLockRejection — end to end: the first request is
// rejected with the prefix-lock 400 + header, the loop strips the named
// thinking block and everything after it, and the retried request succeeds
// with no thinking blocks from that point on the wire.
func TestLoop_HealsPrefixLockRejection(t *testing.T) {
	reg := tool.NewRegistry()
	callCount := 0
	var retriedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		raw, _ := io.ReadAll(r.Body)
		if callCount == 1 {
			w.Header().Set("anthropic-thinking-prefix-mismatch", "block=messages.1.content.0;kind=signature_mismatch")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: thinking block was not created in this conversation"}}`)
			return
		}
		retriedBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("healed")))
	}))
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{Model: "m", MaxTokens: 1024, System: []api.SystemBlock{{Type: "text", Text: "base"}}})

	var retries []LoopEvent
	history, err := lp.Run(context.Background(), []api.Message{
		user("start"),
		asst(think("stale-sig-1"), text("first")),
		user("more"),
		asst(think("stale-sig-2"), text("second")),
		user("continue"),
	}, func(ev LoopEvent) {
		if ev.Type == EventAPIRetry {
			retries = append(retries, ev)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("api calls = %d; want 2", callCount)
	}
	if len(retries) != 1 || retries[0].RetryErr == nil || !strings.Contains(retries[0].RetryErr.Error(), "prefix-lock rejection: stripped thinking (partial)") {
		t.Fatalf("retry events = %+v", retries)
	}
	if strings.Contains(retriedBody, `"signature":"stale-sig-1"`) || strings.Contains(retriedBody, `"signature":"stale-sig-2"`) {
		t.Errorf("stale thinking signature still on the wire after heal:\n%s", retriedBody)
	}
	if !strings.Contains(retriedBody, `"text":"first"`) || !strings.Contains(retriedBody, `"text":"second"`) {
		t.Errorf("non-thinking content lost during heal:\n%s", retriedBody)
	}
	last := history[len(history)-1]
	if last.Role != "assistant" || len(last.Content) == 0 || last.Content[0].Text != "healed" {
		t.Fatalf("unexpected final assistant message: %+v", last)
	}
}

// TestLoop_PrefixLockNothingToStripSurfaces — when history holds no thinking
// at all, the rejection can't be healed and must be returned rather than
// retried forever.
func TestLoop_PrefixLockNothingToStripSurfaces(t *testing.T) {
	reg := tool.NewRegistry()
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bound to a different conversation"}}`)
	}))
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{Model: "m", MaxTokens: 1024, System: []api.SystemBlock{{Type: "text", Text: "base"}}})

	_, err := lp.Run(context.Background(), []api.Message{user("start"), asst(text("plain")), user("go")}, func(LoopEvent) {})
	if err == nil {
		t.Fatal("expected error to surface")
	}
	var se *api.APIStatusError
	if !errors.As(err, &se) || !se.IsPrefixLockRejection() {
		t.Fatalf("surfaced error is %T: %v", err, err)
	}
	// One prefix-lock attempt (nothing stripped) then the generic
	// streamFailures budget (3 retries) — must not spin on the heal path.
	if callCount > 5 {
		t.Errorf("api calls = %d; heal path retried without stripping anything", callCount)
	}
}
