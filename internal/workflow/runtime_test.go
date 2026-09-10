package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeHost struct {
	mu        sync.Mutex
	calls     []AgentRequest
	fn        func(ctx context.Context, req AgentRequest) AgentResult
	workflows map[string]string
}

func (h *fakeHost) RunAgent(ctx context.Context, req AgentRequest) AgentResult {
	h.mu.Lock()
	h.calls = append(h.calls, req)
	h.mu.Unlock()
	if h.fn == nil {
		return AgentResult{Value: "ok:" + req.Prompt, OutputTokens: 1}
	}
	return h.fn(ctx, req)
}

func (h *fakeHost) ResolveWorkflow(_ context.Context, ref WorkflowRef) (string, error) {
	if src, ok := h.workflows[ref.Name]; ok {
		return src, nil
	}
	if src, ok := h.workflows[ref.ScriptPath]; ok {
		return src, nil
	}
	return "", fmt.Errorf("unknown workflow %+v", ref)
}

func (h *fakeHost) prompts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.calls))
	for i, c := range h.calls {
		out[i] = c.Prompt
	}
	return out
}

func mustLoad(t *testing.T, body string) *Script {
	t.Helper()
	s, err := Load("export const meta = { name: 't', description: 'd', phases: [{ title: 'P1' }] }\n" + body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func run(t *testing.T, body string, opts Options) (json.RawMessage, *Run, error) {
	t.Helper()
	r := &Run{ID: "run-1"}
	out, err := Execute(context.Background(), r, mustLoad(t, body), opts)
	return out, r, err
}

func TestExecute_ResultAndArgs(t *testing.T) {
	tests := []struct {
		name string
		body string
		args string
		want string
	}{
		{"returns object", "return { a: 1, b: [true, null, 'x'] }", "", `{"a":1,"b":[true,null,"x"]}`},
		{"returns primitive", "return 7", "", `7`},
		{"no return", "const x = 1", "", `null`},
		{"args object", "return args.files.length + args.cap", `{"files":["a","b"],"cap":10}`, `12`},
		{"args string", "return typeof args + ':' + args", `"hello"`, `"string:hello"`},
		{"args undefined", "return typeof args", "", `"undefined"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var args json.RawMessage
			if tt.args != "" {
				args = json.RawMessage(tt.args)
			}
			out, r, err := run(t, tt.body, Options{Host: &fakeHost{}, Args: args})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if string(out) != tt.want {
				t.Errorf("result = %s, want %s", out, tt.want)
			}
			if r.Status() != RunDone {
				t.Errorf("status = %s", r.Status())
			}
		})
	}
}

func TestExecute_ResultNotSerializable(t *testing.T) {
	_, r, err := run(t, "return () => 1", Options{Host: &fakeHost{}})
	if err == nil || !strings.Contains(err.Error(), "function") {
		t.Fatalf("err = %v, want function rejection", err)
	}
	if r.Status() != RunFailed {
		t.Errorf("status = %s", r.Status())
	}
}

func TestExecute_ScriptThrows(t *testing.T) {
	_, r, err := run(t, "throw new Error('boom')", Options{Host: &fakeHost{}})
	if !errors.Is(err, ErrScript) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if s := r.Snapshot(); s.Status != RunFailed || !strings.Contains(s.Err, "boom") {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestAgent_TextAndSchema(t *testing.T) {
	host := &fakeHost{fn: func(_ context.Context, req AgentRequest) AgentResult {
		if req.Schema != nil {
			return AgentResult{Value: map[string]any{"holds": true, "reason": req.Prompt}, InputTokens: 5, OutputTokens: 3}
		}
		return AgentResult{Value: "text for " + req.Prompt, OutputTokens: 2}
	}}
	body := `
phase('P1')
const a = await agent('one', { label: 'first' })
phase('P2')
const b = await agent('two', { phase: 'Explicit', schema: { type: 'object', required: ['holds'], properties: { holds: { type: 'boolean' }, reason: { type: 'string' } } } })
const c = await agent('three')
return { a, b, c }
`
	out, r, err := run(t, body, Options{Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":"text for one","b":{"holds":true,"reason":"two"},"c":"text for three"}` {
		t.Errorf("result = %s", out)
	}
	if len(host.calls) != 3 {
		t.Fatalf("calls = %d", len(host.calls))
	}
	if host.calls[0].Label != "first" || host.calls[0].Phase != "P1" || host.calls[0].Schema != nil {
		t.Errorf("call 0 = %+v", host.calls[0])
	}
	if host.calls[1].Phase != "Explicit" || host.calls[1].Schema == nil {
		t.Errorf("call 1 = %+v", host.calls[1])
	}
	if host.calls[2].Phase != "P2" || host.calls[2].Label != "three" {
		t.Errorf("call 2 = %+v", host.calls[2])
	}
	s := r.Snapshot()
	if len(s.Nodes) != 3 || s.Nodes[1].Status != NodeDone || s.Nodes[1].OutputTokens != 3 {
		t.Errorf("nodes = %+v", s.Nodes)
	}
	if s.OutputTokens != 7 || s.InputTokens != 5 {
		t.Errorf("tokens in=%d out=%d", s.InputTokens, s.OutputTokens)
	}
	titles := make([]string, len(s.Phases))
	for i, p := range s.Phases {
		titles[i] = p.Title
	}
	if strings.Join(titles, ",") != "P1,P2,Explicit" {
		t.Errorf("phases = %v", titles)
	}
}

func TestAgent_BadSchemaThrowsSynchronously(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{"root not object", `{ type: 'array' }`, "schema root"},
		{"required not in properties", `{ type: 'object', properties: { a: {} }, required: ['b'] }`, "does not declare"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := &fakeHost{}
			body := "try { agent('x', { schema: " + tt.schema + " }) } catch (e) { return 'threw: ' + e.message }\nreturn 'no throw'"
			out, _, err := run(t, body, Options{Host: host})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), "threw") || !strings.Contains(string(out), tt.want) {
				t.Errorf("result = %s", out)
			}
			if len(host.calls) != 0 {
				t.Error("agent was spawned despite bad schema")
			}
		})
	}
}

func TestAgent_ErrorResolvesNull(t *testing.T) {
	host := &fakeHost{fn: func(_ context.Context, req AgentRequest) AgentResult {
		if req.Prompt == "bad" {
			return AgentResult{Err: errors.New("terminal API error")}
		}
		return AgentResult{Value: "fine"}
	}}
	out, r, err := run(t, "const r = await parallel([() => agent('bad'), () => agent('good')])\nreturn r", Options{Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `[null,"fine"]` {
		t.Errorf("result = %s", out)
	}
	s := r.Snapshot()
	var failed, done int
	for _, n := range s.Nodes {
		switch n.Status {
		case NodeFailed:
			failed++
		case NodeDone:
			done++
		}
	}
	if failed != 1 || done != 1 {
		t.Errorf("nodes = %+v", s.Nodes)
	}
	if len(s.Logs) == 0 || !strings.Contains(s.Logs[0].Text, "terminal API error") {
		t.Errorf("logs = %+v", s.Logs)
	}
}

func TestParallel(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
		opts Options
	}{
		{
			name: "barrier collects all in order",
			body: "return await parallel([() => agent('a'), () => agent('b'), () => agent('c')])",
			want: `["ok:a","ok:b","ok:c"]`,
		},
		{
			name: "thrown thunk becomes null, others survive",
			body: "return await parallel([() => { throw new Error('x') }, () => agent('b'), async () => { throw new Error('y') }])",
			want: `[null,"ok:b",null]`,
		},
		{
			name: "non-array throws",
			body: "try { await parallel('nope') } catch (e) { return e.name }",
			want: `"TypeError"`,
		},
		{
			name: "non-function element throws",
			body: "try { await parallel([1]) } catch (e) { return e.name }",
			want: `"TypeError"`,
		},
		{
			name: "over cap throws",
			body: "try { await parallel(Array.from({length: 4}, () => () => 1)) } catch (e) { return e.name }",
			want: `"RangeError"`,
			opts: Options{MaxItems: 3},
		},
		{
			name: "empty array",
			body: "return await parallel([])",
			want: `[]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			opts.Host = &fakeHost{}
			out, _, err := run(t, tt.body, opts)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tt.want {
				t.Errorf("result = %s, want %s", out, tt.want)
			}
		})
	}
}

func TestPipeline(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "stages receive prev, item, index",
			body: `return await pipeline(['x', 'y'],
				(item) => agent(item),
				(prev, item, index) => prev + '|' + item + '|' + index)`,
			want: `["ok:x|x|0","ok:y|y|1"]`,
		},
		{
			name: "throw short-circuits that item only",
			body: `return await pipeline([1, 2, 3],
				(n) => { if (n === 2) throw new Error('skip'); return n * 10 },
				(prev) => prev + 1)`,
			want: `[11,null,31]`,
		},
		{
			name: "null short-circuits remaining stages",
			body: `let stage2 = 0
				const r = await pipeline([1, 2],
					(n) => n === 1 ? null : n,
					(prev) => { stage2++; return prev })
				return { r, stage2 }`,
			want: `{"r":[null,2],"stage2":1}`,
		},
		{
			name: "undefined passes through",
			body: `return await pipeline([1], () => undefined, (prev) => prev === undefined ? 'u' : 'x')`,
			want: `["u"]`,
		},
		{
			name: "no stages throws",
			body: "try { await pipeline([1]) } catch (e) { return e.name }",
			want: `"TypeError"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := run(t, tt.body, Options{Host: &fakeHost{}})
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tt.want {
				t.Errorf("result = %s, want %s", out, tt.want)
			}
		})
	}
}

// TestPipeline_NoBarrier proves item B reaches stage 2 while item A is still
// blocked in stage 1: the host holds "A1" until it has seen "B2".
func TestPipeline_NoBarrier(t *testing.T) {
	sawB2 := make(chan struct{})
	var once sync.Once
	host := &fakeHost{fn: func(ctx context.Context, req AgentRequest) AgentResult {
		switch req.Prompt {
		case "A1":
			select {
			case <-sawB2:
			case <-ctx.Done():
				return AgentResult{Err: ctx.Err()}
			case <-time.After(5 * time.Second):
				return AgentResult{Err: errors.New("barrier: B2 never ran while A1 was pending")}
			}
		case "B2":
			once.Do(func() { close(sawB2) })
		}
		return AgentResult{Value: req.Prompt}
	}}
	body := `return await pipeline(['A', 'B'],
		(item) => agent(item + '1'),
		(prev, item) => agent(item + '2'))`
	out, _, err := run(t, body, Options{Host: host, MaxConcurrent: 4})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `["A2","B2"]` {
		t.Errorf("result = %s", out)
	}
}

func TestConcurrencyCap(t *testing.T) {
	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	host := &fakeHost{fn: func(ctx context.Context, req AgentRequest) AgentResult {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		inFlight.Add(-1)
		return AgentResult{Value: req.Prompt}
	}}
	done := make(chan struct{})
	var out json.RawMessage
	var err error
	go func() {
		defer close(done)
		out, _, err = run(t, "return await parallel(Array.from({length: 6}, (_, i) => () => agent('p' + i)))", Options{Host: host, MaxConcurrent: 2})
	}()
	// Wait until the cap is saturated, then let everyone through.
	deadline := time.After(5 * time.Second)
	for inFlight.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("cap never reached 2 in flight")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Errorf("peak in-flight = %d, want 2", peak.Load())
	}
	if !strings.HasPrefix(string(out), `["p0","p1"`) {
		t.Errorf("result = %s", out)
	}
}

func TestLifetimeCap(t *testing.T) {
	host := &fakeHost{}
	body := `await agent('1'); await agent('2')
try { await agent('3') } catch (e) { return 'capped: ' + e.message }
return 'not capped'`
	out, _, err := run(t, body, Options{Host: host, MaxAgents: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "capped") || !strings.Contains(string(out), "2 agents") {
		t.Errorf("result = %s", out)
	}
	if len(host.calls) != 2 {
		t.Errorf("host called %d times", len(host.calls))
	}
}

func TestBudget(t *testing.T) {
	host := &fakeHost{fn: func(_ context.Context, req AgentRequest) AgentResult {
		return AgentResult{Value: req.Prompt, OutputTokens: 60}
	}}
	body := `const before = { total: budget.total, spent: budget.spent(), remaining: budget.remaining() }
await agent('a')
const mid = { spent: budget.spent(), remaining: budget.remaining() }
await agent('b')
let threw = null
try { await agent('c') } catch (e) { threw = e.message }
return { before, mid, after: { spent: budget.spent(), remaining: budget.remaining() }, threw }`
	out, _, err := run(t, body, Options{Host: host, BudgetTotal: 100})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Before, Mid, After struct {
			Total     *int    `json:"total"`
			Spent     int     `json:"spent"`
			Remaining float64 `json:"remaining"`
		}
		Threw string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Before.Total == nil || *got.Before.Total != 100 || got.Before.Spent != 0 || got.Before.Remaining != 100 {
		t.Errorf("before = %+v", got.Before)
	}
	if got.Mid.Spent != 60 || got.Mid.Remaining != 40 {
		t.Errorf("mid = %+v", got.Mid)
	}
	if got.After.Spent != 120 || got.After.Remaining != 0 {
		t.Errorf("after = %+v", got.After)
	}
	if !strings.Contains(got.Threw, "budget exhausted") {
		t.Errorf("threw = %q", got.Threw)
	}
	if len(host.calls) != 2 {
		t.Errorf("host called %d times, want 2", len(host.calls))
	}
}

func TestBudget_NoneIsNullAndInfinity(t *testing.T) {
	out, _, err := run(t, "return { total: budget.total, inf: budget.remaining() === Infinity }", Options{Host: &fakeHost{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"total":null,"inf":true}` {
		t.Errorf("result = %s", out)
	}
}

func TestNondeterminismThrowsAtRuntime(t *testing.T) {
	// Computed access defeats the static scan; the runtime shims must still fire.
	tests := []struct {
		name string
		body string
	}{
		{"Date.now", "try { globalThis['Date']['now']() } catch (e) { return e.message }\nreturn 'no throw'"},
		{"Math.random", "try { Math['random']() } catch (e) { return e.message }\nreturn 'no throw'"},
		{"new Date()", "const D = globalThis['Date']\ntry { new D() } catch (e) { return e.message }\nreturn 'no throw'"},
		{"Date() as function", "const D = globalThis['Date']\ntry { D() } catch (e) { return e.message }\nreturn 'no throw'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := run(t, tt.body, Options{Host: &fakeHost{}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), "unavailable") {
				t.Errorf("result = %s", out)
			}
		})
	}
	t.Run("new Date(arg) still works", func(t *testing.T) {
		out, _, err := run(t, "return new Date(0).toISOString()", Options{Host: &fakeHost{}})
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != `"1970-01-01T00:00:00.000Z"` {
			t.Errorf("result = %s", out)
		}
	})
}

func TestKill(t *testing.T) {
	started := make(chan struct{})
	host := &fakeHost{fn: func(ctx context.Context, req AgentRequest) AgentResult {
		close(started)
		<-ctx.Done()
		return AgentResult{Err: ctx.Err()}
	}}
	r := &Run{ID: "run-kill"}
	done := make(chan error, 1)
	go func() {
		_, err := Execute(context.Background(), r, mustLoad(t, "return await agent('hang')"), Options{Host: host})
		done <- err
	}()
	<-started
	r.Kill()
	select {
	case err := <-done:
		if !errors.Is(err, ErrAborted) {
			t.Fatalf("err = %v, want ErrAborted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after Kill")
	}
	if r.Status() != RunKilled {
		t.Errorf("status = %s", r.Status())
	}
}

func TestKill_InterruptsTightLoop(t *testing.T) {
	r := &Run{ID: "run-spin"}
	done := make(chan error, 1)
	go func() {
		_, err := Execute(context.Background(), r, mustLoad(t, "for (;;) {}"), Options{Host: &fakeHost{}})
		done <- err
	}()
	// Give the VM a moment to enter the loop, then kill.
	time.Sleep(20 * time.Millisecond)
	r.Kill()
	select {
	case err := <-done:
		if !errors.Is(err, ErrAborted) {
			t.Fatalf("err = %v, want ErrAborted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tight loop was not interrupted")
	}
}

func TestChildWorkflow(t *testing.T) {
	host := &fakeHost{workflows: map[string]string{
		"child": `export const meta = { name: 'child', description: 'c', phases: [{ title: 'Inner' }] }
phase('Inner')
log('inside child')
const r = await agent('child sees ' + args.n)
return { got: r }`,
		"grandchild-caller": `export const meta = { name: 'gc', description: 'g' }
return await workflow('child', { n: 1 })`,
		"/tmp/by-path.js": `export const meta = { name: 'bypath', description: 'p' }
return 'from path'`,
	}}
	body := `const a = await workflow('child', { n: 7 })
const b = await workflow({ scriptPath: '/tmp/by-path.js' })
let nested = null
try { await workflow('grandchild-caller') } catch (e) { nested = e.message }
let missing = null
try { await workflow('nope') } catch (e) { missing = e.message }
return { a, b, nested, missing }`
	out, r, err := run(t, body, Options{Host: host})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		A       map[string]string
		B       string
		Nested  string
		Missing string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err, string(out))
	}
	if got.A["got"] != "ok:child sees 7" || got.B != "from path" {
		t.Errorf("a=%v b=%q", got.A, got.B)
	}
	if !strings.Contains(got.Nested, "one level") {
		t.Errorf("nested = %q", got.Nested)
	}
	if !strings.Contains(got.Missing, "unknown workflow") {
		t.Errorf("missing = %q", got.Missing)
	}
	s := r.Snapshot()
	if len(s.Nodes) != 1 || s.Nodes[0].Child != "child" || s.Nodes[0].Phase != "↳ child: Inner" {
		t.Errorf("nodes = %+v", s.Nodes)
	}
	var sawLog bool
	for _, l := range s.Logs {
		if l.Text == "↳ child: inside child" {
			sawLog = true
		}
	}
	if !sawLog {
		t.Errorf("logs = %+v", s.Logs)
	}
}

func TestJournalReplay(t *testing.T) {
	dir := t.TempDir()
	host := &fakeHost{fn: func(_ context.Context, req AgentRequest) AgentResult {
		return AgentResult{Value: map[string]any{"p": req.Prompt}, OutputTokens: 1}
	}}
	body := `const a = await agent('same', { schema: { type: 'object', properties: {} } })
const b = await agent('same', { schema: { type: 'object', properties: {} } })
const c = await agent('other')
log('done')
return [a, b, c]`

	j1, err := OpenJournal(dir+"/r1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	out1, _, err := run(t, body, Options{Host: host, Journal: j1})
	if err != nil {
		t.Fatal(err)
	}
	if len(host.calls) != 3 {
		t.Fatalf("first run made %d calls", len(host.calls))
	}

	entries, err := ReadJournal(j1.Path)
	if err != nil {
		t.Fatal(err)
	}
	var results, logs, finals int
	for _, e := range entries {
		switch e.Type {
		case "result":
			results++
			if e.Cached {
				t.Error("first run recorded a cached hit")
			}
		case "log":
			logs++
		case "final":
			finals++
			if string(e.Result) != string(out1) {
				t.Errorf("final result = %s", e.Result)
			}
		}
	}
	if results != 3 || logs != 1 || finals != 1 {
		t.Errorf("journal: results=%d logs=%d finals=%d", results, logs, finals)
	}

	// Second run: everything replays, host is never called.
	host.calls = nil
	j2, err := OpenJournal(dir+"/r2", "r2")
	if err != nil {
		t.Fatal(err)
	}
	if err := j2.LoadPrior(j1.Path, "r1"); err != nil {
		t.Fatal(err)
	}
	out2, r2, err := run(t, body, Options{Host: host, Journal: j2})
	if err != nil {
		t.Fatal(err)
	}
	if string(out2) != string(out1) {
		t.Errorf("replayed result = %s, want %s", out2, out1)
	}
	if len(host.calls) != 0 {
		t.Errorf("replay called host %d times", len(host.calls))
	}
	for _, n := range r2.Snapshot().Nodes {
		if n.Status != NodeCached {
			t.Errorf("node %d status = %s", n.Seq, n.Status)
		}
	}

	// Third run with an edited prompt: the changed call runs live, the
	// unchanged ones still replay.
	host.calls = nil
	j3, err := OpenJournal(dir+"/r3", "r3")
	if err != nil {
		t.Fatal(err)
	}
	if err := j3.LoadPrior(j2.Path, "r2"); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(body, "'other'", "'changed'", 1)
	_, _, err = run(t, edited, Options{Host: host, Journal: j3})
	if err != nil {
		t.Fatal(err)
	}
	if got := host.prompts(); len(got) != 1 || got[0] != "changed" {
		t.Errorf("edited run called host with %v, want [changed]", got)
	}
}

func TestJournal_KeyIgnoresScriptText(t *testing.T) {
	// Same prompt in an edited script → still replays: editing and resuming
	// is the whole point of the journal.
	dir := t.TempDir()
	host := &fakeHost{}
	j1, _ := OpenJournal(dir+"/a", "a")
	if _, _, err := run(t, "return await agent('p')", Options{Host: host, Journal: j1}); err != nil {
		t.Fatal(err)
	}
	host.calls = nil
	j2, _ := OpenJournal(dir+"/b", "b")
	if err := j2.LoadPrior(j1.Path, "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "const x = 1\nreturn await agent('p')", Options{Host: host, Journal: j2}); err != nil {
		t.Fatal(err)
	}
	if len(host.calls) != 0 {
		t.Errorf("edited script did not replay an identical call")
	}
}

func TestJournal_OptsChangeMisses(t *testing.T) {
	dir := t.TempDir()
	host := &fakeHost{}
	j1, _ := OpenJournal(dir+"/a", "a")
	if _, _, err := run(t, "return await agent('p', { label: 'x' })", Options{Host: host, Journal: j1}); err != nil {
		t.Fatal(err)
	}
	host.calls = nil
	j2, _ := OpenJournal(dir+"/b", "b")
	if err := j2.LoadPrior(j1.Path, "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "return await agent('p', { label: 'x', model: 'other' })", Options{Host: host, Journal: j2}); err != nil {
		t.Fatal(err)
	}
	if len(host.calls) != 1 {
		t.Errorf("changed opts replayed a stale result")
	}
}

func TestCanonicalJSON(t *testing.T) {
	a := canonicalJSON(json.RawMessage(`{"b":1,"a":{"d":2,"c":[3,{"z":1,"y":2}]}}`))
	b := canonicalJSON(json.RawMessage(`{"a":{"c":[3,{"y":2,"z":1}],"d":2},"b":1}`))
	if string(a) != string(b) {
		t.Errorf("%s != %s", a, b)
	}
	if canonicalJSON(nil) != nil {
		t.Error("nil should stay nil")
	}
}
