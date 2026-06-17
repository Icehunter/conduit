// Package tasktool implements TaskCreate, TaskGet, TaskList, TaskUpdate,
// TaskOutput, and TaskStop tools backed by an in-process task store.
//
// Mirrors src/tools/Task*Tool/ and src/utils/tasks.ts.
package tasktool

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// Status mirrors TaskStatus from tasks.ts.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusCancelled  Status = "cancelled"
)

// Task is one item in the task list.
type Task struct {
	ID          string
	Subject     string
	Description string
	Status      Status
	ActiveForm  string // present-continuous form e.g. "Running tests"
	Metadata    map[string]any
	Output      string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Assignee is the teammate name that owns or is assigned this task.
	// Empty means claimable by any teammate.
	Assignee string
	// Dependencies lists task IDs that must be StatusCompleted before this
	// task is claimable. All deps must exist in the same Store.
	Dependencies []string
}

// Store is a thread-safe in-memory task store shared across all Task tools
// within one agent session.
type Store struct {
	mu      sync.RWMutex
	tasks   map[string]*Task
	ordered []string // insertion order
	nextID  int

	// OnCreated fires after a task is created (called with lock released).
	// Wire this from app/ to avoid import cycles with the hooks package.
	OnCreated func(*Task)
	// OnCompleted fires after a task reaches StatusCompleted.
	OnCompleted func(*Task)
}

// Global store for the session — tools share one instance via New*.
var globalStore = &Store{
	tasks:  make(map[string]*Task),
	nextID: 1,
}

// GlobalStore returns the shared task store for live UI consumers like
// the coordinator footer panel. Don't mutate via this handle — use the
// dedicated tools so updates flow through the same entry points.
func GlobalStore() *Store { return globalStore }

func (s *Store) Create(subject, description, activeForm string, metadata map[string]any) *Task {
	s.mu.Lock()
	id := fmt.Sprintf("task_%d", s.nextID)
	s.nextID++
	t := &Task{
		ID:          id,
		Subject:     subject,
		Description: description,
		Status:      StatusPending,
		ActiveForm:  activeForm,
		Metadata:    metadata,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.tasks[id] = t
	s.ordered = append(s.ordered, id)
	cb := s.OnCreated
	s.mu.Unlock()

	// Fire callback after releasing the lock so the callback can safely
	// call other Store methods without deadlocking.
	if cb != nil {
		cb(t)
	}
	return t
}

func (s *Store) Get(id string) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	return t, ok
}

func (s *Store) List() []*Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Task, 0, len(s.ordered))
	for _, id := range s.ordered {
		if t, ok := s.tasks[id]; ok {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) Update(id string, fn func(*Task)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("task %s not found", id)
	}
	fn(t)
	t.UpdatedAt = time.Now()
	return nil
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
	for i, oid := range s.ordered {
		if oid == id {
			s.ordered = append(s.ordered[:i], s.ordered[i+1:]...)
			break
		}
	}
}

// Claim atomically transitions a pending task to in_progress with the given
// assignee. Returns an error if:
//   - the task doesn't exist
//   - the task is not StatusPending
//   - the task has an Assignee already set to a different name
//   - any dependency is not yet StatusCompleted
func (s *Store) Claim(id, assignee string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("task %s not found", id)
	}
	if t.Status != StatusPending {
		return fmt.Errorf("task %s is not pending (status: %s)", id, t.Status)
	}
	if t.Assignee != "" && t.Assignee != assignee {
		return fmt.Errorf("task %s is pre-assigned to %q, not %q", id, t.Assignee, assignee)
	}
	for _, depID := range t.Dependencies {
		dep, exists := s.tasks[depID]
		if !exists || dep.Status != StatusCompleted {
			return fmt.Errorf("task %s has unmet dependency %s", id, depID)
		}
	}
	t.Status = StatusInProgress
	t.Assignee = assignee
	t.UpdatedAt = time.Now()
	return nil
}

// NextClaimable returns the first task in insertion order that is pending,
// has all dependencies completed, and is either unassigned or pre-assigned
// to the given assignee. Returns nil if no such task exists.
func (s *Store) NextClaimable(assignee string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.ordered {
		t := s.tasks[id]
		if t.Status != StatusPending {
			continue
		}
		if t.Assignee != "" && t.Assignee != assignee {
			continue
		}
		if s.allDepsCompletedLocked(t.Dependencies) {
			return t
		}
	}
	return nil
}

// Complete transitions a task to StatusCompleted and returns the set of
// tasks that became newly claimable (all their dependencies are now done).
// Fires OnCompleted after releasing the lock.
func (s *Store) Complete(id string) ([]*Task, error) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("task %s not found", id)
	}
	t.Status = StatusCompleted
	t.UpdatedAt = time.Now()

	// Find tasks that are newly unblocked: pending tasks whose dependency
	// set includes id and all other deps are now completed.
	var unblocked []*Task
	for _, other := range s.tasks {
		if other.ID == id || other.Status != StatusPending {
			continue
		}
		if !slices.Contains(other.Dependencies, id) {
			continue
		}
		if s.allDepsCompletedLocked(other.Dependencies) {
			unblocked = append(unblocked, other)
		}
	}
	cb := s.OnCompleted
	s.mu.Unlock()

	if cb != nil {
		cb(t)
	}
	return unblocked, nil
}

// allDepsCompletedLocked reports whether every dep ID in deps refers to a
// completed task. Caller must hold at least RLock (or any Lock).
func (s *Store) allDepsCompletedLocked(deps []string) bool {
	for _, depID := range deps {
		dep, ok := s.tasks[depID]
		if !ok || dep.Status != StatusCompleted {
			return false
		}
	}
	return true
}
