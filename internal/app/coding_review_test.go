package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/icehunter/conduit/internal/coding"
	"github.com/icehunter/conduit/internal/episodes"
	"github.com/icehunter/conduit/internal/settings"
)

func TestCodingRuntimeProtectsDefinitionsBeforeAnyWrite(t *testing.T) {
	for _, tc := range []struct{ path, old string }{{"expected.txt", "correct"}, {"Makefile", ""}} {
		t.Run(tc.path, func(t *testing.T) {
			rt, _, root := codingFixture(t, false)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				draft, err := json.Marshal(map[string]any{"edits": []map[string]string{
					{"path": "answer.txt", "old": "wrong", "new": "correct"},
					{"path": tc.path, "old": tc.old, "new": "weakened definition"},
				}})
				if err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(draft)}, "finish_reason": "stop"}}}); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			p := rt.providers.Settings.Providers["local"]
			p.BaseURL = srv.URL
			rt.providers.Settings.Providers["local"] = p
			out, err := rt.Run(context.Background(), coding.Task{Prompt: "fix without weakening checks", Files: []string{"answer.txt", tc.path}})
			if err != nil || out.Status != "blocked" || calls.Load() != 1 {
				t.Fatalf("out=%+v calls=%d err=%v", out, calls.Load(), err)
			}
			answer, err := os.ReadFile(filepath.Join(root, "answer.txt"))
			if err != nil || string(answer) != "wrong" {
				t.Fatalf("earlier edit leaked before authorization: answer=%q err=%v", answer, err)
			}
			definition, err := os.ReadFile(filepath.Join(root, tc.path))
			if tc.old == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("new protected definition was created: %q %v", definition, err)
				}
			} else if err != nil || string(definition) != tc.old {
				t.Fatalf("protected definition changed: %q %v", definition, err)
			}
		})
	}
}

func TestCodingRuntimeUntrustedPreflightHasNoModelCallOrWrite(t *testing.T) {
	rt, calls, root := codingFixture(t, false)
	t.Setenv("CLAUDE_CODE_SANDBOXED", "")
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	out, err := rt.Run(context.Background(), coding.Task{Prompt: "fix", Files: []string{"answer.txt"}})
	if err == nil || !strings.Contains(err.Error(), "trusted") || out.Status != "blocked" || calls.Load() != 0 {
		t.Fatalf("out=%+v calls=%d err=%v", out, calls.Load(), err)
	}
	answer, err := os.ReadFile(filepath.Join(root, "answer.txt"))
	if err != nil || string(answer) != "wrong" {
		t.Fatalf("untrusted checkout changed: %q %v", answer, err)
	}
}

func TestCodingRuntimeRejectsSameModelReviewBeforeTransport(t *testing.T) {
	rt, calls, _ := codingFixture(t, false)
	reviewer := rt.providers.Settings.Providers["cloud"]
	reviewer.Model = rt.providers.Settings.Providers["local"].Model
	rt.providers.Settings.Providers["cloud"] = reviewer
	out, err := rt.Run(context.Background(), coding.Task{Prompt: "fix", Files: []string{"answer.txt"}})
	if err == nil || !strings.Contains(err.Error(), "distinct") || out.Status != "error" || calls.Load() != 2 {
		t.Fatalf("out=%+v calls=%d err=%v", out, calls.Load(), err)
	}
	data, err := episodes.ExportFile(rt.recorder.Path(), episodes.ExportOptions{AllowModelReview: true})
	if err != nil || len(data.SFT) != 0 || len(data.DPO) != 0 {
		t.Fatalf("self-reviewed error exported: %+v %v", data.Manifest, err)
	}
}

func TestCodingJournalUnknownErrorCannotBecomeTrainingEvidence(t *testing.T) {
	rt, _, root := codingFixture(t, false)
	task := coding.Task{Prompt: "fix", Files: []string{"answer.txt"}}
	j := newCodingJournal(rt.recorder, "test-repository", task)
	failure := errors.New("unexpected reviewer transport failure")
	run := coding.Runner{
		Root: root,
		Generate: func(_ context.Context, request coding.Request) (string, error) {
			prompt := episodes.Request{Messages: []episodes.Message{{Role: "user", Content: coding.ImplementationPrompt(request)}}, ModelID: "qwen", ContextComplete: true}
			if err := j.request(settings.RoleImplement, settings.ActiveProviderSettings{Model: "qwen"}, prompt); err != nil {
				return "", err
			}
			return `{"edits":[{"path":"answer.txt","old":"wrong","new":"correct"}]}`, nil
		},
		AuthorizeWrite: func(context.Context, string) error { return nil },
		Verify: func(context.Context) (coding.Evidence, error) {
			return coding.Evidence{Passed: true, WorkspaceDigest: "candidate-tree", PolicyDigest: "frozen-policy"}, nil
		},
		Review:  func(context.Context, coding.Request) (coding.Review, error) { return coding.Review{}, failure },
		Observe: j.observe,
	}
	out, err := run.Run(context.Background(), task)
	if !errors.Is(err, failure) || out.Status != "error" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	// Even a later explicit label cannot repair missing execution evidence.
	if err := j.write(episodes.KindDecision, episodes.Decision{CandidateID: j.episode + ":0", Action: "accepted", Source: episodes.SourceExplicitHuman}); err != nil {
		t.Fatal(err)
	}
	data, err := episodes.ExportFile(rt.recorder.Path(), episodes.ExportOptions{AllowModelReview: true})
	if err != nil || len(data.SFT) != 0 || len(data.DPO) != 0 {
		t.Fatalf("unknown-error episode exported: %+v %v", data.Manifest, err)
	}
}
