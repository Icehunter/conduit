package workflowtool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/workflow"
)

type fakeHost struct {
	mu    sync.Mutex
	calls []workflow.AgentRequest
	block chan struct{} // when non-nil, agents wait on it
}

func (h *fakeHost) RunAgent(ctx context.Context, req workflow.AgentRequest) workflow.AgentResult {
	h.mu.Lock()
	h.calls = append(h.calls, req)
	h.mu.Unlock()
	if h.block != nil {
		select {
		case <-h.block:
		case <-ctx.Done():
			return workflow.AgentResult{Err: ctx.Err()}
		}
	}
	return workflow.AgentResult{Value: "r:" + req.Prompt, OutputTokens: 2}
}

func (h *fakeHost) ResolveWorkflow(context.Context, workflow.WorkflowRef) (string, error) {
	return "", errors.New("none")
}

func (h *fakeHost) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

type notifier struct {
	ch chan string
}

func (n *notifier) notify(text string) { n.ch <- text }

const script = `export const meta = { name: 'demo', description: 'd', phases: [{ title: 'A' }] }
phase('A')
log('starting')
const r = await parallel([() => agent('one'), () => agent('two')])
return { r, n: args ? args.n : null }
`

var runIDRE = regexp.MustCompile(`runId: (\S+)`)

func newTool(t *testing.T, host workflow.Host) (*Tool, *notifier) {
	t.Helper()
	n := &notifier{ch: make(chan string, 4)}
	reg := workflow.NewRegistry(10)
	tt := New(Config{Host: host, Registry: reg, Notify: n.notify, RunsDir: t.TempDir(), Cwd: func() string { return t.TempDir() }})
	return tt, n
}

func exec(t *testing.T, tt *Tool, in map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(in)
	res, err := tt.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", res.Content[0].Text)
	}
	return res.Content[0].Text
}

func execErr(t *testing.T, tt *Tool, in map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(in)
	res, err := tt.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error, got: %s", res.Content[0].Text)
	}
	return res.Content[0].Text
}

func waitNotify(t *testing.T, n *notifier) string {
	t.Helper()
	select {
	case s := <-n.ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no notification")
		return ""
	}
}

func TestExecute_BackgroundRunAndNotification(t *testing.T) {
	host := &fakeHost{}
	tt, n := newTool(t, host)
	out := exec(t, tt, map[string]any{"script": script, "args": map[string]any{"n": 5}})
	if !strings.Contains(out, "started in the background") {
		t.Fatalf("out = %s", out)
	}
	m := runIDRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no runId in %s", out)
	}
	runID := m[1]

	note := waitNotify(t, n)
	// The result body is XML-escaped inside <result>, as CC's notification is.
	for _, want := range []string{"<task-notification>", "<task-id>" + runID + "</task-id>", "<status>completed</status>", `&quot;r&quot;:[&quot;r:one&quot;,&quot;r:two&quot;]`, `&quot;n&quot;:5`, "starting", "journal:"} {
		if !strings.Contains(note, want) {
			t.Errorf("notification missing %q:\n%s", want, note)
		}
	}
	if host.count() != 2 {
		t.Errorf("host called %d times", host.count())
	}

	// The inline script was persisted next to the journal.
	runDir := filepath.Join(tt.runsDir, runID)
	if _, err := os.Stat(filepath.Join(runDir, "script.js")); err != nil {
		t.Errorf("script not persisted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "journal.jsonl")); err != nil {
		t.Errorf("journal not written: %v", err)
	}

	// result op returns the JSON; status op shows the phase.
	res := exec(t, tt, map[string]any{"op": "result", "runId": runID})
	if !strings.Contains(res, `"r":["r:one","r:two"]`) {
		t.Errorf("result op = %s", res)
	}
	st := exec(t, tt, map[string]any{"op": "status", "runId": runID})
	if !strings.Contains(st, "done") || !strings.Contains(st, "A: 2 agents") {
		t.Errorf("status op = %s", st)
	}
}

func TestExecute_Validation(t *testing.T) {
	tt, _ := newTool(t, &fakeHost{})
	tests := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"nothing", map[string]any{}, "exactly one of"},
		{"two sources", map[string]any{"script": script, "name": "x"}, "exactly one of"},
		{"no meta", map[string]any{"script": "return 1"}, "must begin with"},
		{"nondeterministic", map[string]any{"script": "export const meta = {name:'a',description:'b'}\nreturn Date.now()"}, "deterministic"},
		{"stringified args", map[string]any{"script": script, "args": `["a.ts"]`}, "JSON-encoded string"},
		{"unknown saved", map[string]any{"name": "nope"}, "no saved workflow"},
		{"missing scriptPath", map[string]any{"scriptPath": "/nonexistent/x.js"}, "cannot read scriptPath"},
		{"op without runId", map[string]any{"op": "status"}, "requires runId"},
		{"op unknown run", map[string]any{"op": "status", "runId": "zzz"}, "unknown runId"},
		{"bad resume", map[string]any{"script": script, "resumeFromRunId": "missing"}, "cannot resume"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := execErr(t, tt, tc.in)
			if !strings.Contains(got, tc.want) {
				t.Errorf("error = %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestExecute_KillOp(t *testing.T) {
	host := &fakeHost{block: make(chan struct{})}
	tt, n := newTool(t, host)
	out := exec(t, tt, map[string]any{"script": script})
	runID := runIDRE.FindStringSubmatch(out)[1]

	deadline := time.After(5 * time.Second)
	for host.count() < 2 {
		select {
		case <-deadline:
			t.Fatal("agents never started")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	st := exec(t, tt, map[string]any{"op": "status", "runId": runID})
	if !strings.Contains(st, "running") {
		t.Errorf("status = %s", st)
	}
	if r := exec(t, tt, map[string]any{"op": "result", "runId": runID}); !strings.Contains(r, "still running") {
		t.Errorf("result while running = %s", r)
	}
	exec(t, tt, map[string]any{"op": "kill", "runId": runID})
	note := waitNotify(t, n)
	if !strings.Contains(note, "<status>killed</status>") {
		t.Errorf("notification = %s", note)
	}
	if again := exec(t, tt, map[string]any{"op": "kill", "runId": runID}); !strings.Contains(again, "already killed") {
		t.Errorf("second kill = %s", again)
	}
}

func TestExecute_ScriptFailureNotifies(t *testing.T) {
	tt, n := newTool(t, &fakeHost{})
	exec(t, tt, map[string]any{"script": "export const meta = {name:'a',description:'b'}\nthrow new Error('kaboom')"})
	note := waitNotify(t, n)
	if !strings.Contains(note, "<status>failed</status>") || !strings.Contains(note, "kaboom") {
		t.Errorf("notification = %s", note)
	}
}

func TestExecute_ResumeReplaysJournal(t *testing.T) {
	host := &fakeHost{}
	tt, n := newTool(t, host)
	out := exec(t, tt, map[string]any{"script": script})
	first := runIDRE.FindStringSubmatch(out)[1]
	waitNotify(t, n)
	scriptPath := filepath.Join(tt.runsDir, first, "script.js")

	out2 := exec(t, tt, map[string]any{"scriptPath": scriptPath, "resumeFromRunId": first})
	second := runIDRE.FindStringSubmatch(out2)[1]
	note := waitNotify(t, n)
	if !strings.Contains(note, "<status>completed</status>") {
		t.Fatalf("resume notification = %s", note)
	}
	if host.count() != 2 {
		t.Errorf("resume re-ran agents: host called %d times total, want 2", host.count())
	}
	st := exec(t, tt, map[string]any{"op": "status", "runId": second})
	if !strings.Contains(st, "2 cached") {
		t.Errorf("status = %s", st)
	}
}

func TestExecute_SavedWorkflowByName(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".claude", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo.js"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	n := &notifier{ch: make(chan string, 4)}
	tt := New(Config{Host: &fakeHost{}, Registry: workflow.NewRegistry(10), Notify: n.notify, RunsDir: t.TempDir(), Cwd: func() string { return cwd }})
	out := exec(t, tt, map[string]any{"name": "demo"})
	if !strings.Contains(out, "scriptPath: "+filepath.Join(dir, "demo.js")) {
		t.Errorf("saved workflow should run from its own path: %s", out)
	}
	waitNotify(t, n)
}

func TestDescription_UltracodeGate(t *testing.T) {
	off := New(Config{Host: &fakeHost{}}).Description()
	on := New(Config{Host: &fakeHost{}, Ultracode: func() bool { return true }}).Description()
	if !strings.Contains(off, "ONLY call this tool") || strings.Contains(off, "ULTRACODE IS ON") {
		t.Error("gate text missing when ultracode is off")
	}
	if !strings.Contains(on, "ULTRACODE IS ON") || strings.Contains(on, "ONLY call this tool") {
		t.Error("standing opt-in text missing when ultracode is on")
	}
	for _, api := range []string{"agent(prompt", "pipeline(items", "parallel(thunks", "budget:", "workflow(nameOrRef"} {
		if !strings.Contains(on, api) {
			t.Errorf("description missing %q", api)
		}
	}
}

func TestIsReadOnly(t *testing.T) {
	tt := &Tool{}
	if !tt.IsReadOnly(json.RawMessage(`{"op":"status","runId":"x"}`)) {
		t.Error("status should be read-only")
	}
	if tt.IsReadOnly(json.RawMessage(`{"op":"kill","runId":"x"}`)) || tt.IsReadOnly(json.RawMessage(`{"script":"x"}`)) {
		t.Error("kill/start must not be read-only")
	}
}
