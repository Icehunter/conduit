package evaluation

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/verification"
)

func fixture(t *testing.T) Task {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{"value.txt": "before\n", "check.sh": "test \"$(cat value.txt)\" = after\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	return Task{Version: 1, ID: "fix-value", RepoPath: root, Commit: git("rev-parse", "HEAD"), Prompt: "Set value to after", ModelRevision: "local-artifact-sha256:abc", Budget: Budget{TimeoutSeconds: 20, MaxTurns: 2, MaxTokens: 100}, Policy: verification.Policy{Enabled: true, Checks: []verification.Check{{ID: "test", Command: "sh check.sh", TimeoutSeconds: 5}}}, ProtectedPaths: []string{"check.sh"}}
}

func TestRunIsolationAndPassing(t *testing.T) {
	task := fixture(t)
	var workspace string
	result, err := Run(context.Background(), task, Options{Authorize: func(context.Context, verification.Check) error { return nil }, Implement: func(ctx context.Context, w Workspace) (Usage, error) {
		workspace = w.Root
		if workspace == task.RepoPath || w.Origin != "evaluation" || !w.DisableMemory {
			t.Error("evaluation isolation contract missing")
		}
		return Usage{Turns: 1, Tokens: 20, ModelRevision: task.ModelRevision}, os.WriteFile(filepath.Join(w.Root, "value.txt"), []byte("after\n"), 0600)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" {
		t.Fatalf("result=%+v", result)
	}
	b, err := os.ReadFile(filepath.Join(task.RepoPath, "value.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "before\n" {
		t.Fatal("source checkout changed")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatal("evaluation workspace retained")
	}
}

func TestRunRefusesInvalidEvidence(t *testing.T) {
	for _, scenario := range []string{"protected", "budget", "revision", "cancelled", "unauthorized"} {
		t.Run(scenario, func(t *testing.T) {
			task := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := Options{Authorize: func(context.Context, verification.Check) error { return nil }, Implement: func(ctx context.Context, w Workspace) (Usage, error) {
				u := Usage{Turns: 1, Tokens: 20, ModelRevision: task.ModelRevision}
				switch scenario {
				case "protected":
					if err := os.WriteFile(filepath.Join(w.Root, "check.sh"), []byte("true\n"), 0600); err != nil {
						return u, err
					}
				case "budget":
					u.Tokens = 101
				case "revision":
					u.ModelRevision = "different"
				case "cancelled":
					cancel()
				}
				return u, os.WriteFile(filepath.Join(w.Root, "value.txt"), []byte("after\n"), 0600)
			}}
			if scenario == "unauthorized" {
				opts.Authorize = nil
			}
			out, _ := Run(ctx, task, opts)
			if out.Status == "passed" {
				t.Fatal("invalid evidence passed")
			}
		})
	}
}

func TestManifestRequiresPinnedInputs(t *testing.T) {
	for _, field := range []string{"commit", "budget", "checks", "protected", "revision"} {
		t.Run(field, func(t *testing.T) {
			task := fixture(t)
			switch field {
			case "commit":
				task.Commit = "HEAD"
			case "budget":
				task.Budget.MaxTokens = 0
			case "checks":
				task.Policy.Checks = nil
			case "protected":
				task.ProtectedPaths = []string{"../outside"}
			case "revision":
				task.ModelRevision = "latest"
			}
			if err := Validate(task); err == nil {
				t.Fatal("accepted unpinned manifest")
			}
		})
	}
}

func TestLoadManifestRejectsTrailingDocument(t *testing.T) {
	task := fixture(t)
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "task.json")
	if err := os.WriteFile(path, append(b, []byte("\n{}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("accepted trailing manifest")
	}
}
