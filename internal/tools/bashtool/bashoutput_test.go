//go:build !windows

package bashtool

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/bgjobs"
	"github.com/icehunter/conduit/internal/tool"
)

func waitJob(t *testing.T, ch <-chan bgjobs.Job) bgjobs.Job {
	t.Helper()
	select {
	case j := <-ch:
		return j
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the background job")
		return bgjobs.Job{}
	}
}

func TestBash_RunInBackground(t *testing.T) {
	done := make(chan bgjobs.Job, 1)
	bgjobs.Default.SetOnDone(func(j bgjobs.Job) { done <- j })
	t.Cleanup(func() {
		bgjobs.Default.SetOnDone(nil)
		bgjobs.Default.KillAll()
	})

	res, err := New(nil).Execute(context.Background(),
		json.RawMessage(`{"command":"echo bg-output","description":"echo it","run_in_background":true}`))
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %+v", err, res)
	}
	text := res.Content[0].Text
	m := regexp.MustCompile(`ID: (bash_\d+)\. Output is being written to: (\S+)\. You will be notified when it completes — do not poll\.`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("unexpected result: %q", text)
	}
	j := waitJob(t, done)
	if j.ID != m[1] || j.OutputPath != m[2] || j.Status != bgjobs.StatusCompleted {
		t.Errorf("job = %+v, result named %v", j, m)
	}
	if out, _ := os.ReadFile(j.OutputPath); string(out) != "bg-output\n" {
		t.Errorf("output = %q", out)
	}
}

func TestBash_BackgroundEnvAndDescription(t *testing.T) {
	if !strings.Contains((&Tool{}).Description(), "run_in_background") {
		t.Error("description does not explain run_in_background")
	}
	if !strings.Contains(string((&Tool{}).InputSchema()), `"run_in_background"`) {
		t.Error("schema lacks run_in_background")
	}
	done := make(chan bgjobs.Job, 1)
	bgjobs.Default.SetOnDone(func(j bgjobs.Job) { done <- j })
	t.Cleanup(func() { bgjobs.Default.SetOnDone(nil) })
	if _, err := New(map[string]string{"BG_ENV": "from-settings"}).Execute(context.Background(),
		json.RawMessage(`{"command":"echo $BG_ENV","run_in_background":true}`)); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, done)
	if out, _ := os.ReadFile(j.OutputPath); string(out) != "from-settings\n" {
		t.Errorf("output = %q", out)
	}
}

func TestBashOutput(t *testing.T) {
	reg := bgjobs.NewRegistry(10)
	done := make(chan bgjobs.Job, 4)
	reg.SetOnDone(func(j bgjobs.Job) { done <- j })
	t.Cleanup(reg.KillAll)
	bo := &OutputTool{jobs: reg}

	run := func(t *testing.T, command string) bgjobs.Job {
		t.Helper()
		started, err := reg.Start(command, "", 0, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		waitJob(t, done)
		j, _ := reg.Get(started.ID)
		return j
	}
	read := func(t *testing.T, input string) (string, bool) {
		t.Helper()
		res, err := bo.Execute(context.Background(), json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].Text, res.IsError
	}

	t.Run("incremental offsets", func(t *testing.T) {
		j := run(t, "echo first; echo second")
		in := `{"bash_id":"` + j.ID + `"}`
		if got, _ := read(t, in); got != "Status: completed\nExit code: 0\nfirst\nsecond" {
			t.Errorf("first read = %q", got)
		}
		if got, _ := read(t, in); got != "Status: completed\nExit code: 0\n(no new output)" {
			t.Errorf("second read = %q", got)
		}
		f, err := os.OpenFile(j.OutputPath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("third\n")
		_ = f.Close()
		if got, _ := read(t, in); !strings.HasSuffix(got, "\nthird") || strings.Contains(got, "first") {
			t.Errorf("third read = %q", got)
		}
	})

	t.Run("filter", func(t *testing.T) {
		j := run(t, "echo apple; echo banana; echo apricot")
		got, isErr := read(t, `{"bash_id":"`+j.ID+`","filter":"^ap"}`)
		if isErr || !strings.HasSuffix(got, "\napple\napricot") || strings.Contains(got, "banana") {
			t.Errorf("filtered = %q", got)
		}
	})

	t.Run("failed status and exit code", func(t *testing.T) {
		j := run(t, "echo boom; exit 7")
		got, _ := read(t, `{"bash_id":"`+j.ID+`"}`)
		if !strings.HasPrefix(got, "Status: failed\nExit code: 7\n") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("killed job shows no exit code", func(t *testing.T) {
		started, err := reg.Start("echo partial; sleep 30", "", 0, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Kill(started.ID); err != nil {
			t.Fatal(err)
		}
		got, _ := read(t, `{"bash_id":"`+started.ID+`"}`)
		if strings.Contains(got, "Exit code") || !strings.HasPrefix(got, "Status: killed") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("running job", func(t *testing.T) {
		started, err := reg.Start("sleep 30", "", 0, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := read(t, `{"bash_id":"`+started.ID+`"}`)
		if got != "Status: running\n(no new output)" {
			t.Errorf("got %q", got)
		}
		if _, err := reg.Kill(started.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("errors", func(t *testing.T) {
		for name, input := range map[string]string{
			"unknown id":     `{"bash_id":"bash_404"}`,
			"missing id":     `{}`,
			"bad filter":     `{"bash_id":"bash_1","filter":"("}`,
			"malformed json": `{`,
		} {
			if got, isErr := read(t, input); !isErr {
				t.Errorf("%s: want error, got %q", name, got)
			}
		}
	})

	t.Run("traits", func(t *testing.T) {
		if !bo.IsReadOnly(nil) || !bo.IsConcurrencySafe(nil) || bo.Name() != "BashOutput" {
			t.Error("BashOutput must be a read-only, concurrency-safe tool named BashOutput")
		}
	})
}

func TestBash_BackgroundWording(t *testing.T) {
	t.Cleanup(func() {
		bgjobs.Default.SetHeadless(false)
		bgjobs.Default.KillAll()
	})
	tests := []struct {
		name     string
		headless bool
		scope    string
		want     string
		notWant  string
	}{
		{"interactive main loop", false, "", "You will be notified when it completes", "no notification can follow"},
		{"print mode", true, "", "no notification can follow that — so do not end your turn to wait for it", "You will be notified when it completes"},
		{"sub-agent", false, "sub-1", "nothing will notify you when it exits", "You will be notified when it completes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bgjobs.Default.SetHeadless(tt.headless)
			ctx := tool.WithScope(context.Background(), tt.scope)
			res, err := New(nil).Execute(ctx, json.RawMessage(`{"command":"sleep 30","run_in_background":true}`))
			if err != nil || res.IsError {
				t.Fatalf("Execute: %v %+v", err, res)
			}
			text := res.Content[0].Text
			if !strings.Contains(text, tt.want) || strings.Contains(text, tt.notWant) {
				t.Errorf("result = %q", text)
			}
			desc := (&Tool{}).Description()
			if tt.headless != strings.Contains(desc, "no notification can follow that") {
				t.Errorf("description headless wording mismatch (headless=%v)", tt.headless)
			}
		})
	}
}

func TestBash_ReleaseScopeStopsChildJobsSilently(t *testing.T) {
	notified := make(chan bgjobs.Job, 4)
	bgjobs.Default.SetOnDone(func(j bgjobs.Job) { notified <- j })
	t.Cleanup(func() {
		bgjobs.Default.SetOnDone(nil)
		bgjobs.Default.KillAll()
	})
	bt := New(nil)
	ctx := tool.WithScope(context.Background(), "sub-rel")
	res, err := bt.Execute(ctx, json.RawMessage(`{"command":"sleep 30","run_in_background":true}`))
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %+v", err, res)
	}
	id := regexp.MustCompile(`bash_\d+`).FindString(res.Content[0].Text)
	bt.ReleaseScope("sub-rel")
	if j, _ := bgjobs.Default.Get(id); j.Status != bgjobs.StatusKilled {
		t.Errorf("status = %s, want killed", j.Status)
	}
	select {
	case j := <-notified:
		t.Errorf("child job notified the main loop: %+v", j)
	default:
	}
}
