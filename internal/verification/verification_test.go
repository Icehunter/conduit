package verification

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func testPolicy(command string) Policy {
	return Policy{Enabled: true, Checks: []Check{{ID: "test", Command: command, TimeoutSeconds: 5}}, TotalTimeoutSeconds: 10}
}

func allow(context.Context, Check) error { return nil }

func TestRunnerExitAndSanitizedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixture")
	}
	for _, tc := range []struct {
		name, command string
		want          Status
		code          int
	}{
		{"pass", "printf ok", Passed, 0},
		{"failure", "printf 'AKIAIOSFODNN7EXAMPLE\\n'; exit 3", Failed, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewRunner(fixtureRepo(t), testPolicy(tc.command), Options{Authorize: allow})
			if err != nil {
				t.Fatal(err)
			}
			report, err := r.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != tc.want || len(report.Checks) != 1 || report.Checks[0].ExitCode != tc.code {
				t.Fatalf("report = %+v", report)
			}
			if strings.Contains(report.Checks[0].Output, "AKIAIOSFODNN7EXAMPLE") {
				t.Fatal("secret retained")
			}
			if report.PolicyHash == "" || report.WorkspaceBefore == "" || report.WorkspaceAfter != report.WorkspaceBefore {
				t.Fatalf("missing fingerprints: %+v", report)
			}
		})
	}
}

func TestRunnerPermissionAndCancellation(t *testing.T) {
	denied := errors.New("permission denied")
	r, err := NewRunner(fixtureRepo(t), testPolicy("echo never"), Options{Authorize: func(context.Context, Check) error { return denied }})
	if err != nil {
		t.Fatal(err)
	}
	report, err := r.Run(context.Background())
	if err != nil || report.Status != Blocked || len(report.Checks) != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err = r.Run(ctx)
	if !errors.Is(err, context.Canceled) || report.Status != Cancelled {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestPolicyValidationAndFrozenCopy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy Policy
	}{
		{"empty checks", Policy{Enabled: true}},
		{"duplicate", Policy{Enabled: true, Checks: []Check{{ID: "same", Command: "echo a"}, {ID: "same", Command: "echo b"}}}},
		{"escape", Policy{Enabled: true, Checks: []Check{{ID: "x", Command: "echo x", Cwd: "../outside"}}}},
		{"negative timeout", Policy{Enabled: true, Checks: []Check{{ID: "x", Command: "echo x", TimeoutSeconds: -1}}}},
		{"negative repairs", Policy{Enabled: true, Checks: []Check{{ID: "x", Command: "echo x"}}, MaxRepairAttempts: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRunner(fixtureRepo(t), tc.policy, Options{Authorize: allow}); err == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
	p := testPolicy("echo original")
	r, err := NewRunner(fixtureRepo(t), p, Options{Authorize: allow})
	if err != nil {
		t.Fatal(err)
	}
	hash := r.PolicyHash()
	p.Checks[0].Command = "echo changed"
	if r.PolicyHash() != hash {
		t.Fatal("caller changed frozen policy")
	}
}

func TestWorkspaceFingerprintIncludesUntrackedAndDirty(t *testing.T) {
	dir := fixtureRepo(t)
	before, err := WorkspaceFingerprint(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "source.go")
	if err := os.WriteFile(p, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	untracked, err := WorkspaceFingerprint(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if untracked == before {
		t.Fatal("untracked file omitted")
	}
	if err := os.WriteFile(p, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := WorkspaceFingerprint(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if changed == untracked {
		t.Fatal("untracked bytes omitted")
	}
	cmd := exec.Command("git", "-C", dir, "add", "source.go")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	staged, err := WorkspaceFingerprint(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if staged == changed {
		t.Fatal("index state omitted")
	}
}

func TestRunnerDetectsCheckMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixture")
	}
	r, err := NewRunner(fixtureRepo(t), testPolicy("printf changed > source.go"), Options{Authorize: allow})
	if err != nil {
		t.Fatal(err)
	}
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != Changed || report.WorkspaceBefore == report.WorkspaceAfter {
		t.Fatalf("report=%+v", report)
	}
}

func TestRunnerTimeoutAndBoundedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixture")
	}
	p := testPolicy("sleep 30 & wait")
	p.Checks[0].TimeoutSeconds = 1
	r, err := NewRunner(fixtureRepo(t), p, Options{Authorize: allow})
	if err != nil {
		t.Fatal(err)
	}
	report, err := r.Run(context.Background())
	if err != nil || report.Status != TimedOut || report.Checks[0].Status != TimedOut {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	r, err = NewRunner(fixtureRepo(t), testPolicy("for ((i=0;i<2000;i++)); do printf 'line with result\\n'; done; printf 'final failure\\n'; exit 7"), Options{Authorize: allow, OutputLimitBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	report, err = r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := report.Checks[0]
	if !result.Truncated || len(result.Output) > 300 || !strings.Contains(result.Output, "final failure") || result.ExitCode != 7 {
		t.Fatalf("result=%+v", result)
	}
}

func TestCaptureChunkBoundaries(t *testing.T) {
	for length := 0; length < 64; length++ {
		for chunk := 1; chunk < 17; chunk++ {
			c := newCapture(64)
			text := strings.Repeat("x", length)
			for i := 0; i < len(text); i += chunk {
				_, _ = c.Write([]byte(text[i:min(i+chunk, len(text))]))
			}
			got, _, truncated := c.result()
			if got != text || truncated {
				t.Fatalf("length=%d chunk=%d got=%q", length, chunk, got)
			}
		}
	}
}

func TestProtectedDefinitionsCannotChangeOrAppear(t *testing.T) {
	for _, tc := range []struct{ name, path, initial string }{
		{"manifest edit", "Makefile", "test: true"},
		{"policy insertion", ".conduit/settings.local.json", ""},
		{"test glob edit", "tests/assert.txt", "initial assertion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureRepo(t)
			path := filepath.Join(root, tc.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.initial != "" {
				if err := os.WriteFile(path, []byte(tc.initial), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			r, err := NewRunner(root, testPolicy("echo ok"), Options{Authorize: allow, ProtectedPaths: []string{"tests/**"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("weakened checks"), 0o600); err != nil {
				t.Fatal(err)
			}
			report, err := r.Run(context.Background())
			if err == nil || report.Status != Blocked || len(report.Checks) != 0 {
				t.Fatalf("changed definition executed: %+v %v", report, err)
			}
		})
	}
}

func TestIsProtectedPathBeforeMutation(t *testing.T) {
	r, err := NewRunner(fixtureRepo(t), testPolicy("echo ok"), Options{Authorize: allow, ProtectedPaths: []string{"tests/**", "internal/**/*_test.go"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"Makefile", true}, {"./Makefile", true}, {".conduit/settings.json", true},
		{"tests/new/deep.txt", true}, {"internal/agent/new_test.go", true},
		{"internal/agent/new.go", false}, {"README.md", false},
		{"../outside", true}, {"/outside", true}, {"", true},
	} {
		if got := r.IsProtectedPath(tc.path); got != tc.want {
			t.Errorf("IsProtectedPath(%q)=%v want %v", tc.path, got, tc.want)
		}
	}
}
