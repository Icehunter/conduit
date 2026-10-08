//go:build !windows

package bgjobs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// newTestRegistry returns a registry whose output files land in a temp dir,
// and a channel that receives each job the OnDone hook reports.
func newTestRegistry(t *testing.T) (*Registry, chan Job) {
	t.Helper()
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	r := NewRegistry(20)
	done := make(chan Job, 64)
	r.SetOnDone(func(j Job) { done <- j })
	t.Cleanup(r.KillAll)
	return r, done
}

func recv(t *testing.T, ch <-chan Job) Job {
	t.Helper()
	select {
	case j := <-ch:
		return j
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the job to end")
		return Job{}
	}
}

func TestStartLifecycle(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		desc       string
		wantStatus Status
		wantExit   int
		wantOut    string
		wantNotif  []string
	}{
		{
			name: "success", command: "echo hello", desc: "say hello",
			wantStatus: StatusCompleted, wantExit: 0, wantOut: "hello\n",
			wantNotif: []string{"<status>completed</status>", `Background command &quot;say hello&quot; completed (exit 0)`, "hello", "Full output: "},
		},
		{
			name: "nonzero exit", command: "echo oops >&2; exit 3",
			wantStatus: StatusFailed, wantExit: 3, wantOut: "oops\n",
			wantNotif: []string{"<status>failed</status>", "failed (exit 3)", "oops"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, done := newTestRegistry(t)
			started, err := r.Start(tt.command, tt.desc, 0, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if started.Status != StatusRunning || started.PID == 0 || !strings.HasPrefix(started.ID, "bash_") {
				t.Errorf("started = %+v", started)
			}
			got := recv(t, done)
			if got.ID != started.ID || got.Status != tt.wantStatus || got.ExitCode != tt.wantExit {
				t.Errorf("done = %+v, want status %s exit %d", got, tt.wantStatus, tt.wantExit)
			}
			if got.DoneAt.IsZero() {
				t.Error("DoneAt not set")
			}
			out, err := os.ReadFile(got.OutputPath)
			if err != nil || string(out) != tt.wantOut {
				t.Errorf("output = %q, %v; want %q", out, err, tt.wantOut)
			}
			notif := r.Notification(got)
			for _, want := range append(tt.wantNotif, "<task-id>"+got.ID+"</task-id>", got.OutputPath) {
				if !strings.Contains(notif, want) {
					t.Errorf("notification missing %q:\n%s", want, notif)
				}
			}
			if r.Running() != 0 {
				t.Errorf("Running() = %d after done", r.Running())
			}
		})
	}
}

func TestStartErrors(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, err := r.Start("", "", 0, "", nil); err == nil {
		t.Error("empty command should fail")
	}
	if _, err := r.Start("true", "", 0, filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Error("missing cwd should fail")
	}
	if _, ok := r.Get("bash_999"); ok {
		t.Error("Get of unknown id succeeded")
	}
	if _, err := r.Kill("bash_999"); err == nil {
		t.Error("Kill of unknown id should fail")
	}
	if _, err := r.ReadNext("bash_999"); err == nil {
		t.Error("ReadNext of unknown id should fail")
	}
}

func TestCwdAndEnv(t *testing.T) {
	r, done := newTestRegistry(t)
	dir := t.TempDir()
	if _, err := r.Start(`pwd; echo "$BGJOB_VAR"`, "", 0, dir, []string{"BGJOB_VAR=marker"}); err != nil {
		t.Fatal(err)
	}
	j := recv(t, done)
	out, _ := os.ReadFile(j.OutputPath)
	want, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(string(out), "marker") || !strings.Contains(string(out), filepath.Base(want)) {
		t.Errorf("output = %q", out)
	}
}

func TestKill(t *testing.T) {
	r, done := newTestRegistry(t)
	started, err := r.Start("sleep 30", "", 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Running() != 1 {
		t.Fatalf("Running() = %d, want 1", r.Running())
	}
	killed, err := r.Kill(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if killed.Status != StatusKilled || killed.TimedOut {
		t.Errorf("killed = %+v", killed)
	}
	select {
	case j := <-done:
		t.Errorf("a stop the caller asked for notified: %+v", j)
	default:
	}
	// Killing a finished job reports it as it ended.
	again, err := r.Kill(started.ID)
	if err != nil || again.Status != StatusKilled {
		t.Errorf("second Kill = %+v, %v", again, err)
	}
}

func TestTimeout(t *testing.T) {
	r, done := newTestRegistry(t)
	if _, err := r.Start("sleep 10", "", 50*time.Millisecond, "", nil); err != nil {
		t.Fatal(err)
	}
	got := recv(t, done)
	if got.Status != StatusKilled || !got.TimedOut {
		t.Errorf("job = %+v, want killed by timeout", got)
	}
	if n := r.Notification(got); !strings.Contains(n, "timed out") {
		t.Errorf("notification does not mention the timeout:\n%s", n)
	}
}

func TestKillAll(t *testing.T) {
	r, _ := newTestRegistry(t)
	var calls atomic.Int32
	r.SetOnDone(func(Job) { calls.Add(1) })
	for range 3 {
		if _, err := r.Start("sleep 30", "", 0, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	r.KillAll()
	if r.Running() != 0 {
		t.Errorf("Running() = %d after KillAll", r.Running())
	}
	for _, j := range r.Snapshot() {
		if j.Status != StatusKilled {
			t.Errorf("%s status = %s", j.ID, j.Status)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("OnDone fired %d times during shutdown", calls.Load())
	}
}

func TestOutputFilePermissions(t *testing.T) {
	r, done := newTestRegistry(t)
	if _, err := r.Start("echo secret", "", 0, "", nil); err != nil {
		t.Fatal(err)
	}
	j := recv(t, done)
	fi, err := os.Stat(j.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(j.OutputPath))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}
}

func TestReadNextIncremental(t *testing.T) {
	r, done := newTestRegistry(t)
	started, err := r.Start("echo one; echo two", "", 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	j := recv(t, done)

	steps := []struct {
		name     string
		appendTo string
		want     string
	}{
		{"first read sees everything", "", "one\ntwo\n"},
		{"second read sees nothing", "", ""},
		{"read after append sees only the new text", "three\n", "three\n"},
		{"read after nothing new", "", ""},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			if s.appendTo != "" {
				f, err := os.OpenFile(j.OutputPath, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString(s.appendTo); err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
			}
			out, err := r.ReadNext(started.ID)
			if err != nil {
				t.Fatal(err)
			}
			if out.Text != s.want {
				t.Errorf("ReadNext = %q, want %q", out.Text, s.want)
			}
		})
	}

	// ReadOutput is stateless: it reads from the given offset every time.
	for range 2 {
		out, err := r.ReadOutput(started.ID, 4)
		if err != nil || out.Text != "two\nthree\n" {
			t.Errorf("ReadOutput(4) = %q, %v", out.Text, err)
		}
	}
	tail, err := r.Tail(started.ID, 6)
	if err != nil || tail != "three\n" {
		t.Errorf("Tail(6) = %q, %v", tail, err)
	}
}

func TestReadFromSkipsOversizedBacklog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, offset, skipped, err := readFrom(path, 0, 4)
	if err != nil || text != "6789" || offset != 10 || skipped != 6 {
		t.Errorf("readFrom = %q %d %d %v", text, offset, skipped, err)
	}
}

func TestEviction(t *testing.T) {
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	r := NewRegistry(2)
	done := make(chan Job, 8)
	r.SetOnDone(func(j Job) { done <- j })
	ids := make([]string, 0, 4)
	for range 4 {
		j, err := r.Start("true", "", 0, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
		recv(t, done)
	}
	if got := len(r.Snapshot()); got != 2 {
		t.Errorf("retained %d jobs, want 2", got)
	}
	if _, ok := r.Get(ids[0]); ok {
		t.Error("oldest job was not evicted")
	}
}

// TestRegistryConcurrency exercises every entry point from many goroutines;
// it exists for the race detector.
func TestRegistryConcurrency(t *testing.T) {
	r, done := newTestRegistry(t)
	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := r.Start("echo x", "", 0, "", nil)
			if err != nil {
				t.Error(err)
				return
			}
			if i%2 == 0 {
				_, _ = r.Kill(j.ID)
			}
			_ = r.Snapshot()
			_ = r.Running()
			_, _ = r.ReadNext(j.ID)
			_, _ = r.Get(j.ID)
			r.SetOnDone(func(Job) {})
		}()
	}
	wg.Wait()
	r.SetOnDone(func(j Job) { done <- j })
	r.KillAll()
	if r.Running() != 0 {
		t.Errorf("Running() = %d", r.Running())
	}
}

func TestNotificationShapes(t *testing.T) {
	long := strings.Repeat("word ", 40)
	tests := []struct {
		name string
		job  Job
		tail string
		want []string
	}{
		{"description wins over command", Job{ID: "bash_1", Command: "make", Description: "build it", Status: StatusCompleted, OutputPath: "/p"}, "", []string{"build it", "Full output: /p"}},
		{"long command is truncated", Job{ID: "bash_2", Command: long, Status: StatusFailed, ExitCode: 2, OutputPath: "/p"}, "tail text\n", []string{"…", "tail text", "exit 2"}},
		{"killed", Job{ID: "bash_3", Command: "x", Status: StatusKilled, OutputPath: "/p"}, "", []string{"<status>killed</status>", "was stopped"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Notification(tt.job, tt.tail)
			if !strings.HasPrefix(got, "<task-notification>") || !strings.HasSuffix(got, "</task-notification>") {
				t.Errorf("not wrapped: %s", got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in %s", w, got)
				}
			}
		})
	}
}

func TestStopReturnsBeforeReap(t *testing.T) {
	r, _ := newTestRegistry(t)
	j, err := r.Start("sleep 30", "", 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(j.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop("bash_404"); err == nil {
		t.Error("Stop of unknown id should fail")
	}
	// The job settles to killed once reaped.
	if got, _ := r.Kill(j.ID); got.Status != StatusKilled {
		t.Errorf("status = %s", got.Status)
	}
}

func TestOwnedJobsAreSilentAndReleasedByOwner(t *testing.T) {
	r, done := newTestRegistry(t)
	quick, err := r.StartOwned("sub-1", "echo hi", "", 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	long1, _ := r.StartOwned("sub-1", "sleep 30", "", 0, "", nil)
	long2, _ := r.StartOwned("sub-2", "sleep 30", "", 0, "", nil)
	mainJob, _ := r.Start("sleep 30", "", 0, "", nil)

	r.KillOwned("sub-1")
	r.KillOwned("")
	for id, want := range map[string]Status{long1.ID: StatusKilled, long2.ID: StatusRunning, mainJob.ID: StatusRunning} {
		if got, _ := r.Get(id); got.Status != want {
			t.Errorf("%s status = %s, want %s", id, got.Status, want)
		}
	}
	r.KillAll()
	select {
	case j := <-done:
		t.Errorf("owned or killed job notified: %+v", j)
	default:
	}
	if got, _ := r.Get(quick.ID); got.Owner != "sub-1" {
		t.Errorf("owner = %q", got.Owner)
	}
}

func TestReadNextHoldsBackPartialLine(t *testing.T) {
	r, done := newTestRegistry(t)
	// The job stays running while the file is edited by hand.
	started, err := r.Start("sleep 30", "", 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	append := func(s string) {
		f, err := os.OpenFile(started.OutputPath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(s)
		_ = f.Close()
	}
	steps := []struct{ add, want string }{
		{"one\ntw", "one\n"},
		{"", ""},
		{"o\nthr", "two\n"},
	}
	for _, s := range steps {
		append(s.add)
		out, err := r.ReadNext(started.ID)
		if err != nil || out.Text != s.want {
			t.Fatalf("ReadNext = %q, %v; want %q", out.Text, err, s.want)
		}
	}
	// Once the job has ended, the unterminated tail is flushed.
	if _, err := r.Kill(started.ID); err != nil {
		t.Fatal(err)
	}
	out, _ := r.ReadNext(started.ID)
	if out.Text != "thr" {
		t.Errorf("flush = %q, want %q", out.Text, "thr")
	}
	_ = done
}

func TestReadFromRuneBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	// "é" is two bytes (0xC3 0xA9); a limit of 4 would start inside the
	// second one.
	if err := os.WriteFile(path, []byte("aéééb"), 0o600); err != nil {
		t.Fatal(err)
	}
	for limit := int64(1); limit <= 8; limit++ {
		text, _, _, err := readFrom(path, 0, limit)
		if err != nil || !utf8.ValidString(text) {
			t.Errorf("limit %d: %q (valid=%v, %v)", limit, text, utf8.ValidString(text), err)
		}
	}
}
