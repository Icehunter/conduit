package coding

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDoesNotCertifyAnEditorsChangeAsModelOutput(t *testing.T) {
	for _, when := range []string{"generation", "candidate", "verification", "review", "review rejects"} {
		t.Run(when, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "answer.txt")
			if err := os.WriteFile(file, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			passedEvidence := 0
			r := Runner{Root: root, MaxRepairAttempts: 1,
				AuthorizeWrite: func(context.Context, string) error { return nil },
				Generate: func(context.Context, Request) (string, error) {
					calls++
					if when == "generation" {
						if err := os.WriteFile(file, []byte("editor change"), 0o600); err != nil {
							return "", err
						}
					}
					return `{"edits":[{"path":"answer.txt","old":"original","new":"model draft"}]}`, nil
				},
				Observe: func(e Event) error {
					if when == "candidate" && e.Kind == "candidate" {
						return os.WriteFile(file, []byte("editor change"), 0o600)
					}
					if e.Kind == "verification" && e.Evidence.Passed {
						passedEvidence++
					}
					return nil
				},
				Verify: func(context.Context) (Evidence, error) {
					if when == "verification" {
						if err := os.WriteFile(file, []byte("editor change"), 0o600); err != nil {
							return Evidence{}, err
						}
					}
					return Evidence{Passed: true, WorkspaceDigest: "editor-state"}, nil
				},
			}
			if strings.HasPrefix(when, "review") {
				r.Review = func(context.Context, Request) (Review, error) {
					return Review{Accepted: when == "review"}, os.WriteFile(file, []byte("editor change"), 0o600)
				}
			}
			out, err := r.Run(context.Background(), Task{Prompt: "change answer", Files: []string{"answer.txt"}})
			wantPassed := 0
			if strings.HasPrefix(when, "review") {
				wantPassed = 1 // First check passed; the final blocked evidence replaces it.
			}
			if err != nil || out.Status != "blocked" || calls != 1 || passedEvidence != wantPassed {
				t.Fatalf("editor state attributed to draft: out=%+v calls=%d passed=%d err=%v", out, calls, passedEvidence, err)
			}
			if strings.Contains(out.Patch, "editor change") {
				t.Fatalf("editor change included in model patch: %s", out.Patch)
			}
			actual, err := os.ReadFile(file)
			if err != nil || string(actual) != "editor change" {
				t.Fatalf("editor change was overwritten: %q %v", actual, err)
			}
		})
	}
}

func TestRunBindsEvidenceToWholeWorkspaceSnapshot(t *testing.T) {
	root := t.TempDir()
	for name, contents := range map[string]string{"answer.txt": "original", "other.txt": "base"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func(context.Context) (string, error) {
		answer, err := os.ReadFile(filepath.Join(root, "answer.txt"))
		if err != nil {
			return "", err
		}
		other, err := os.ReadFile(filepath.Join(root, "other.txt"))
		return string(answer) + "|" + string(other), err
	}
	passedEvidence := 0
	base := ""
	r := Runner{Root: root, Snapshot: snapshot,
		AuthorizeWrite: func(context.Context, string) error { return nil },
		Generate: func(context.Context, Request) (string, error) {
			return `{"edits":[{"path":"answer.txt","old":"original","new":"model draft"}]}`, nil
		},
		Observe: func(e Event) error {
			if e.Kind == "start" {
				base = e.BaseDigest
			}
			if e.Kind == "candidate" {
				return os.WriteFile(filepath.Join(root, "other.txt"), []byte("editor change"), 0o600)
			}
			if e.Kind == "verification" && e.Evidence.Passed {
				passedEvidence++
			}
			return nil
		},
		Verify: func(ctx context.Context) (Evidence, error) {
			actual, err := snapshot(ctx)
			return Evidence{Passed: true, WorkspaceBefore: actual, WorkspaceDigest: actual}, err
		},
	}
	out, err := r.Run(context.Background(), Task{Prompt: "change answer", Files: []string{"answer.txt"}})
	if err != nil || out.Status != "blocked" || passedEvidence != 0 || base != "original|base" {
		t.Fatalf("out=%+v passed=%d base=%q err=%v", out, passedEvidence, base, err)
	}
}

func TestRunRejectsOversizedPromptWithoutGeneration(t *testing.T) {
	for _, size := range []int{16 * 1024, 16*1024 + 1} {
		called := false
		r := Runner{Root: t.TempDir(), AuthorizeWrite: func(context.Context, string) error { return nil },
			Generate: func(context.Context, Request) (string, error) { called = true; return `{"edits":[]}`, nil },
			Verify:   func(context.Context) (Evidence, error) { return Evidence{Passed: true}, nil },
		}
		_, err := r.Run(context.Background(), Task{Prompt: strings.Repeat("a", size), Files: []string{"new.txt"}})
		if size == 16*1024 {
			if err != nil || !called {
				t.Fatalf("valid boundary prompt rejected: called=%v err=%v", called, err)
			}
		} else if err == nil || called {
			t.Fatalf("oversized prompt reached model: called=%v err=%v", called, err)
		}
	}
}
