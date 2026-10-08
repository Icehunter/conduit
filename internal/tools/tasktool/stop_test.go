//go:build !windows

package tasktool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/bgjobs"
	"github.com/icehunter/conduit/internal/tool"
)

func TestTaskStop(t *testing.T) {
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	jobs := bgjobs.NewRegistry(10)
	t.Cleanup(jobs.KillAll)
	store := &Store{tasks: make(map[string]*Task), nextID: 1}
	todo := store.Create("write docs", "d", "", nil)

	newTool := func(name string) tool.Tool {
		base := &TaskStopTool{store: store, jobs: jobs}
		if name == "TaskStop" {
			return base
		}
		base.legacy = true
		return &LegacyStopTool{TaskStopTool: base, name: name}
	}
	startJob := func(t *testing.T) string {
		t.Helper()
		j, err := jobs.Start("sleep 30", "", 0, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		return j.ID
	}

	tests := []struct {
		name     string
		toolName string
		input    func(t *testing.T) string
		wantErr  bool
		check    func(t *testing.T, text string)
	}{
		{"job via task_id", "TaskStop", func(t *testing.T) string { return `{"task_id":"` + startJob(t) + `"}` }, false,
			func(t *testing.T, text string) {
				var out struct {
					Success bool   `json:"success"`
					Status  string `json:"status"`
					TaskID  string `json:"task_id"`
				}
				if err := json.Unmarshal([]byte(text), &out); err != nil || !out.Success || out.Status != "killed" {
					t.Errorf("result = %q (%v)", text, err)
				}
				if strings.Contains(text, "exit_code") {
					t.Errorf("killed job reports an exit code: %q", text)
				}
				if j, _ := jobs.Get(out.TaskID); j.Status != bgjobs.StatusKilled {
					t.Errorf("job status = %s", j.Status)
				}
			}},
		{"job via legacy taskId", "TaskStop", func(t *testing.T) string { return `{"taskId":"` + startJob(t) + `"}` }, false,
			func(t *testing.T, text string) {
				if !strings.Contains(text, `"status":"killed"`) {
					t.Errorf("result = %q", text)
				}
			}},
		{"KillShell alias with shell_id", "KillShell", func(t *testing.T) string { return `{"shell_id":"` + startJob(t) + `"}` }, false,
			func(t *testing.T, text string) {
				if !strings.Contains(text, `"status":"killed"`) {
					t.Errorf("result = %q", text)
				}
			}},
		{"KillBash alias", "KillBash", func(t *testing.T) string { return `{"task_id":"` + startJob(t) + `"}` }, false,
			func(t *testing.T, text string) {
				if !strings.Contains(text, `"status":"killed"`) {
					t.Errorf("result = %q", text)
				}
			}},
		{"todo task still cancels", "TaskStop", func(*testing.T) string { return `{"task_id":"` + todo.ID + `","reason":"obsolete"}` }, false,
			func(t *testing.T, text string) {
				got, _ := store.Get(todo.ID)
				if text != `{"success":true}` || got.Status != StatusCancelled || !strings.Contains(got.Output, "Stopped: obsolete") {
					t.Errorf("text = %q, task = %+v", text, got)
				}
			}},
		{"unknown id", "TaskStop", func(*testing.T) string { return `{"task_id":"nope"}` }, true, nil},
		{"missing id", "TaskStop", func(*testing.T) string { return `{}` }, true, nil},
		{"malformed", "TaskStop", func(*testing.T) string { return `{` }, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := newTool(tt.toolName)
			if tool.Name() != tt.toolName {
				t.Fatalf("Name = %q", tool.Name())
			}
			res, err := tool.Execute(context.Background(), json.RawMessage(tt.input(t)))
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, want %v: %v", res.IsError, tt.wantErr, res.Content)
			}
			if tt.check != nil {
				tt.check(t, res.Content[0].Text)
			}
		})
	}

	t.Run("deferral", func(t *testing.T) {
		deferrable := func(name string) bool {
			d, ok := newTool(name).(interface{ Deferrable() bool })
			return ok && d.Deferrable()
		}
		if deferrable("TaskStop") || !deferrable("KillShell") || !deferrable("KillBash") {
			t.Error("only the legacy names should be deferrable")
		}
	})
}
