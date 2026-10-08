package coding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRunRepairsLocalCandidateBeforeCompletion(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "answer.txt")
	if err := os.WriteFile(p, []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	checks := 0
	reviews := 0
	endAttempt := -1
	r := Runner{Root: root, MaxRepairAttempts: 1, AuthorizeWrite: func(context.Context, string) error { return nil },
		Observe: func(e Event) error {
			if e.Kind == "end" {
				endAttempt = e.Attempt
			}
			return nil
		},
		Generate: func(_ context.Context, q Request) (string, error) {
			calls++
			if calls == 1 {
				if q.Task != "implement" {
					t.Fatalf("task=%s", q.Task)
				}
				return `{"edits":[{"path":"answer.txt","old":"wrong","new":"still wrong"}]}`, nil
			}
			if q.Task != "fix" || q.Feedback == "" || q.Sources[0].Content != "still wrong" {
				t.Fatalf("repair context=%+v", q)
			}
			return `{"edits":[{"path":"answer.txt","old":"still wrong","new":"correct"}]}`, nil
		},
		Verify: func(context.Context) (Evidence, error) {
			checks++
			b, err := os.ReadFile(p)
			if err != nil {
				return Evidence{}, err
			}
			return Evidence{Passed: string(b) == "correct", Feedback: "expected correct", WorkspaceDigest: string(b)}, nil
		},
		Review: func(_ context.Context, q Request) (Review, error) {
			reviews++
			if q.Patch == "" {
				t.Fatal("review without patch")
			}
			return Review{Accepted: true}, nil
		},
	}
	out, err := r.Run(context.Background(), Task{Prompt: "fix answer", Files: []string{"answer.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "verified" || calls != 2 || checks < 2 || reviews != 1 {
		t.Fatalf("out=%+v calls=%d checks=%d reviews=%d", out, calls, checks, reviews)
	}
	if endAttempt != out.Attempts-1 {
		t.Fatalf("terminal event attempt=%d, want last attempt=%d", endAttempt, out.Attempts-1)
	}
}

func TestRunBudgetAndReviewNeverDeclareSuccess(t *testing.T) {
	for _, rejectReview := range []bool{false, true} {
		t.Run(fmt.Sprint(rejectReview), func(t *testing.T) {
			r := Runner{Root: t.TempDir(), MaxRepairAttempts: 1, AuthorizeWrite: func(context.Context, string) error { return nil }, Generate: func(context.Context, Request) (string, error) { return `{"edits":[]}`, nil }, Verify: func(context.Context) (Evidence, error) {
				return Evidence{Passed: rejectReview, Feedback: "check failed"}, nil
			}}
			if rejectReview {
				r.Review = func(context.Context, Request) (Review, error) { return Review{Feedback: "needs work"}, nil }
			}
			out, err := r.Run(context.Background(), Task{Prompt: "fix", Files: []string{"new.txt"}})
			if err != nil {
				t.Fatal(err)
			}
			if out.Status != "incomplete" || out.Attempts != 2 {
				t.Fatalf("out=%+v", out)
			}
		})
	}
}

func TestRunCancelledDoesNotGenerate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Runner{Root: t.TempDir(), Generate: func(context.Context, Request) (string, error) { t.Fatal("generated after cancel"); return "", nil }, Verify: func(context.Context) (Evidence, error) { return Evidence{}, nil }}
	if _, err := r.Run(ctx, Task{Prompt: "fix", Files: []string{"new.txt"}}); err == nil {
		t.Fatal("cancellation swallowed")
	}
}
