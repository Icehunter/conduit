package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/tui/workinganim"
)

func TestSpinnerClock(t *testing.T) {
	tests := []struct {
		name        string
		running     bool
		ticking     bool
		wantTicking bool
		wantCmd     bool
		msg         any
	}{
		{name: "step while idle stops the clock", running: false, ticking: true, msg: workinganim.StepMsg{}, wantTicking: false, wantCmd: false},
		{name: "step while running keeps the clock", running: true, ticking: true, msg: workinganim.StepMsg{}, wantTicking: true, wantCmd: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := idleModel()
			m.running = tt.running
			m.animTicking = tt.ticking
			next, cmd := m.Update(tt.msg)
			nm := next.(Model)
			if nm.animTicking != tt.wantTicking {
				t.Errorf("animTicking = %v, want %v", nm.animTicking, tt.wantTicking)
			}
			if (cmd != nil) != tt.wantCmd {
				t.Errorf("cmd non-nil = %v, want %v", cmd != nil, tt.wantCmd)
			}
		})
	}
}

func TestSpinnerClockArmsWhenRunStarts(t *testing.T) {
	m := idleModel()
	m.running = true
	next, cmd := m.Update(clearFlash{})
	nm := next.(Model)
	if !nm.animTicking || cmd == nil {
		t.Fatalf("running model must arm the clock: ticking=%v cmd=%v", nm.animTicking, cmd != nil)
	}
	// Already ticking: no second chain.
	_, cmd2 := nm.Update(clearFlash{})
	if cmd2 != nil {
		t.Fatal("an armed clock must not start a second chain")
	}
}

func TestLocationStatusUsesCache(t *testing.T) {
	m := idleModel()
	if got := m.renderLocationStatus(60); got != "" {
		t.Fatalf("no cached location must render nothing, got %q", got)
	}
	next, _ := m.Update(locationInfoMsg{cwd: "/tmp/proj", branch: "feature/x"})
	got := next.(Model).renderLocationStatus(60)
	if !strings.Contains(got, "feature/x") || !strings.Contains(got, "/tmp/proj") {
		t.Fatalf("cached location not rendered: %q", got)
	}
}

func TestAnimationClockFollowsSubagents(t *testing.T) {
	isolateSubagentTracker(t)
	m := idleModel()
	if _, cmd := m.Update(clearFlash{}); cmd != nil {
		t.Fatal("idle model must not arm the clock")
	}

	subagent.Default.Add(subagent.Entry{ID: "a1", Label: "worker", StartedAt: time.Now()})
	next, cmd := m.Update(clearFlash{})
	nm := next.(Model)
	if !nm.animTicking || cmd == nil {
		t.Fatalf("running sub-agent must arm the clock: ticking=%v cmd=%v", nm.animTicking, cmd != nil)
	}
	if _, cmd := nm.Update(workinganim.StepMsg{}); cmd == nil {
		t.Error("clock must keep ticking while a sub-agent runs")
	}

	subagent.Default.Remove("a1")
	next, cmd = nm.Update(workinganim.StepMsg{})
	if next.(Model).animTicking || cmd != nil {
		t.Errorf("clock must stop once nothing runs: ticking=%v cmd=%v", next.(Model).animTicking, cmd != nil)
	}
}
