package tasktool

import (
	"context"
	"encoding/json"
	"testing"
)

// TestStore_CRUD exercises Create, Get, List, Update, and Delete.
func TestStore_CRUD(t *testing.T) {
	s := &Store{tasks: make(map[string]*Task), nextID: 1}

	task := s.Create("do the thing", "details", "Doing the thing", nil)
	if task.ID == "" {
		t.Fatal("Create returned empty ID")
	}
	if task.Status != StatusPending {
		t.Errorf("initial status = %q; want %q", task.Status, StatusPending)
	}

	got, ok := s.Get(task.ID)
	if !ok {
		t.Fatalf("Get(%q) not found", task.ID)
	}
	if got.Subject != "do the thing" {
		t.Errorf("subject = %q", got.Subject)
	}

	all := s.List()
	if len(all) != 1 {
		t.Errorf("List() len = %d; want 1", len(all))
	}

	if err := s.Update(task.ID, func(task *Task) {
		task.Status = StatusInProgress
		task.Subject = "new subject"
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	updated, _ := s.Get(task.ID)
	if updated.Status != StatusInProgress {
		t.Errorf("status after update = %q; want %q", updated.Status, StatusInProgress)
	}

	s.Delete(task.ID)
	if _, ok := s.Get(task.ID); ok {
		t.Error("Get after Delete should return false")
	}
	if len(s.List()) != 0 {
		t.Errorf("List() len after Delete = %d; want 0", len(s.List()))
	}
}

// TestStore_UpdateNotFound returns an error for unknown IDs.
func TestStore_UpdateNotFound(t *testing.T) {
	s := &Store{tasks: make(map[string]*Task), nextID: 1}
	err := s.Update("no-such-id", func(*Task) {})
	if err == nil {
		t.Error("Update nonexistent ID should return error")
	}
}

// TestTaskCreateTool_Execute checks the TaskCreate tool round-trips through JSON.
func TestTaskCreateTool_Execute(t *testing.T) {
	s := &Store{tasks: make(map[string]*Task), nextID: 1}
	tc := &TaskCreateTool{store: s}

	raw, _ := json.Marshal(map[string]string{"subject": "test task", "description": "desc"})
	res, err := tc.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("result isError; content: %v", res.Content)
	}
	if len(s.List()) != 1 {
		t.Errorf("store has %d tasks; want 1", len(s.List()))
	}
}

// TestTaskCreateTool_InvalidInput exercises the error path.
func TestTaskCreateTool_InvalidInput(t *testing.T) {
	s := &Store{tasks: make(map[string]*Task), nextID: 1}
	tc := &TaskCreateTool{store: s}
	res, err := tc.Execute(context.Background(), json.RawMessage(`{invalid json`))
	if err != nil {
		t.Fatalf("Execute returned go error: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError=true for invalid JSON input")
	}
}
