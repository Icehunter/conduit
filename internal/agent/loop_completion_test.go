package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
)

func TestCompletionFailureRepairPassPublishesOnlyVerifiedText(t *testing.T) {
	var published strings.Builder
	var order []string
	checks := 0
	config := &CompletionConfig{
		MaxRepairAttempts: 1,
		PrepareCompletion: func(context.Context) error { order = append(order, "flush"); return nil },
		Verify: func(_ context.Context, c CompletionCandidate) (CompletionDecision, error) {
			if published.Len() != 0 {
				t.Fatal("candidate published before verification")
			}
			order = append(order, "verify")
			checks++
			if checks == 1 {
				return CompletionDecision{Status: CompletionFailed, Feedback: "test failed"}, nil
			}
			if !strings.Contains(apiMessagesText(c.History), "test failed") {
				t.Fatal("repair feedback missing")
			}
			return CompletionDecision{Status: CompletionVerified}, nil
		},
	}
	// The leading tool call makes this a coding turn, so the gate applies.
	lp, srv := newTestLoopWithConfig(t, []string{toolCallOnlySSE("Bash"), textOnlySSE("premature done"), textOnlySSE("verified done")}, tool.NewRegistry(), LoopConfig{Completion: config})
	defer srv.Close()
	_, err := lp.Run(context.Background(), []api.Message{user("implement")}, func(ev LoopEvent) {
		if ev.Type == EventText {
			published.WriteString(ev.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.String() != "verified done" {
		t.Fatalf("published=%q", published.String())
	}
	if strings.Join(order, ",") != "flush,verify,flush,verify" {
		t.Fatalf("order=%v", order)
	}
}

func TestCompletionContinuationPreservesTextUntilVerified(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "pass", true: "repair discards failed candidate"}[repair], func(t *testing.T) {
			cutoff := func(text string) string {
				return strings.ReplaceAll(textOnlySSE(text), `"stop_reason":"end_turn"`, `"stop_reason":"max_tokens"`)
			}
			// The leading tool call makes this a coding turn, so the gate applies.
			responses := []string{toolCallOnlySSE("Bash"), cutoff("Complete "), textOnlySSE("candidate.")}
			if repair {
				responses = append(responses, cutoff("Fixed "), textOnlySSE("candidate."))
			}
			published := ""
			checks := 0
			cfg := &CompletionConfig{MaxRepairAttempts: 1, Verify: func(_ context.Context, candidate CompletionCandidate) (CompletionDecision, error) {
				checks++
				want := "Complete candidate."
				if checks == 2 {
					want = "Fixed candidate."
				}
				if published != "" || candidate.Text != want {
					t.Fatalf("premature=%q candidate=%q want=%q", published, candidate.Text, want)
				}
				if repair && checks == 1 {
					return CompletionDecision{Status: CompletionFailed, Feedback: "fix needed"}, nil
				}
				return CompletionDecision{Status: CompletionVerified}, nil
			}}
			lp, srv := newTestLoopWithConfig(t, responses, tool.NewRegistry(), LoopConfig{Completion: cfg})
			defer srv.Close()
			_, err := lp.Run(context.Background(), []api.Message{user("implement")}, func(ev LoopEvent) {
				if ev.Type == EventText {
					published += ev.Text
				}
			})
			want := "Complete candidate."
			if repair {
				want = "Fixed candidate."
			}
			if err != nil || published != want {
				t.Fatalf("published=%q want=%q error=%v", published, want, err)
			}
		})
	}
}

func TestCompletionExternalRepairAndBudget(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "same loop", true: "pinned implementer"}[external], func(t *testing.T) {
			checks, repairs := 0, 0
			cfg := &CompletionConfig{MaxRepairAttempts: 1, Verify: func(context.Context, CompletionCandidate) (CompletionDecision, error) {
				checks++
				return CompletionDecision{Status: CompletionFailed, Feedback: "still broken"}, nil
			}}
			if external {
				cfg.Repair = func(_ context.Context, c CompletionCandidate, feedback string) ([]api.Message, error) {
					repairs++
					if feedback != "still broken" || c.Attempt != 1 {
						t.Fatalf("candidate=%+v feedback=%q", c, feedback)
					}
					return []api.Message{nudgeMessage("local implementer repaired")}, nil
				}
			}
			// The leading tool call makes this a coding turn, so the gate applies.
			lp, srv := newTestLoopWithConfig(t, []string{toolCallOnlySSE("Bash"), textOnlySSE("one"), textOnlySSE("two")}, tool.NewRegistry(), LoopConfig{Completion: cfg})
			defer srv.Close()
			var text string
			_, err := lp.Run(context.Background(), []api.Message{user("implement")}, func(ev LoopEvent) {
				if ev.Type == EventText {
					text += ev.Text
				}
			})
			if !errors.Is(err, ErrCompletionIncomplete) || checks != 2 || text != "" {
				t.Fatalf("checks=%d text=%q err=%v", checks, text, err)
			}
			if external && repairs != 1 {
				t.Fatalf("repairs=%d", repairs)
			}
		})
	}
}

// A turn that makes no tool call is not a coding turn: the gate must not run
// (no flush, no verify, no completion events) and the text publishes as-is.
func TestCompletionNoWorkTurnSkipsGate(t *testing.T) {
	var published strings.Builder
	var completionEvents []CompletionStatus
	flushes, verifies := 0, 0
	cfg := &CompletionConfig{
		PrepareCompletion: func(context.Context) error { flushes++; return nil },
		Verify: func(context.Context, CompletionCandidate) (CompletionDecision, error) {
			verifies++
			return CompletionDecision{Status: CompletionVerified}, nil
		},
	}
	lp, srv := newTestLoopWithConfig(t, []string{textOnlySSE("hi there")}, tool.NewRegistry(), LoopConfig{Completion: cfg})
	defer srv.Close()
	_, err := lp.Run(context.Background(), []api.Message{user("hi")}, func(ev LoopEvent) {
		switch ev.Type {
		case EventText:
			published.WriteString(ev.Text)
		case EventCompletion:
			completionEvents = append(completionEvents, ev.CompletionStatus)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if flushes != 0 || verifies != 0 {
		t.Fatalf("gate ran on a no-work turn: flushes=%d verifies=%d", flushes, verifies)
	}
	if len(completionEvents) != 0 {
		t.Fatalf("completion events on a no-work turn: %v", completionEvents)
	}
	if published.String() != "hi there" {
		t.Fatalf("published=%q", published.String())
	}
}

// A turn that executes tools but leaves the workspace byte-identical is not
// a coding turn: the gate must not run (no flush, no verify, no completion
// events) and the text publishes as-is.
func TestCompletionUnchangedWorkspaceSkipsGate(t *testing.T) {
	var published strings.Builder
	var completionEvents []CompletionStatus
	flushes, verifies := 0, 0
	cfg := &CompletionConfig{
		PrepareCompletion: func(context.Context) error { flushes++; return nil },
		WorkspaceDigest:   func(context.Context) (string, error) { return "unchanged", nil },
		Verify: func(context.Context, CompletionCandidate) (CompletionDecision, error) {
			verifies++
			return CompletionDecision{Status: CompletionVerified}, nil
		},
	}
	lp, srv := newTestLoopWithConfig(t, []string{toolCallOnlySSE("Bash"), textOnlySSE("investigation only")}, tool.NewRegistry(), LoopConfig{Completion: cfg})
	defer srv.Close()
	_, err := lp.Run(context.Background(), []api.Message{user("investigate")}, func(ev LoopEvent) {
		switch ev.Type {
		case EventText:
			published.WriteString(ev.Text)
		case EventCompletion:
			completionEvents = append(completionEvents, ev.CompletionStatus)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if flushes != 0 || verifies != 0 || len(completionEvents) != 0 {
		t.Fatalf("gate ran on an unchanged-workspace turn: flushes=%d verifies=%d events=%v", flushes, verifies, completionEvents)
	}
	if published.String() != "investigation only" {
		t.Fatalf("published=%q", published.String())
	}
}

// A turn whose tools changed the workspace still runs the gate.
func TestCompletionChangedWorkspaceRunsGate(t *testing.T) {
	digestCalls, verifies := 0, 0
	cfg := &CompletionConfig{
		WorkspaceDigest: func(context.Context) (string, error) {
			digestCalls++
			if digestCalls == 1 {
				return "before", nil // baseline captured before the first tool batch
			}
			return "after", nil // the turn's tools changed the workspace
		},
		Verify: func(context.Context, CompletionCandidate) (CompletionDecision, error) {
			verifies++
			return CompletionDecision{Status: CompletionVerified}, nil
		},
	}
	lp, srv := newTestLoopWithConfig(t, []string{toolCallOnlySSE("Bash"), textOnlySSE("changed")}, tool.NewRegistry(), LoopConfig{Completion: cfg})
	defer srv.Close()
	var text string
	_, err := lp.Run(context.Background(), []api.Message{user("implement")}, func(ev LoopEvent) {
		if ev.Type == EventText {
			text += ev.Text
		}
	})
	if err != nil || verifies != 1 || text != "changed" {
		t.Fatalf("verifies=%d text=%q err=%v", verifies, text, err)
	}
}

// A workspace fingerprint failure is conservative: the gate still applies.
func TestCompletionDigestFailureKeepsGate(t *testing.T) {
	verifies := 0
	cfg := &CompletionConfig{
		WorkspaceDigest: func(context.Context) (string, error) { return "", errors.New("git unavailable") },
		Verify: func(context.Context, CompletionCandidate) (CompletionDecision, error) {
			verifies++
			return CompletionDecision{Status: CompletionVerified}, nil
		},
	}
	lp, srv := newTestLoopWithConfig(t, []string{toolCallOnlySSE("Bash"), textOnlySSE("done")}, tool.NewRegistry(), LoopConfig{Completion: cfg})
	defer srv.Close()
	var text string
	_, err := lp.Run(context.Background(), []api.Message{user("implement")}, func(ev LoopEvent) {
		if ev.Type == EventText {
			text += ev.Text
		}
	})
	if err != nil || verifies != 1 || text != "done" {
		t.Fatalf("verifies=%d text=%q err=%v", verifies, text, err)
	}
}

func TestCompletionNonNaturalAndInactive(t *testing.T) {
	for _, reason := range []string{"pause_turn", "max_tokens", "unknown"} {
		t.Run(reason, func(t *testing.T) {
			checks := 0
			cfg := &CompletionConfig{Verify: func(context.Context, CompletionCandidate) (CompletionDecision, error) {
				checks++
				return CompletionDecision{Status: CompletionVerified}, nil
			}}
			body := strings.ReplaceAll(textOnlySSE("not finished"), `"stop_reason":"end_turn"`, `"stop_reason":"`+reason+`"`)
			lp, srv := newTestLoopWithConfig(t, []string{body, body, body, body}, tool.NewRegistry(), LoopConfig{Completion: cfg})
			defer srv.Close()
			_, err := lp.Run(context.Background(), []api.Message{user("implement")}, func(ev LoopEvent) {
				if ev.Type == EventText {
					t.Fatal("unverified text escaped")
				}
			})
			if !errors.Is(err, ErrCompletionIncomplete) || checks != 0 {
				t.Fatalf("checks=%d err=%v", checks, err)
			}
		})
	}
	lp, srv := newTestLoopWithConfig(t, []string{textOnlySSE("plan")}, tool.NewRegistry(), LoopConfig{Completion: &CompletionConfig{Active: func() bool { return false }}})
	defer srv.Close()
	var text string
	_, err := lp.Run(context.Background(), []api.Message{user("plan")}, func(ev LoopEvent) {
		if ev.Type == EventText {
			text += ev.Text
		}
	})
	if err != nil || text != "plan" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestCompletionCancellationAndFlushError(t *testing.T) {
	for _, tc := range []string{"cancel", "flush"} {
		t.Run(tc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := &CompletionConfig{TotalTimeout: time.Second, Verify: func(context.Context, CompletionCandidate) (CompletionDecision, error) {
				t.Fatal("verified after failed flush")
				return CompletionDecision{}, nil
			}, PrepareCompletion: func(context.Context) error {
				if tc == "cancel" {
					cancel()
					return context.Canceled
				}
				return errors.New("unapplied edits")
			}}
			// The leading tool call makes this a coding turn, so the gate applies.
			lp, srv := newTestLoopWithConfig(t, []string{toolCallOnlySSE("Bash"), textOnlySSE("done")}, tool.NewRegistry(), LoopConfig{Completion: cfg})
			defer srv.Close()
			_, err := lp.Run(ctx, []api.Message{user("implement")}, func(ev LoopEvent) {
				if ev.Type == EventText {
					t.Fatal("unverified text escaped")
				}
			})
			if err == nil {
				t.Fatal("failure swallowed")
			}
		})
	}
}

func TestCompletionNotInheritedByHelpers(t *testing.T) {
	lp := NewLoop(nil, tool.NewRegistry(), LoopConfig{Completion: &CompletionConfig{Verify: func(context.Context, CompletionCandidate) (CompletionDecision, error) {
		t.Fatal("helper verified")
		return CompletionDecision{}, nil
	}}})
	child, _ := lp.buildChildLoop(SubAgentSpec{})
	if child.cfg.Completion != nil {
		t.Fatal("helper inherited parent gate")
	}
}

func TestStrictRoleFailureDoesNotCallInheritedClient(t *testing.T) {
	want := errors.New("local role unavailable")
	lp, srv := newTestLoopWithConfig(t, nil, tool.NewRegistry(), LoopConfig{ResolveRole: func(string) (string, *api.Client, error) { return "", nil, want }})
	defer srv.Close()
	_, err := lp.RunSubAgentTyped(context.Background(), "implement", SubAgentSpec{Role: "implement"})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	_, err = lp.RunSubAgentTyped(context.Background(), "implement", SubAgentSpec{Role: "implement", Model: "qwen-override"})
	if !errors.Is(err, want) {
		t.Fatalf("explicit model bypassed strict role: %v", err)
	}
}

func apiMessagesText(messages []api.Message) string {
	var b strings.Builder
	for _, m := range messages {
		for _, c := range m.Content {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}
