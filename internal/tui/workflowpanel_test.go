package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/workflow"
)

type stubHost struct{ release chan struct{} }

func (h stubHost) RunAgent(ctx context.Context, req workflow.AgentRequest) workflow.AgentResult {
	select {
	case <-h.release:
	case <-ctx.Done():
		return workflow.AgentResult{Err: ctx.Err()}
	}
	return workflow.AgentResult{Value: req.Prompt, OutputTokens: 1}
}

func (stubHost) ResolveWorkflow(context.Context, workflow.WorkflowRef) (string, error) {
	return "", nil
}

// startRun launches a script that blocks on one agent until release is closed.
func startRun(t *testing.T, id string, release chan struct{}) *workflow.Run {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.js")
	src := "export const meta = { name: 'demo-" + id + "', description: 'd', phases: [{ title: 'Go' }] }\nphase('Go')\nlog('hello')\nreturn await agent('p')\n"
	if err := os.WriteFile(scriptPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := workflow.Load(src)
	if err != nil {
		t.Fatal(err)
	}
	r := &workflow.Run{ID: id, ScriptPath: scriptPath, StartedAt: time.Now()}
	workflow.Default.Add(r)
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_, _ = workflow.Execute(context.Background(), r, sc, workflow.Options{Host: stubHost{release: release}})
		close(done)
	}()
	<-started
	t.Cleanup(func() {
		r.Kill()
		<-done
	})
	// Wait until the agent node is registered so the panel has something to show.
	deadline := time.After(5 * time.Second)
	for len(r.Snapshot().Nodes) == 0 {
		select {
		case <-deadline:
			t.Fatal("run never spawned its agent")
		case <-time.After(time.Millisecond):
		}
	}
	return r
}

func TestWorkflowPanel_OpenNavigateKill(t *testing.T) {
	workflow.Default.Reset()
	t.Cleanup(workflow.Default.Reset)
	release := make(chan struct{})
	r := startRun(t, "wf-1", release)

	m := idleModel()
	m, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 100, Height: 40})
	m2, cmd := m.applyCommandResult(commands.Result{Type: "workflow-panel"})
	if m2.workflowPanel == nil || m2.workflowPanel.view != "list" {
		t.Fatalf("panel not opened: %+v", m2.workflowPanel)
	}
	if cmd == nil {
		t.Error("expected refresh tick")
	}
	if list := m2.renderWorkflowList(); !strings.Contains(list, "demo-wf-1") || !strings.Contains(list, "running") {
		t.Errorf("list render = %q", list)
	}

	// Keys route to the panel while it is open.
	m3, _, consumed := m2.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !consumed || m3.workflowPanel == nil || m3.workflowPanel.view != "detail" {
		t.Fatalf("enter did not open detail: consumed=%v panel=%+v", consumed, m3.workflowPanel)
	}
	detail := m3.renderWorkflowDetail()
	for _, want := range []string{"demo-wf-1", "Go 0/1", "1 running", "hello", "x kill"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q:\n%s", want, detail)
		}
	}

	m4, _, _ := m3.handleKey(tea.KeyPressMsg{Code: 'x'})
	deadline := time.After(5 * time.Second)
	for r.Status() != workflow.RunKilled {
		select {
		case <-deadline:
			t.Fatal("x did not kill the run")
		case <-time.After(time.Millisecond):
		}
	}
	if !strings.Contains(m4.flashMsg, "killed") {
		t.Errorf("flash = %q", m4.flashMsg)
	}

	m5, _, _ := m4.handleKey(tea.KeyPressMsg{Code: 'b'})
	if m5.workflowPanel.view != "list" {
		t.Errorf("b did not return to list")
	}
	m6, _, _ := m5.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m6.workflowPanel != nil {
		t.Errorf("esc did not close the panel")
	}
}

func TestWorkflowPanel_SaveRegistersCommand(t *testing.T) {
	workflow.Default.Reset()
	t.Cleanup(workflow.Default.Reset)
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	release := make(chan struct{})
	close(release)
	startRun(t, "wf-save", release)

	m := idleModel()
	m.cfg.Commands = commands.New()
	m = m.openWorkflowPanel()
	m2, _ := m.saveSelectedWorkflow()
	if !strings.Contains(m2.flashMsg, "saved") {
		t.Fatalf("flash = %q", m2.flashMsg)
	}
	if _, err := os.Stat(filepath.Join(workflow.SavedDir(), "demo-wf-save.js")); err != nil {
		t.Errorf("script not saved: %v", err)
	}
	if _, ok := m2.cfg.Commands.Dispatch("/demo-wf-save"); !ok {
		t.Error("saved workflow was not registered as a slash command")
	}
}

func TestWorkflowPanel_EmptyList(t *testing.T) {
	workflow.Default.Reset()
	m := idleModel()
	m, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = m.openWorkflowPanel()
	if list := m.renderWorkflowList(); !strings.Contains(list, "no workflow runs yet") {
		t.Errorf("list = %q", list)
	}
	m2, _ := m.killSelectedWorkflow()
	if !strings.Contains(m2.flashMsg, "no workflow selected") {
		t.Errorf("flash = %q", m2.flashMsg)
	}
}

func TestTakeUserContent_UltracodeReminder(t *testing.T) {
	m := idleModel()
	_, content, _ := m.takeUserContent("ultracode audit everything")
	if last := content[len(content)-1]; last.Text != commands.UltracodeReminder {
		t.Errorf("keyword reminder missing; last block = %+v", last)
	}

	_, content, _ = m.takeUserContent("plain message")
	if last := content[len(content)-1]; last.Text != "plain message" {
		t.Errorf("unexpected reminder without opt-in: %+v", last)
	}

	workflow.Ultracode.Store(true)
	t.Cleanup(func() { workflow.Ultracode.Store(false) })
	_, content, _ = m.takeUserContent("plain message")
	if last := content[len(content)-1]; last.Text != commands.UltracodeSessionReminder {
		t.Errorf("session reminder missing; last block = %+v", last)
	}
}
