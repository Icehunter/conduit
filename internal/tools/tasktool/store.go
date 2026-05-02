// Package tasktool implements TaskCreate, TaskGet, TaskList, TaskUpdate,
// TaskOutput, and TaskStop tools backed by an in-process task store.
//
// Mirrors src/tools/Task*Tool/ and src/utils/tasks.ts.
package tasktool

import (
	"fmt"
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
	ActiveForm  string            // present-continuous form e.g. "Running tests"
	Metadata    map[string]any
	Output      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Store is a thread-safe in-memory task store shared across all Task tools
// within one agent session.
type Store struct {
	mu      sync.RWMutex
	tasks   map[string]*Task
	ordered []string // insertion order
	nextID  int
}

// Global store for the session — tools share one instance via New*.
var globalStore = &Store{
	tasks:  make(map[string]*Task),
	nextID: 1,
}

func (s *Store) Create(subject, description, activeForm string, metadata map[string]any) *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
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
