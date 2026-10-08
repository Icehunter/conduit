package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/episodes"
	"github.com/icehunter/conduit/internal/evaluation"
	"github.com/icehunter/conduit/internal/verification"
)

func TestEpisodesCLIRefusesAmbiguousLabels(t *testing.T) {
	for _, args := range [][]string{{"accept"}, {"reject", "--source", "reviewer_model"}, {"export", "--format", "other"}, {"export", "--split", "held_out"}} {
		var out bytes.Buffer
		if err := episodesCommand(args, &out); err == nil {
			t.Errorf("accepted invalid arguments %v", args)
		}
	}
}

func TestEpisodesCLILabelsKnownCandidateOnly(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	r, err := episodes.New(episodes.Config{Enabled: true, IncludeContent: true, Dir: dir, RepositoryRoot: repo, SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []episodes.Event{{Kind: episodes.KindStart, EpisodeID: "e", Payload: episodes.Start{RepoID: "r", TaskID: "t", SplitGroup: "t"}}, {Kind: episodes.KindCandidate, EpisodeID: "e", Payload: episodes.Candidate{ID: "candidate"}}, {Kind: episodes.KindEnd, EpisodeID: "e", Payload: episodes.End{Complete: true}}} {
		if err := r.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := episodesCommand([]string{"accept", "--file", r.Path(), "--episode", "e", "--candidate", "missing"}, &out); err == nil {
		t.Fatal("unknown candidate labeled")
	}
	if err := episodesCommand([]string{"accept", "--file", r.Path(), "--episode", "e", "--candidate", "candidate"}, &out); err != nil {
		t.Fatal(err)
	}
	events, err := episodes.ReadFile(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	payload := events[len(events)-1].Payload.(map[string]any)
	if payload["source"] != episodes.SourceExplicitHuman || payload["action"] != "accepted" {
		t.Fatalf("label=%v", payload)
	}
}

func TestEvaluateCLIRequiresExecutionConsent(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{"--execute"}, {"--manifest", "absent", "--implement-argv", `["true"]`}, {"--manifest", "absent", "--execute", "--implement-argv", `["true"]`}} {
		if err := evaluateCommand(args, &out); err == nil {
			t.Fatalf("accepted invalid eval args %v", args)
		}
	}
}

func TestEvaluationCLIHelper(_ *testing.T) {
	for i, arg := range os.Args {
		if arg == "conduit-evaluation-fixture" {
			fmt.Printf("{\"turns\":1,\"tokens\":1,\"model_revision\":%q}\n", os.Args[i+1])
			os.Exit(0)
		}
	}
}

func TestEvaluateCLIFailedCheckReturnsError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "check.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	task := evaluation.Task{Version: 1, ID: "fail", RepoPath: root, Commit: git("rev-parse", "HEAD"), Prompt: "task", ModelRevision: "immutable-artifact", Budget: evaluation.Budget{TimeoutSeconds: 30, MaxTurns: 2, MaxTokens: 10}, Policy: verification.Policy{Enabled: true, Checks: []verification.Check{{ID: "test", Command: "exit 1"}}}, ProtectedPaths: []string{"check.txt"}}
	manifest := filepath.Join(t.TempDir(), "task.json")
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0600); err != nil {
		t.Fatal(err)
	}
	argv, err := json.Marshal([]string{os.Args[0], "-test.run=TestEvaluationCLIHelper", "--", "conduit-evaluation-fixture", task.ModelRevision})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = evaluateCommand([]string{"--manifest", manifest, "--execute", "--authorize-checks", "--implement-argv", string(argv)}, &out)
	if err == nil {
		t.Fatal("failed check returned success")
	}
	var result evaluation.Result
	if e := json.Unmarshal(out.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if result.Status != "failed" {
		t.Fatalf("status=%s err=%v", result.Status, err)
	}
}
