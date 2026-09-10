package workflow

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// NodeStatus is the lifecycle of one agent() call.
type NodeStatus string

const (
	NodeQueued  NodeStatus = "queued"  // waiting for a concurrency slot
	NodeRunning NodeStatus = "running" // sub-agent in flight
	NodeDone    NodeStatus = "done"    // returned a value
	NodeFailed  NodeStatus = "failed"  // terminal error; script saw null
	NodeCached  NodeStatus = "cached"  // replayed from the journal on resume
)

// Node is one agent() call as seen by the UI and the journal.
type Node struct {
	Seq          int
	Label        string
	Phase        string
	Status       NodeStatus
	StartedAt    time.Time
	DoneAt       time.Time
	InputTokens  int
	OutputTokens int
	Err          string
	// Child is the name of the child workflow this node belongs to, or "".
	Child string
}

// LogLine is one narrator line from log().
type LogLine struct {
	At   time.Time
	Text string
}

// RunStatus is the lifecycle of a whole run.
type RunStatus string

const (
	RunRunning RunStatus = "running"
	RunDone    RunStatus = "done"
	RunFailed  RunStatus = "failed"
	RunKilled  RunStatus = "killed"
)

// Run is the observable state of one workflow execution. All methods are
// goroutine-safe; the UI reads via Snapshot.
type Run struct {
	ID         string
	Name       string
	ScriptPath string
	ScriptHash string
	Args       json.RawMessage
	StartedAt  time.Time

	mu           sync.RWMutex
	status       RunStatus
	doneAt       time.Time
	phases       []Phase // meta.phases plus any phase() titles not declared
	currentPhase string
	nodes        []*Node
	logs         []LogLine
	result       json.RawMessage
	err          string
	budgetTotal  int
	spent        int
	cancel       context.CancelFunc
}

// Snapshot is a consistent copy of a Run for rendering.
type Snapshot struct {
	ID           string
	Name         string
	ScriptPath   string
	Status       RunStatus
	StartedAt    time.Time
	DoneAt       time.Time
	Phases       []Phase
	CurrentPhase string
	Nodes        []Node
	Logs         []LogLine
	Result       json.RawMessage
	Err          string
	BudgetTotal  int
	Spent        int
	InputTokens  int
	OutputTokens int
}

// Snapshot returns a copy of the run state.
func (r *Run) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := Snapshot{
		ID:           r.ID,
		Name:         r.Name,
		ScriptPath:   r.ScriptPath,
		Status:       r.status,
		StartedAt:    r.StartedAt,
		DoneAt:       r.doneAt,
		Phases:       append([]Phase(nil), r.phases...),
		CurrentPhase: r.currentPhase,
		Nodes:        make([]Node, 0, len(r.nodes)),
		Logs:         append([]LogLine(nil), r.logs...),
		Result:       r.result,
		Err:          r.err,
		BudgetTotal:  r.budgetTotal,
		Spent:        r.spent,
	}
	for _, n := range r.nodes {
		s.Nodes = append(s.Nodes, *n)
		s.InputTokens += n.InputTokens
		s.OutputTokens += n.OutputTokens
	}
	return s
}

// Status returns the run status.
func (r *Run) Status() RunStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

// Kill cancels the run's context. Safe to call more than once.
func (r *Run) Kill() {
	r.mu.Lock()
	cancel := r.cancel
	if r.status == RunRunning {
		r.status = RunKilled
		r.doneAt = time.Now()
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Run) setPhase(title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentPhase = title
	r.ensurePhaseLocked(title)
}

// ensurePhase registers a progress group for title without making it current
// (used for an explicit opts.phase on agent()).
func (r *Run) ensurePhase(title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensurePhaseLocked(title)
}

func (r *Run) ensurePhaseLocked(title string) {
	for _, p := range r.phases {
		if p.Title == title {
			return
		}
	}
	r.phases = append(r.phases, Phase{Title: title})
}

func (r *Run) phaseOr(explicit string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if explicit != "" {
		return explicit
	}
	return r.currentPhase
}

func (r *Run) log(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, LogLine{At: time.Now(), Text: text})
}

func (r *Run) addNode(n *Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes = append(r.nodes, n)
}

func (r *Run) updateNode(n *Node, fn func(*Node)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(n)
}

func (r *Run) addSpent(outputTokens int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spent += outputTokens
	return r.spent
}

func (r *Run) budget() (total, spent int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.budgetTotal, r.spent
}

func (r *Run) finish(result json.RawMessage, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == RunKilled {
		return
	}
	r.doneAt = time.Now()
	if err != nil {
		r.status = RunFailed
		r.err = err.Error()
		return
	}
	r.status = RunDone
	r.result = result
}
