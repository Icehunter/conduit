//go:build !windows

package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/bgjobs"
)

func TestBashesCommand(t *testing.T) {
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	reg := bgjobs.NewRegistry(10)
	done := make(chan bgjobs.Job, 2)
	reg.SetOnDone(func(j bgjobs.Job) { done <- j })
	t.Cleanup(reg.KillAll)

	empty := bashesCommand(reg, "")
	if empty.Type != "text" || !strings.Contains(empty.Text, "No background shells") {
		t.Errorf("empty list = %+v", empty)
	}

	finished, err := reg.Start("echo shown-output", "print it", 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("job did not finish")
	}
	live, err := reg.Start("sleep 30", "", 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		args     string
		wantType string
		want     []string
		notWant  []string
	}{
		{"list opens a picker", "", "picker", []string{`"value":"` + live.ID + `"`, `"value":"` + finished.ID + `"`, "print it"}, nil},
		{"running job offers kill", live.ID, "picker", []string{"view " + live.ID, "kill " + live.ID}, nil},
		{"finished job has no kill", finished.ID, "picker", []string{"view " + finished.ID}, []string{"kill "}},
		{"view defers the read to the TUI", "view " + finished.ID, "bashes-view", []string{finished.ID}, nil},
		{"unknown job", "bash_404", "error", []string{"bash_404"}, nil},
		{"unknown view", "view bash_404", "error", nil, nil},
		{"bad usage", "frob bash_1", "error", []string{"Usage"}, nil},
		{"kill signals without waiting", "kill " + live.ID, "text", []string{live.ID, "stopping"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := bashesCommand(reg, tt.args)
			if res.Type != tt.wantType {
				t.Fatalf("type = %q (%s), want %q", res.Type, res.Text, tt.wantType)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Text, w) {
					t.Errorf("missing %q in %q", w, res.Text)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(res.Text, w) {
					t.Errorf("unexpected %q in %q", w, res.Text)
				}
			}
			if res.Type == "picker" && res.Model != "bashes" {
				t.Errorf("picker kind = %q", res.Model)
			}
		})
	}
}

func TestRegisterBashesCommands(t *testing.T) {
	r := New()
	RegisterBashesCommands(r)
	for _, name := range []string{"/bashes", "/jobs"} {
		if res, ok := r.Dispatch(name); !ok || res.Type == "error" {
			t.Errorf("Dispatch(%q) = %+v, %v", name, res, ok)
		}
	}
}

func TestBashesView(t *testing.T) {
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	reg := bgjobs.NewRegistry(10)
	done := make(chan bgjobs.Job, 1)
	reg.SetOnDone(func(j bgjobs.Job) { done <- j })
	j, err := reg.Start("echo shown-output", "", 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	res := BashesView(reg, j.ID)
	if res.Type != "text" || !strings.Contains(res.Text, "shown-output") || !strings.Contains(res.Text, j.ID) {
		t.Errorf("view = %+v", res)
	}
	if res := BashesView(reg, "bash_404"); res.Type != "error" {
		t.Errorf("unknown view = %+v", res)
	}
}
