package todowritetool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func input(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, _ := json.Marshal(v)
	return b
}

func todos(items ...map[string]any) map[string]any {
	return map[string]any{"todos": items}
}

func item(id, content, status string) map[string]any {
	return map[string]any{"id": id, "content": content, "status": status}
}

func reset() {
	store.mu.Lock()
	store.todos = nil
	store.mu.Unlock()
}

func TestTodoWrite_SetsTodos(t *testing.T) {
	reset()
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, todos(
		item("1", "Write tests", "pending"),
		item("2", "Implement", "in_progress"),
	)))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	got := GetTodos()
	if len(got) != 2 {
		t.Fatalf("expected 2 todos, got %d", len(got))
	}
	if got[0].Content != "Write tests" {
		t.Errorf("todo[0].Content = %q", got[0].Content)
	}
}

func TestTodoWrite_AllCompletedClearsList(t *testing.T) {
	reset()
	tt := New()
	_, _ = tt.Execute(context.Background(), input(t, todos(
		item("1", "Task A", "completed"),
		item("2", "Task B", "completed"),
	)))
	got := GetTodos()
	if len(got) != 0 {
		t.Errorf("all-completed should clear list; got %d items", len(got))
	}
}

func TestTodoWrite_PartialCompletedKeepsList(t *testing.T) {
	reset()
	tt := New()
	_, _ = tt.Execute(context.Background(), input(t, todos(
		item("1", "Done", "completed"),
		item("2", "Still pending", "pending"),
	)))
	got := GetTodos()
	if len(got) != 2 {
		t.Errorf("partial completed should keep list; got %d items", len(got))
	}
}

func TestTodoWrite_EmptyListAccepted(t *testing.T) {
	reset()
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, todos()))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
}

func TestTodoWrite_ReplacesExistingList(t *testing.T) {
	reset()
	tt := New()
	_, _ = tt.Execute(context.Background(), input(t, todos(item("1", "Old task", "pending"))))
	_, _ = tt.Execute(context.Background(), input(t, todos(item("2", "New task", "in_progress"))))
	got := GetTodos()
	if len(got) != 1 || got[0].Content != "New task" {
		t.Errorf("list not replaced; got %+v", got)
	}
}

func TestTodoWrite_InvalidJSON(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), json.RawMessage(`{bad`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should be IsError=true")
	}
}

func TestTodoWrite_ResultMessageContainsContinue(t *testing.T) {
	reset()
	tt := New()
	res, _ := tt.Execute(context.Background(), input(t, todos(item("1", "x", "pending"))))
	if !strings.Contains(res.Content[0].Text, "proceed") {
		t.Errorf("result should nudge model to proceed; got: %s", res.Content[0].Text)
	}
}

func TestFormatTodos(t *testing.T) {
	todos := []Todo{
		{ID: "1", Content: "pending task", Status: StatusPending},
		{ID: "2", Content: "active task", Status: StatusInProgress},
		{ID: "3", Content: "done task", Status: StatusCompleted},
	}
	out := FormatTodos(todos)
	if !strings.Contains(out, "○") || !strings.Contains(out, "◉") || !strings.Contains(out, "✓") {
		t.Errorf("FormatTodos output: %q", out)
	}
}

func TestTodoWrite_StaticMetadata(t *testing.T) {
	tt := New()
	if tt.Name() != "TodoWrite" {
		t.Errorf("Name = %q", tt.Name())
	}
	if tt.IsReadOnly(nil) {
		t.Error("IsReadOnly should be false")
	}
	var schema map[string]any
	if err := json.Unmarshal(tt.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
}
