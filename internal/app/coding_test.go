package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/coding"
	"github.com/icehunter/conduit/internal/episodes"
	"github.com/icehunter/conduit/internal/implguard"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

func codingFixture(t *testing.T, denyRead bool) (*CodingRuntime, *atomic.Int32, string) {
	t.Helper()
	t.Setenv("CLAUDE_CODE_SANDBOXED", "1")
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	for name, content := range map[string]string{"answer.txt": "wrong", "expected.txt": "correct"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var wire struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content := `{"accepted":true,"feedback":"checked cumulative change"}`
		if wire.Model == "qwen" {
			n := calls.Add(1)
			if n == 1 {
				content = `{"edits":[{"path":"answer.txt","old":"wrong","new":"still wrong"}]}`
			} else {
				if !strings.Contains(wire.Messages[len(wire.Messages)-1].Content, "exitCode") {
					t.Error("repair request has no verification diagnostics")
				}
				content = `{"edits":[{"path":"answer.txt","old":"still wrong","new":"correct"}]}`
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"id": "test", "choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	store := secure.NewMemoryStorage()
	if err := settings.SaveProviderCredential(store, "fixture", "none"); err != nil {
		t.Fatal(err)
	}
	local := settings.ActiveProviderSettings{Kind: settings.ProviderKindOpenAICompatible, Credential: "fixture", Model: "qwen", BaseURL: srv.URL, EndpointID: "local"}
	cloud := local
	cloud.Model = "reviewer"
	cloud.EndpointID = "cloud"
	merged := &settings.Merged{Providers: map[string]settings.ActiveProviderSettings{"local": local, "cloud": cloud}, Roles: map[string]string{"implement": "local", "review": "cloud"}}
	policies := settings.CodingPolicies{DefinitionOfDone: &settings.DefinitionOfDoneSettings{Enabled: true, MaxRepairAttempts: 1, RequireReview: true, Checks: []settings.VerificationCheck{{ID: "answer", Command: "git diff --no-index expected.txt answer.txt"}}, ProtectedPaths: []string{"expected.txt"}}, Episodes: &settings.EpisodeSettings{Enabled: true, IncludeContent: true, AllowedPaths: []string{"*.txt"}}}
	var deny []string
	if denyRead {
		deny = []string{"Read"}
	}
	gate := permissions.New(root, nil, permissions.ModeDefault, []string{"Read", "Edit", "Bash"}, deny, nil)
	rt, err := NewCodingRuntime(CodingOptions{Root: root, SessionID: "fixture", Settings: merged, Policies: &policies, Gate: gate, Secure: store, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rt.Close(); err != nil {
			t.Error(err)
		}
	})
	return rt, &calls, root
}

func TestCodingRuntimeRepairsChecksReviewsAndExports(t *testing.T) {
	rt, calls, root := codingFixture(t, false)
	out, err := rt.Run(context.Background(), coding.Task{Prompt: "make answer correct", Files: []string{"answer.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "verified" || out.Attempts != 2 || calls.Load() != 2 {
		t.Fatalf("out=%+v calls=%d", out, calls.Load())
	}
	if !strings.Contains(out.Patch, "correct") {
		t.Fatalf("patch=%s", out.Patch)
	}
	b, err := os.ReadFile(filepath.Join(root, "answer.txt"))
	if err != nil || string(b) != "correct" {
		t.Fatalf("answer=%q err=%v", b, err)
	}
	decision, err := rt.Completion(nil, nil).Verify(context.Background(), agent.CompletionCandidate{})
	if err != nil || decision.Status != agent.CompletionVerified {
		t.Fatalf("completion=%+v err=%v", decision, err)
	}
	data, err := episodes.ExportFile(rt.recorder.Path(), episodes.ExportOptions{AllowModelReview: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.SFT) != 1 || len(data.DPO) != 0 {
		t.Fatalf("dataset=%+v", data.Manifest)
	}
	strict, err := episodes.ExportFile(rt.recorder.Path(), episodes.ExportOptions{})
	if err != nil || len(strict.SFT) != 0 {
		t.Fatalf("verification/review fabricated human preference: %+v %v", strict.Manifest, err)
	}
	// A subsequent editor change invalidates the earlier review and evidence.
	if err := os.WriteFile(filepath.Join(root, "answer.txt"), []byte("external edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err = rt.Completion(nil, nil).Verify(context.Background(), agent.CompletionCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status == agent.CompletionVerified {
		t.Fatal("stale workspace certified")
	}
}

func TestCodingRuntimeReadDenyPreventsModelDisclosure(t *testing.T) {
	rt, calls, _ := codingFixture(t, true)
	_, err := rt.Run(context.Background(), coding.Task{Prompt: "fix", Files: []string{"answer.txt"}})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestCodingRuntimeDisabledHasNoSideEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	rt, err := NewCodingRuntime(CodingOptions{StateDir: path, Policies: &settings.CodingPolicies{}})
	if err != nil || rt != nil {
		t.Fatalf("runtime=%v error=%v", rt, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(fmt.Sprintf("disabled runtime created state: %v", err))
	}
}

func TestCodingRuntimeRecordsWrittenFilesOnImplGuard(t *testing.T) {
	rt, _, root := codingFixture(t, false)
	guard := implguard.New(func() (bool, string) { return true, "local" })
	rt.opts.ImplGuard = guard
	if _, err := rt.Run(context.Background(), coding.Task{Prompt: "make answer correct", Files: []string{"answer.txt"}}); err != nil {
		t.Fatal(err)
	}
	if !guard.Allowed(filepath.Join(root, "answer.txt")) {
		t.Fatalf("answer.txt not recorded: %v", guard.Touched())
	}
	if guard.Allowed(filepath.Join(root, "expected.txt")) {
		t.Fatal("untouched file recorded")
	}
}
