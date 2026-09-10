package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// AgentRequest is one agent() call handed to the Host.
type AgentRequest struct {
	Seq       int
	Prompt    string
	Label     string
	Phase     string
	Model     string
	Effort    string
	Isolation string // "" or "worktree"
	AgentType string
	Schema    json.RawMessage // nil when the script asked for plain text
}

// AgentResult is what the Host returns for one agent() call.
type AgentResult struct {
	// Value is the decoded JSON object when a schema was supplied, otherwise
	// the final text as a string. Ignored when Err is set.
	Value        any
	InputTokens  int
	OutputTokens int
	// Note, when non-empty, is surfaced as a narrator line (e.g. the path
	// of a worktree that was kept because the agent changed files).
	Note string
	// Err marks a terminal failure; the script receives null.
	Err error
}

// Host runs sub-agents and resolves child workflows on behalf of a script.
type Host interface {
	RunAgent(ctx context.Context, req AgentRequest) AgentResult
	// ResolveWorkflow returns the source of a saved workflow by name, or of
	// a script file by path when ref is {scriptPath}.
	ResolveWorkflow(ctx context.Context, ref WorkflowRef) (src string, err error)
}

// WorkflowRef identifies a child workflow: exactly one of Name/ScriptPath.
type WorkflowRef struct {
	Name       string
	ScriptPath string
}

// Options configures one Execute call.
type Options struct {
	Host Host
	// Args is the JSON value exposed as the `args` global; nil → undefined.
	Args json.RawMessage
	// MaxConcurrent bounds in-flight agents; 0 → min(16, max(2, NumCPU-2)).
	MaxConcurrent int
	// MaxAgents bounds agent() calls per run lifetime; 0 → 1000.
	MaxAgents int
	// MaxItems bounds one parallel()/pipeline() call; 0 → 4096.
	MaxItems int
	// BudgetTotal is the output-token ceiling; 0 → no budget.
	BudgetTotal int
	// Journal, when set, replays cached agent results and records new ones.
	Journal *Journal
}

const (
	defaultMaxAgents = 1000
	defaultMaxItems  = 4096
)

// DefaultMaxConcurrent mirrors CC: min(16, max(2, CPUs-2)).
func DefaultMaxConcurrent() int {
	return min(16, max(2, runtime.NumCPU()-2))
}

var (
	// ErrAborted is returned when the run context is cancelled.
	ErrAborted = errors.New("workflow: aborted")
	// ErrScript wraps an uncaught exception from the script.
	ErrScript = errors.New("workflow: script threw")
)

type engine struct {
	ctx   context.Context
	run   *Run
	opts  Options
	sem   chan struct{}
	count atomic.Int64
	// occurrences counts journal-key repeats so identical prompts map to
	// distinct cached results.
	occurrences map[string]int
}

// Execute runs script under run and returns the JSON-encoded result. The
// run's status, nodes, logs and result are updated as it goes; callers
// observe them via Run.Snapshot. Execute blocks until the script settles or
// ctx is cancelled.
func Execute(ctx context.Context, run *Run, script *Script, opts Options) (json.RawMessage, error) {
	if opts.Host == nil {
		return nil, errors.New("workflow: Options.Host is required")
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = DefaultMaxConcurrent()
	}
	if opts.MaxAgents <= 0 {
		opts.MaxAgents = defaultMaxAgents
	}
	if opts.MaxItems <= 0 {
		opts.MaxItems = defaultMaxItems
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	run.mu.Lock()
	run.cancel = cancel
	run.status = RunRunning
	run.budgetTotal = opts.BudgetTotal
	run.phases = append([]Phase(nil), script.Meta.Phases...)
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if run.Name == "" {
		run.Name = script.Meta.Name
	}
	if run.ScriptHash == "" {
		run.ScriptHash = script.Hash
	}
	run.mu.Unlock()

	e := &engine{
		ctx:         ctx,
		run:         run,
		opts:        opts,
		sem:         make(chan struct{}, opts.MaxConcurrent),
		occurrences: map[string]int{},
	}
	result, err := e.execScript(script, opts.Args, "", 0)
	if opts.Journal != nil {
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		opts.Journal.Final(result, errText)
	}
	run.finish(result, err)
	return result, err
}

// prelude removes the nondeterministic clock/RNG surface and defines the
// pure-JS combinators. parallel/pipeline live in JS so their interleaving
// follows ordinary promise semantics on the single VM thread.
const prelude = `(function () {
  // globalThis.Date, not Date: the function declaration below hoists over
  // this whole scope, so a bare Date here would capture the shim itself.
  const RealDate = globalThis.Date;
  const unavailable = (what) => { throw new Error(what + ' is unavailable in workflow scripts (it would break resume); pass timestamps via args or stamp results after the workflow returns') };
  function Date(...a) {
    if (!(this instanceof Date)) unavailable('Date()');
    if (a.length === 0) unavailable('new Date()');
    return new RealDate(...a);
  }
  Date.prototype = RealDate.prototype;
  Date.parse = RealDate.parse;
  Date.UTC = RealDate.UTC;
  Date.now = () => unavailable('Date.now()');
  globalThis.Date = Date;
  Math.random = () => unavailable('Math.random()');

  const checkItems = (name, items) => {
    if (!Array.isArray(items)) throw new TypeError(name + '() expects an array');
    if (items.length > __maxItems) throw new RangeError(name + '() accepts at most ' + __maxItems + ' items; got ' + items.length);
  };
  globalThis.parallel = (thunks) => {
    checkItems('parallel', thunks);
    for (const t of thunks) if (typeof t !== 'function') throw new TypeError('parallel() expects an array of functions');
    return Promise.all(thunks.map(async (t) => { try { return await t() } catch (e) { __swallowed(e); return null } }));
  };
  globalThis.pipeline = (items, ...stages) => {
    checkItems('pipeline', items);
    if (stages.length === 0) throw new TypeError('pipeline() needs at least one stage');
    for (const s of stages) if (typeof s !== 'function') throw new TypeError('pipeline() stages must be functions');
    return Promise.all(items.map(async (item, index) => {
      let prev = item;
      for (const stage of stages) {
        try { prev = await stage(prev, item, index) } catch (e) { __swallowed(e); return null }
        if (prev === null) return null;
      }
      return prev;
    }));
  };
})()`

// execScript runs one script in its own VM on the calling goroutine and
// returns its JSON result. childName is "" for the root script.
func (e *engine) execScript(script *Script, args json.RawMessage, childName string, depth int) (json.RawMessage, error) {
	vm := goja.New()
	jobs := make(chan func(), 64)
	done := make(chan struct{})
	defer close(done)

	post := func(fn func()) {
		select {
		case jobs <- fn:
		case <-done:
		}
	}

	parse, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
	stringify, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	fromJSON := func(raw json.RawMessage) goja.Value {
		if len(raw) == 0 {
			return goja.Undefined()
		}
		v, err := parse(goja.Undefined(), vm.ToValue(string(raw)))
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return v
	}
	toJSON := func(v goja.Value) json.RawMessage {
		if v == nil || goja.IsUndefined(v) {
			return nil
		}
		out, err := stringify(goja.Undefined(), v)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		if goja.IsUndefined(out) {
			return nil
		}
		return json.RawMessage(out.String())
	}

	phasePrefix := ""
	if childName != "" {
		phasePrefix = "↳ " + childName + ": "
	}

	throwErr := func(err error) {
		panic(vm.NewGoError(err))
	}

	_ = vm.Set("__maxItems", e.opts.MaxItems)
	_ = vm.Set("__swallowed", func(call goja.FunctionCall) goja.Value {
		// A thunk/stage that threw is reported as a narrator line so a
		// swallowed failure is visible somewhere, then becomes null.
		if v := call.Argument(0); !goja.IsUndefined(v) && !goja.IsNull(v) {
			e.run.log("node error: " + errString(v))
		}
		return goja.Undefined()
	})
	_ = vm.Set("args", fromJSON(args))
	_ = vm.Set("phase", func(call goja.FunctionCall) goja.Value {
		e.run.setPhase(phasePrefix + call.Argument(0).String())
		return goja.Undefined()
	})
	_ = vm.Set("log", func(call goja.FunctionCall) goja.Value {
		e.run.log(phasePrefix + call.Argument(0).String())
		if e.opts.Journal != nil {
			e.opts.Journal.Log(phasePrefix + call.Argument(0).String())
		}
		return goja.Undefined()
	})

	budget := vm.NewObject()
	total, _ := e.run.budget()
	if total > 0 {
		_ = budget.Set("total", total)
	} else {
		_ = budget.Set("total", goja.Null())
	}
	_ = budget.Set("spent", func(goja.FunctionCall) goja.Value {
		_, spent := e.run.budget()
		return vm.ToValue(spent)
	})
	_ = budget.Set("remaining", func(goja.FunctionCall) goja.Value {
		total, spent := e.run.budget()
		if total <= 0 {
			return vm.ToValue(math.Inf(1))
		}
		return vm.ToValue(max(0, total-spent))
	})
	_ = vm.Set("budget", budget)

	neverSettles := func() goja.Value {
		p, _, _ := vm.NewPromise()
		return vm.ToValue(p)
	}

	_ = vm.Set("agent", func(call goja.FunctionCall) goja.Value {
		if e.ctx.Err() != nil {
			return neverSettles()
		}
		promptV := call.Argument(0)
		if goja.IsUndefined(promptV) || goja.IsNull(promptV) {
			panic(vm.NewTypeError("agent() expects a prompt string"))
		}
		req := AgentRequest{Prompt: promptV.String()}
		var optsRaw json.RawMessage
		if o := call.Argument(1); !goja.IsUndefined(o) && !goja.IsNull(o) {
			obj := o.ToObject(vm)
			str := func(k string) string {
				v := obj.Get(k)
				if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
					return ""
				}
				return v.String()
			}
			req.Label, req.Phase, req.Model = str("label"), str("phase"), str("model")
			req.Effort, req.Isolation, req.AgentType = str("effort"), str("isolation"), str("agentType")
			if req.Isolation != "" && req.Isolation != "worktree" {
				panic(vm.NewTypeError("agent(): isolation must be 'worktree' or omitted"))
			}
			if sv := obj.Get("schema"); sv != nil && !goja.IsUndefined(sv) && !goja.IsNull(sv) {
				req.Schema = toJSON(sv)
				if err := checkSchema(req.Schema); err != nil {
					throwErr(fmt.Errorf("agent(): %w", err))
				}
			}
			optsRaw = toJSON(o)
		}
		if req.Label == "" {
			req.Label = shortLabel(req.Prompt)
		}
		if req.Phase != "" {
			req.Phase = phasePrefix + req.Phase
			e.run.ensurePhase(req.Phase)
		} else {
			req.Phase = e.run.phaseOr("")
		}
		if req.Phase == "" && phasePrefix != "" {
			req.Phase = strings.TrimSuffix(phasePrefix, ": ")
		}

		if total, spent := e.run.budget(); total > 0 && spent >= total {
			throwErr(fmt.Errorf("agent(): token budget exhausted (%d of %d output tokens spent)", spent, total))
		}
		n := int(e.count.Add(1))
		if n > e.opts.MaxAgents {
			throwErr(fmt.Errorf("agent(): lifetime cap of %d agents reached", e.opts.MaxAgents))
		}
		req.Seq = n

		node := &Node{Seq: n, Label: req.Label, Phase: req.Phase, Status: NodeQueued, StartedAt: time.Now(), Child: childName}
		e.run.addNode(node)

		key := journalKey(req.Prompt, optsRaw)
		occ := e.occurrences[key]
		e.occurrences[key] = occ + 1
		if e.opts.Journal != nil {
			if cached, ok := e.opts.Journal.Replay(key, occ); ok {
				e.run.updateNode(node, func(n *Node) {
					n.Status = NodeCached
					n.DoneAt = time.Now()
				})
				return fromJSON(cached)
			}
		}

		p, resolve, _ := vm.NewPromise()
		go func() {
			select {
			case e.sem <- struct{}{}:
			case <-e.ctx.Done():
				return
			}
			e.run.updateNode(node, func(n *Node) { n.Status = NodeRunning; n.StartedAt = time.Now() })
			res := e.opts.Host.RunAgent(e.ctx, req)
			<-e.sem
			e.run.addSpent(res.OutputTokens)
			var raw json.RawMessage
			if res.Err == nil {
				if b, err := json.Marshal(res.Value); err == nil {
					raw = b
				} else {
					res.Err = fmt.Errorf("agent result is not JSON-serializable: %w", err)
				}
			}
			e.run.updateNode(node, func(n *Node) {
				n.DoneAt = time.Now()
				n.InputTokens, n.OutputTokens = res.InputTokens, res.OutputTokens
				if res.Err != nil {
					n.Status, n.Err = NodeFailed, res.Err.Error()
				} else {
					n.Status = NodeDone
				}
			})
			if res.Note != "" {
				e.run.log(fmt.Sprintf("agent %q: %s", req.Label, res.Note))
			}
			if res.Err != nil {
				e.run.log(fmt.Sprintf("agent %q failed: %v", req.Label, res.Err))
			} else if e.opts.Journal != nil {
				e.opts.Journal.Record(key, occ, req.Label, raw)
			}
			post(func() {
				if res.Err != nil {
					_ = resolve(goja.Null())
					return
				}
				_ = resolve(fromJSON(raw))
			})
		}()
		return vm.ToValue(p)
	})

	_ = vm.Set("workflow", func(call goja.FunctionCall) goja.Value {
		if depth > 0 {
			throwErr(errors.New("workflow(): nesting is one level only"))
		}
		if e.ctx.Err() != nil {
			return neverSettles()
		}
		var ref WorkflowRef
		switch v := call.Argument(0); {
		case goja.IsUndefined(v) || goja.IsNull(v):
			panic(vm.NewTypeError("workflow() expects a name or {scriptPath}"))
		case v.ExportType() != nil && v.ExportType().Kind().String() == "string":
			ref.Name = v.String()
		default:
			sp := v.ToObject(vm).Get("scriptPath")
			if sp == nil || goja.IsUndefined(sp) {
				panic(vm.NewTypeError("workflow() expects a name or {scriptPath}"))
			}
			ref.ScriptPath = sp.String()
		}
		childArgs := toJSON(call.Argument(1))
		p, resolve, reject := vm.NewPromise()
		go func() {
			src, err := e.opts.Host.ResolveWorkflow(e.ctx, ref)
			var child *Script
			if err == nil {
				child, err = Load(src)
			}
			if err != nil {
				e.run.log("workflow() failed to start: " + err.Error())
				post(func() { _ = reject(vm.NewGoError(err)) })
				return
			}
			name := child.Meta.Name
			e.run.log("↳ running child workflow " + name)
			out, err := e.execScript(child, childArgs, name, depth+1)
			post(func() {
				if err != nil {
					_ = reject(vm.NewGoError(err))
					return
				}
				e.run.log("↳ " + name + " done")
				_ = resolve(fromJSON(out))
			})
		}()
		return vm.ToValue(p)
	})

	if _, err := vm.RunString(prelude); err != nil {
		return nil, fmt.Errorf("workflow: prelude: %w", err)
	}

	// Interrupt long-running JS (tight loops) when the run is cancelled.
	go func() {
		select {
		case <-e.ctx.Done():
			vm.Interrupt(ErrAborted)
		case <-done:
		}
	}()

	v, err := vm.RunProgram(script.program)
	if err != nil {
		return nil, scriptError(err)
	}
	mainP, ok := v.Export().(*goja.Promise)
	if !ok {
		return nil, errors.New("workflow: script did not produce a promise")
	}
	for mainP.State() == goja.PromiseStatePending {
		select {
		case job := <-jobs:
			job()
		case <-e.ctx.Done():
			return nil, ErrAborted
		}
	}
	if e.ctx.Err() != nil {
		return nil, ErrAborted
	}
	if mainP.State() == goja.PromiseStateRejected {
		return nil, fmt.Errorf("%w: %s", ErrScript, errString(mainP.Result()))
	}
	return exportResult(mainP.Result(), toJSON)
}

func exportResult(v goja.Value, toJSON func(goja.Value) json.RawMessage) (json.RawMessage, error) {
	if v == nil || goja.IsUndefined(v) {
		return json.RawMessage("null"), nil
	}
	if _, isFn := goja.AssertFunction(v); isFn {
		return nil, errors.New("workflow: result cannot be a function")
	}
	var raw json.RawMessage
	var perr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				perr = fmt.Errorf("workflow: result is not JSON-serializable (a circular reference?): %v", r)
			}
		}()
		raw = toJSON(v)
	}()
	if perr != nil {
		return nil, perr
	}
	if raw == nil {
		return json.RawMessage("null"), nil
	}
	return raw, nil
}

func scriptError(err error) error {
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		return ErrAborted
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return fmt.Errorf("%w: %s", ErrScript, ex.String())
	}
	return errors.Join(ErrScript, err)
}

func errString(v goja.Value) string {
	if v == nil {
		return "unknown error"
	}
	if obj, ok := v.(*goja.Object); ok {
		if st := obj.Get("stack"); st != nil && !goja.IsUndefined(st) {
			return st.String()
		}
	}
	return v.String()
}

func shortLabel(prompt string) string {
	s := strings.Join(strings.Fields(prompt), " ")
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}

// checkSchema enforces CC's rule: root must be {type:'object', properties}
// and required ⊆ properties, and the schema must compile.
func checkSchema(raw json.RawMessage) error {
	var root struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("schema is not an object: %w", err)
	}
	if root.Type != "object" || root.Properties == nil {
		return errors.New("schema root must be {type: 'object', properties: {...}}")
	}
	for _, r := range root.Required {
		if _, ok := root.Properties[r]; !ok {
			return fmt.Errorf("schema requires %q but does not declare it in properties", r)
		}
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("agent.json", doc); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	if _, err := c.Compile("agent.json"); err != nil {
		return fmt.Errorf("schema does not compile: %w", err)
	}
	return nil
}

// journalKey identifies an agent() call by prompt and options so a resumed
// run can reuse the result of an identical call. The script text is
// deliberately not part of the key: editing a script and resuming must
// replay the calls that did not change. CC instead chains each key on the
// previous one, which invalidates every call after the first reordering —
// and replay itself reorders pipeline continuations.
func journalKey(prompt string, opts json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(prompt))
	h.Write([]byte{0})
	h.Write(canonicalJSON(opts))
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalJSON re-encodes raw with sorted object keys so key order in the
// script does not change the journal key.
func canonicalJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v) // encoding/json sorts map keys
	if err != nil {
		return raw
	}
	return out
}
