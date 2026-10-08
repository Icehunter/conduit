// Package bgjobs tracks shell commands the Bash tool started with
// run_in_background. A job runs detached from the tool call that started it,
// writes its combined output to a private file, and reports its end through
// an OnDone hook so the session can tell the model it finished.
package bgjobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/icehunter/conduit/internal/procgroup"
	"github.com/icehunter/conduit/internal/truncate"
)

// Status is where a job is in its life.
type Status string

// Job states. A job that exits 0 is completed, one that exits non-zero is
// failed, and one stopped by Kill, KillAll or its timeout is killed.
const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusKilled    Status = "killed"
)

// Timeout limits match the foreground Bash tool.
const (
	DefaultTimeout = 2 * time.Minute
	MaxTimeout     = 10 * time.Minute
)

// maxReadBytes bounds what one read pulls into memory. A job that wrote more
// since the last read is shown from its tail.
const maxReadBytes = 4 << 20

// killWait is how long Kill and KillAll wait for a killed process to be
// reaped before reporting the job as it stands.
const killWait = 3 * time.Second

// Job is a point-in-time view of a background command.
type Job struct {
	ID          string
	Command     string
	Description string
	PID         int
	StartedAt   time.Time
	DoneAt      time.Time
	Status      Status
	ExitCode    int
	OutputPath  string
	TimedOut    bool
	// Owner is the scope of the sub-agent or teammate that started the job,
	// or "" for the main loop.
	Owner string
}

// Running reports whether the job has not ended yet.
func (j Job) Running() bool { return j.Status == StatusRunning }

type job struct {
	Job
	cancel func()
	done   chan struct{}
	// killed is set when a caller asked for the stop, so a signal exit is
	// reported as killed rather than failed.
	killed bool
	// silent suppresses OnDone (session shutdown).
	silent bool
	// readOffset is how much of the output file ReadNext has returned.
	readOffset int64
}

// Registry tracks running and recently finished jobs.
type Registry struct {
	mu     sync.Mutex
	jobs   map[string]*job
	order  []string
	seq    int
	keep   int
	onDone func(Job)
	// headless is set when no session outlives the final response (print mode).
	headless atomic.Bool
}

// Default is the process-wide registry.
var Default = NewRegistry(20)

// NewRegistry returns a registry that retains at most keep finished jobs.
func NewRegistry(keep int) *Registry {
	return &Registry{jobs: map[string]*job{}, keep: keep}
}

// SetHeadless records that the session ends with the model's final response
// (print mode), so a job still running then is terminated unannounced.
func (r *Registry) SetHeadless(v bool) { r.headless.Store(v) }

// Headless reports whether SetHeadless was set.
func (r *Registry) Headless() bool { return r.headless.Load() }

// SetOnDone installs the hook called once for each main-loop job that ends on
// its own or times out. It runs on the job's own goroutine after the job's
// state is final. A job stopped by Stop, Kill, KillAll or KillOwned is
// silent, as is any job owned by a sub-agent or teammate: nothing tells the
// model about a stop it asked for, and a child's jobs have no loop to tell.
func (r *Registry) SetOnDone(fn func(Job)) {
	r.mu.Lock()
	r.onDone = fn
	r.mu.Unlock()
}

// Start launches command under `bash -c` in its own process group and
// returns at once. timeout <= 0 means DefaultTimeout; it is capped at
// MaxTimeout. env entries are added to the process environment.
func (r *Registry) Start(command, description string, timeout time.Duration, cwd string, env []string) (Job, error) {
	return r.StartOwned("", command, description, timeout, cwd, env)
}

// StartOwned is Start for a job owned by the sub-agent or teammate with the
// given scope ("" for the main loop). A child's job never fires OnDone and is
// stopped by KillOwned when the child ends.
func (r *Registry) StartOwned(owner, command, description string, timeout time.Duration, cwd string, env []string) (Job, error) {
	if command == "" {
		return Job{}, errors.New("bgjobs: command is empty")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timeout = min(timeout, MaxTimeout)

	out, err := truncate.NewFile()
	if err != nil {
		return Job{}, fmt.Errorf("bgjobs: create output file: %w", err)
	}
	// Detached from the calling tool's context: the job outlives the turn.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	procgroup.Set(cmd)
	cmd.Dir = cwd
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	// Children the command backgrounds itself (`foo &`) are in the same
	// process group but can outlive bash; the job ends when bash does, and a
	// kill signals the group, which only reaches the ones still running.
	// The file is the process's stdout and stderr directly, so no copier
	// goroutine holds the call open on a grandchild that kept the descriptor.
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		cancel()
		_ = out.Close()
		_ = os.Remove(out.Name())
		return Job{}, fmt.Errorf("bgjobs: start: %w", err)
	}
	// The child holds its own descriptor now.
	_ = out.Close()

	j := &job{cancel: cancel, done: make(chan struct{}), silent: owner != ""}
	j.Owner = owner
	j.Command = command
	j.Description = description
	j.PID = cmd.Process.Pid
	j.StartedAt = time.Now()
	j.Status = StatusRunning
	j.OutputPath = out.Name()

	r.mu.Lock()
	r.seq++
	j.ID = fmt.Sprintf("bash_%d", r.seq)
	r.jobs[j.ID] = j
	r.order = append(r.order, j.ID)
	snap := j.Job
	r.mu.Unlock()

	go r.wait(ctx, j, cmd)
	return snap, nil
}

// wait reaps the process, records how it ended and fires OnDone.
func (r *Registry) wait(ctx context.Context, j *job, cmd *exec.Cmd) {
	waitErr := cmd.Wait()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	j.cancel()

	r.mu.Lock()
	j.DoneAt = time.Now()
	j.ExitCode = cmd.ProcessState.ExitCode()
	switch {
	case j.killed && waitErr != nil:
		// A kill that lost the race to a clean exit leaves a completed job.
		j.Status = StatusKilled
	case timedOut && waitErr != nil:
		j.Status = StatusKilled
		j.TimedOut = true
	case waitErr != nil:
		j.Status = StatusFailed
	default:
		j.Status = StatusCompleted
	}
	snap := j.Job
	fn := r.onDone
	silent := j.silent
	r.evictLocked()
	r.mu.Unlock()
	close(j.done)

	if fn != nil && !silent {
		fn(snap)
	}
}

// Get returns the job with id.
func (r *Registry) Get(id string) (Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return Job{}, false
	}
	return j.Job, true
}

// Snapshot returns every retained job, running first (newest first), then
// finished (most recently finished first).
func (r *Registry) Snapshot() []Job {
	r.mu.Lock()
	out := make([]Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j.Job)
	}
	r.mu.Unlock()
	sort.SliceStable(out, func(i, k int) bool {
		ri, rk := out[i].Running(), out[k].Running()
		if ri != rk {
			return ri
		}
		if ri {
			return out[i].StartedAt.After(out[k].StartedAt)
		}
		return out[i].DoneAt.After(out[k].DoneAt)
	})
	return out
}

// Running reports how many jobs have not ended.
func (r *Registry) Running() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, j := range r.jobs {
		if j.Status == StatusRunning {
			n++
		}
	}
	return n
}

// Stop signals the job's process group to die and returns at once; the job
// is reported killed when the process has been reaped. A stop the caller asked
// for is silent: OnDone does not fire.
func (r *Registry) Stop(id string) error {
	_, err := r.stop(id)
	return err
}

func (r *Registry) stop(id string) (*job, error) {
	r.mu.Lock()
	j, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("bgjobs: no background job %q", id)
	}
	if j.Status == StatusRunning {
		j.killed = true
		j.silent = true
	}
	r.mu.Unlock()
	j.cancel()
	return j, nil
}

// Kill is Stop that also waits for the process to be reaped (or a short
// while) and returns the job as it stands. Killing a finished job is not an
// error; it returns the job as it ended.
func (r *Registry) Kill(id string) (Job, error) {
	j, err := r.stop(id)
	if err != nil {
		return Job{}, err
	}
	select {
	case <-j.done:
	case <-time.After(killWait):
	}
	snap, _ := r.Get(id)
	return snap, nil
}

// KillOwned stops every running job owned by scope without notifying anyone
// (the sub-agent or teammate that started them has finished).
func (r *Registry) KillOwned(scope string) {
	if scope == "" {
		return
	}
	r.killWhere(func(j *job) bool { return j.Owner == scope })
}

// KillAll stops every running job without firing OnDone (session shutdown).
func (r *Registry) KillAll() {
	r.killWhere(func(*job) bool { return true })
}

func (r *Registry) killWhere(match func(*job) bool) {
	r.mu.Lock()
	var running []*job
	for _, j := range r.jobs {
		if j.Status == StatusRunning && match(j) {
			j.killed = true
			j.silent = true
			running = append(running, j)
		}
	}
	r.mu.Unlock()

	deadline := time.After(killWait)
	for _, j := range running {
		j.cancel()
	}
	for _, j := range running {
		select {
		case <-j.done:
		case <-deadline:
			return
		}
	}
}

// Reset drops every job (tests). Running jobs are not stopped.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = map[string]*job{}
	r.order = nil
}

// Output is what ReadNext and ReadOutput return.
type Output struct {
	Text string
	// Offset is where the next read from this point starts.
	Offset int64
	// Skipped is how many bytes before Text were left out because more than
	// maxReadBytes was waiting.
	Skipped int64
	Job     Job
}

// ReadOutput returns the job's output from byte offset since to the end,
// without touching the job's own read position.
func (r *Registry) ReadOutput(id string, since int64) (Output, error) {
	snap, ok := r.Get(id)
	if !ok {
		return Output{}, fmt.Errorf("bgjobs: no background job %q", id)
	}
	text, offset, skipped, err := readFrom(snap.OutputPath, since, maxReadBytes)
	if err != nil {
		return Output{}, err
	}
	return Output{Text: text, Offset: offset, Skipped: skipped, Job: snap}, nil
}

// ReadNext returns the output written since the previous ReadNext for this
// job and advances the job's read position. While the job runs, a trailing
// partial line is held back until its newline arrives, so callers see whole
// lines; once the job has ended everything left is returned.
func (r *Registry) ReadNext(id string) (Output, error) {
	r.mu.Lock()
	j, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return Output{}, fmt.Errorf("bgjobs: no background job %q", id)
	}
	// Status is read before the file: a job that has ended by now has
	// written everything the read will find.
	snap := j.Job
	since := j.readOffset
	r.mu.Unlock()

	text, offset, skipped, err := readFrom(snap.OutputPath, since, maxReadBytes)
	if err != nil {
		return Output{}, err
	}
	if snap.Running() {
		if cut := strings.LastIndexByte(text, '\n') + 1; cut < len(text) {
			offset -= int64(len(text) - cut)
			text = text[:cut]
		}
	}
	r.mu.Lock()
	// Two readers racing keep the furthest position.
	if offset > j.readOffset {
		j.readOffset = offset
	}
	r.mu.Unlock()
	return Output{Text: text, Offset: offset, Skipped: skipped, Job: snap}, nil
}

// Tail returns the last n bytes of the job's output, starting on a
// character boundary.
func (r *Registry) Tail(id string, n int64) (string, error) {
	snap, ok := r.Get(id)
	if !ok {
		return "", fmt.Errorf("bgjobs: no background job %q", id)
	}
	text, _, _, err := readFrom(snap.OutputPath, 0, n)
	return text, err
}

// readFrom reads up to limit bytes of path starting at since. When more than
// limit bytes remain it returns the last limit of them and counts the rest as
// skipped. offset is the end of what was read.
func readFrom(path string, since, limit int64) (text string, offset, skipped int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", since, 0, fmt.Errorf("bgjobs: open output: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", since, 0, fmt.Errorf("bgjobs: stat output: %w", err)
	}
	size := st.Size()
	if since >= size {
		return "", size, 0, nil
	}
	start := since
	if size-start > limit {
		start = size - limit
		// Land on the start of a character, not inside one.
		for start < size && start > 0 {
			var b [1]byte
			if _, err := f.ReadAt(b[:], start); err != nil || b[0]&0xC0 != 0x80 {
				break
			}
			start++
		}
		skipped = start - since
	}
	buf := make([]byte, size-start)
	n, err := io.ReadFull(io.NewSectionReader(f, start, size-start), buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", since, 0, fmt.Errorf("bgjobs: read output: %w", err)
	}
	return string(buf[:n]), start + int64(n), skipped, nil
}

// evictLocked drops the oldest finished jobs past keep.
func (r *Registry) evictLocked() {
	finished := 0
	for _, id := range r.order {
		if r.jobs[id].Status != StatusRunning {
			finished++
		}
	}
	for i := 0; i < len(r.order) && finished > r.keep; i++ {
		id := r.order[i]
		if r.jobs[id].Status == StatusRunning {
			continue
		}
		delete(r.jobs, id)
		r.order = append(r.order[:i], r.order[i+1:]...)
		i--
		finished--
	}
}
